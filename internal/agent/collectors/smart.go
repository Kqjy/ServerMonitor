package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

var smartctlCandidates = map[string][]string{
	"linux": {
		"/usr/sbin/smartctl",
		"/usr/local/sbin/smartctl",
		"/sbin/smartctl",
		"/usr/bin/smartctl",
	},
	"darwin": {
		"/usr/local/sbin/smartctl",
		"/opt/homebrew/sbin/smartctl",
		"/usr/sbin/smartctl",
	},
	"windows": {
		`C:\Program Files\smartmontools\bin\smartctl.exe`,
		`C:\Program Files (x86)\smartmontools\bin\smartctl.exe`,
	},
}

const (
	smartStateUnknown    = "unknown"
	smartStateOK         = "ok"
	smartStateBinary     = "binary_missing"
	smartStateScanFail   = "scan_failed"
	smartStateNoDevs     = "no_devices"
	smartStateReadFailed = "read_failed"
)

type smartDevice struct {
	name    string
	devType string
	isNVMe  bool
}

type smartCollector struct {
	mu         sync.Mutex
	probed     bool
	smartctl   string
	devices    []smartDevice
	devicesAt  time.Time
	devicesTTL time.Duration
	state      string
	stateMsg   string
	warned     bool
}

func init() { Register(&smartCollector{}) }

func (c *smartCollector) Name() string        { return "smart" }
func (c *smartCollector) Platforms() []string { return []string{"linux", "darwin", "windows"} }

func (c *smartCollector) Status() wire.CollectorStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state
	if state == "" {
		state = smartStateUnknown
	}
	return wire.CollectorStatus{State: state, Message: c.stateMsg}
}

func (c *smartCollector) setStateLocked(state, msg string) {
	c.state = state
	c.stateMsg = msg
}

