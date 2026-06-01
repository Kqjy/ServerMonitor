package collectors

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	gopsnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

const (
	sockStream = 1
	sockDgram  = 2
)

const (
	connStateOK       = "ok"
	connStateNoOwners = "no_owners"
)

type connCollector struct {
	mu       sync.Mutex
	cached   []wire.Port
	state    string
	stateMsg string
	warned   bool
}

func init() { Register(&connCollector{}) }

func (c *connCollector) Name() string        { return "connections" }
func (c *connCollector) Platforms() []string { return []string{"linux", "darwin", "windows", "freebsd"} }

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
		slog.Warn("listening-port owner attribution failed", "collector", "connections", "detail", msg)
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
	if total == 0 || resolved > 0 {
		c.state, c.stateMsg, c.warned = connStateOK, "", false
		return false
	}
	c.state = connStateNoOwners
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
