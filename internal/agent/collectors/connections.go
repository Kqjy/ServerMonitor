package collectors

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	gopsnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	agentbackup "servermonitor/internal/agent/backup"
	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

const (
	sockStream = 1
	sockDgram  = 2
)

const (
	connStateOK            = "ok"
	connStateNoOwners      = "no_owners"
	connStatePartialOwners = "partial_owners"
)

type connCollector struct {
	mu       sync.Mutex
	cached   []wire.Port
	state    string
	stateMsg string
	warned   bool
}

func init() { Register(&connCollector{}) }

func (c *connCollector) Name() string { return "connections" }
func (c *connCollector) Platforms() []string {
	return []string{"linux", "darwin", "windows", "freebsd"}
}

func (c *connCollector) Status() wire.CollectorStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state
	if state == "" {
		state = connStateOK
	}
	return wire.CollectorStatus{State: state, Message: c.stateMsg}
}

func (c *connCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	tcpConns, err := gopsnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, err
	}
	udpConns, _ := gopsnet.ConnectionsWithContext(ctx, "udp")

	var established, listen, timeWait, ports int
	listening := make([]gopsnet.ConnectionStat, 0, 32)
	for _, conn := range tcpConns {
		switch conn.Status {
		case "ESTABLISHED":
			established++
		case "LISTEN":
			listen++
			ports++
			if conn.Laddr.Port > 0 {
				listening = append(listening, conn)
			}
		case "TIME_WAIT":
			timeWait++
		}
	}
	for _, conn := range udpConns {
		if conn.Laddr.Port > 0 {
			listening = append(listening, conn)
		}
	}

	nameByPID := make(map[int32]string, 16)
	for _, conn := range listening {
		if conn.Pid > 0 {
			nameByPID[conn.Pid] = ""
		}
	}
	for pid := range nameByPID {
		if p, err := process.NewProcessWithContext(ctx, pid); err == nil {
			if n, err := p.NameWithContext(ctx); err == nil {
				nameByPID[pid] = n
			}
		}
	}

	out := make([]wire.Port, 0, len(listening))
	for _, conn := range listening {
		proto := protoLabel(conn)
		if proto == "" {
			continue
		}
		out = append(out, wire.Port{
			Time:    now,
			Proto:   proto,
			Addr:    conn.Laddr.IP,
			Port:    uint16(conn.Laddr.Port),
			PID:     conn.Pid,
			Process: nameByPID[conn.Pid],
		})
	}

	total := len(listening)
	resolved := 0
	for _, conn := range listening {
		if conn.Pid > 0 {
			resolved++
		}
	}

	c.mu.Lock()
	c.cached = out
	logHint := c.updateOwnerStateLocked(resolved, total)
	msg := c.stateMsg
	c.mu.Unlock()

	if logHint {
		logPortOwnerHint(msg)
	}

	return []wire.Point{
		point(now, metrics.ConnEstab, nil, float64(established)),
		point(now, metrics.ConnListen, nil, float64(listen)),
		point(now, metrics.ConnTimeWait, nil, float64(timeWait)),
		point(now, metrics.PortOpen, nil, float64(ports)),
	}, nil
}

func (c *connCollector) CollectPorts(ctx context.Context) ([]wire.Port, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]wire.Port, len(c.cached))
	copy(out, c.cached)
	return out, nil
}

func protoLabel(c gopsnet.ConnectionStat) string {
	isV6 := strings.Contains(c.Laddr.IP, ":")
	switch c.Type {
	case sockStream:
		if isV6 {
			return "tcp6"
		}
		return "tcp"
	case sockDgram:
		if isV6 {
			return "udp6"
		}
		return "udp"
	}
	return ""
}

func (c *connCollector) updateOwnerStateLocked(resolved, total int) bool {
	state := connStateOK
	if total > 0 {
		switch {
		case resolved == 0:
			state = connStateNoOwners
		case resolved*2 < total:
			state = connStatePartialOwners
		}
	}
	if state == connStateOK {
		c.state, c.stateMsg, c.warned = connStateOK, "", false
		return false
	}
	c.state = state
	c.stateMsg = portOwnerHint(resolved, total)
	if c.warned {
		return false
	}
	c.warned = true
	return true
}

func portOwnerHint(resolved, total int) string {
	base := fmt.Sprintf("resolved %d/%d listening-socket owners", resolved, total)
	if runtime.GOOS == "linux" {
		return base + " — agent likely lacks CAP_SYS_PTRACE or is AppArmor/LSM-confined"
	}
	return base + " — agent lacks privilege to read listening-socket owners"
}

func logPortOwnerHint(detail string) {
	if runtime.GOOS != "linux" {
		slog.Warn("listening-port owner attribution failed", "collector", "connections", "detail", detail)
		return
	}
	status, err := os.ReadFile("/proc/self/status")
	dacReadSearch, sysPtrace, decoded := decodePortOwnerCapabilities(string(status))
	if err == nil && decoded && !dacReadSearch && !sysPtrace {
		slog.Info("port-owner attribution is off (opt-in: --enable-port-owners / SM_ENABLE_PORT_OWNERS)", "collector", "connections", "detail", detail)
		return
	}
	if err == nil && decoded {
		detail += " — port-owner capabilities are present; check AppArmor/LSM confinement"
	}
	if agentbackup.IsContainerized() {
		detail += " — see deploy/AGENT-DOCKER.md § Port / process owner attribution"
	}
	slog.Warn("listening-port owner attribution failed", "collector", "connections", "detail", detail)
}

func decodePortOwnerCapabilities(status string) (bool, bool, bool) {
	for _, line := range strings.Split(status, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "CapEff" {
			continue
		}
		bits, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return false, false, false
		}
		return bits&(1<<2) != 0, bits&(1<<19) != 0, true
	}
	return false, false, false
}