func (c *smartCollector) ensure(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.devicesTTL == 0 {
		c.devicesTTL = 5 * time.Minute
	}
	if !c.probed {
		c.probed = true
		c.smartctl = resolveTrustedBin(smartctlCandidates[runtime.GOOS])
	}
	if c.smartctl == "" {
		c.setStateLocked(smartStateBinary, "smartctl binary not found; install smartmontools")
		return false
	}
	if time.Since(c.devicesAt) < c.devicesTTL && c.devices != nil {
		return len(c.devices) > 0
	}
	out, err := run(ctx, c.smartctl, "--scan", "--json=c")
	if err != nil {
		c.setStateLocked(smartStateScanFail, "smartctl --scan failed: "+err.Error())
		return false
	}
	var scan struct {
		Devices []struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Protocol string `json:"protocol"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &scan); err != nil {
		c.setStateLocked(smartStateScanFail, "smartctl --scan returned unparseable output")
		return false
	}
	c.devices = c.devices[:0]
	for _, d := range scan.Devices {
		c.devices = append(c.devices, smartDevice{name: d.Name, devType: d.Type, isNVMe: isNVMeDevice(d.Name, d.Type, d.Protocol)})
	}
	c.devicesAt = time.Now()
	if len(c.devices) == 0 {
		c.setStateLocked(smartStateNoDevs, "smartctl scanned successfully but reports no SMART-capable devices")
		return false
	}
	c.setStateLocked(smartStateOK, "")
	return true
}

func (c *smartCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	if !c.ensure(ctx) {
		return nil, nil
	}
	now := time.Now()
	out := make([]wire.Point, 0, len(c.devices)*4)
	readOK, failed, nvmeFailed := 0, 0, 0
	for _, dev := range c.devices {
		select {
		case <-ctx.Done():
			return out, nil
		default:
		}
		args := []string{"-a", "--json=c"}
		if dev.devType != "" {
			args = append(args, "-d", dev.devType)
		}
		args = append(args, dev.name)
		body, _ := run(ctx, c.smartctl, args...)
		var s smartView
		parseErr := json.Unmarshal(body, &s)
		exitStatus := 0
		if s.Smartctl != nil {
			exitStatus = s.Smartctl.ExitStatus
		}
		if parseErr != nil || smartCannotRead(exitStatus) {
			failed++
			if dev.isNVMe {
				nvmeFailed++
			}
			continue
		}
		readOK++
		labels := map[string]string{"device": deviceLabel(dev.name, dev.devType)}
		out = append(out, smartPoints(now, labels, &s)...)
	}

	c.mu.Lock()
	logHint := c.updateReadStateLocked(readOK, failed, nvmeFailed)
	msg := c.stateMsg
	c.mu.Unlock()
	if logHint {
		slog.Warn("smart per-device read failed", "collector", "smart", "detail", msg)
	}
	return out, nil
}

func (c *smartCollector) updateReadStateLocked(readOK, failed, nvmeFailed int) bool {
	if failed == 0 {
		c.setStateLocked(smartStateOK, "")
		c.warned = false
		return false
	}
	c.setStateLocked(smartStateReadFailed, smartReadFailHint(readOK, failed, nvmeFailed))
	if c.warned {
		return false
	}
	c.warned = true
	return true
}

func smartReadFailHint(readOK, failed, nvmeFailed int) string {
	base := fmt.Sprintf("read SMART data from %d of %d devices", readOK, readOK+failed)
	if nvmeFailed > 0 && runtime.GOOS == "linux" {
		return fmt.Sprintf("%s; %d NVMe device(s) returned no data — NVMe SMART needs CAP_SYS_ADMIN, which CAP_SYS_RAWIO does not grant", base, nvmeFailed)
	}
	return base + "; smartctl could not read the rest — check that the agent has the privileges its devices require"
}

const nvmeDataUnitBytes = 512000

func deviceLabel(name, devType string) string {
	label := strings.TrimPrefix(name, "/dev/")
	if i := strings.LastIndex(devType, ","); i >= 0 {
		label += "#" + devType[i+1:]
	}
	return label
}

func smartPoints(now time.Time, labels map[string]string, s *smartView) []wire.Point {
	out := make([]wire.Point, 0, 12)
	if s.Temperature != nil && s.Temperature.Current > 0 {
		out = append(out, point(now, metrics.SmartTempC, labels, float64(s.Temperature.Current)))
	}
	if s.PowerOnTime != nil && s.PowerOnTime.Hours > 0 {
		out = append(out, point(now, metrics.SmartPowerOnHours, labels, float64(s.PowerOnTime.Hours)))
	}
	if s.SmartStatus != nil {
		healthy := 0.0
		if s.SmartStatus.Passed {
			healthy = 1
		}
		out = append(out, point(now, metrics.SmartHealthy, labels, healthy))
	}
	if n := s.NVMeLog; n != nil {
		if n.DataUnitsWritten > 0 {
			out = append(out, point(now, metrics.SmartDataWrittenBytes, labels, float64(n.DataUnitsWritten)*nvmeDataUnitBytes))
		}
		if n.DataUnitsRead > 0 {
			out = append(out, point(now, metrics.SmartDataReadBytes, labels, float64(n.DataUnitsRead)*nvmeDataUnitBytes))
		}
		out = append(out, point(now, metrics.SmartPercentUsed, labels, float64(n.PercentageUsed)))
		out = append(out, point(now, metrics.SmartAvailableSpare, labels, float64(n.AvailableSpare)))
		out = append(out, point(now, metrics.SmartMediaErrors, labels, float64(n.MediaErrors)))
		out = append(out, point(now, metrics.SmartPowerCycles, labels, float64(n.PowerCycles)))
		out = append(out, point(now, metrics.SmartUnsafeShutdowns, labels, float64(n.UnsafeShutdowns)))
	}
	sectorBytes := s.LogicalBlockSize
	if sectorBytes <= 0 {
		sectorBytes = 512
	}
	for _, a := range s.AtaSmartAttributes.Table {
		switch a.Name {
		case "Reallocated_Sector_Ct":
			out = append(out, point(now, metrics.SmartReallocSectors, labels, float64(a.Raw.Value)))
		case "Current_Pending_Sector":
			out = append(out, point(now, metrics.SmartPendingSectors, labels, float64(a.Raw.Value)))
		case "Offline_Uncorrectable":
			out = append(out, point(now, metrics.SmartOfflineUncorrect, labels, float64(a.Raw.Value)))
		case "UDMA_CRC_Error_Count":
			out = append(out, point(now, metrics.SmartCRCErrors, labels, float64(a.Raw.Value)))
		case "Power_Cycle_Count":
			out = append(out, point(now, metrics.SmartPowerCycles, labels, float64(a.Raw.Value)))
		case "Total_LBAs_Written":
			out = append(out, point(now, metrics.SmartDataWrittenBytes, labels, float64(a.Raw.Value)*float64(sectorBytes)))
		case "Total_LBAs_Read":
			out = append(out, point(now, metrics.SmartDataReadBytes, labels, float64(a.Raw.Value)*float64(sectorBytes)))
		}
	}
	return out
}

func isNVMeDevice(name, typ, protocol string) bool {
	if strings.EqualFold(protocol, "nvme") {
		return true
	}
	if strings.HasPrefix(strings.ToLower(typ), "nvme") {
		return true
	}
	return strings.HasPrefix(name, "/dev/nvme")
}

const smartctlOpenFailMask = 0x03

func smartCannotRead(exitStatus int) bool {
	return exitStatus&smartctlOpenFailMask != 0
}

type smartctlMeta struct {
	ExitStatus int `json:"exit_status"`
}

type smartView struct {
	Smartctl    *smartctlMeta `json:"smartctl"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	LogicalBlockSize int64 `json:"logical_block_size"`
	NVMeLog          *struct {
		DataUnitsWritten int64 `json:"data_units_written"`
		DataUnitsRead    int64 `json:"data_units_read"`
		PercentageUsed   int   `json:"percentage_used"`
		AvailableSpare   int   `json:"available_spare"`
		MediaErrors      int64 `json:"media_errors"`
		PowerCycles      int64 `json:"power_cycles"`
		UnsafeShutdowns  int64 `json:"unsafe_shutdowns"`
	} `json:"nvme_smart_health_information_log"`
	AtaSmartAttributes struct {
		Table []struct {
			Name string `json:"name"`
			Raw  struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return exec.CommandContext(cmdCtx, name, args...).Output()
}
