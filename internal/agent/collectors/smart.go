package collectors

import (
	"context"
	"encoding/json"
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

type smartCollector struct {
	mu         sync.Mutex
	probed     bool
	smartctl   string
	devices    []string
	devicesAt  time.Time
	devicesTTL time.Duration
}

func init() { Register(&smartCollector{}) }

func (c *smartCollector) Name() string        { return "smart" }
func (c *smartCollector) Platforms() []string { return []string{"linux", "darwin", "windows"} }

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
		return false
	}
	if time.Since(c.devicesAt) < c.devicesTTL && c.devices != nil {
		return true
	}
	out, err := run(ctx, c.smartctl, "--scan", "--json=c")
	if err != nil {
		return false
	}
	var scan struct {
		Devices []struct {
			Name string `json:"name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &scan); err != nil {
		return false
	}
	c.devices = c.devices[:0]
	for _, d := range scan.Devices {
		c.devices = append(c.devices, d.Name)
	}
	c.devicesAt = time.Now()
	return len(c.devices) > 0
}

func (c *smartCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	if !c.ensure(ctx) {
		return nil, nil
	}
	now := time.Now()
	out := make([]wire.Point, 0, len(c.devices)*4)
	for _, dev := range c.devices {
		select {
		case <-ctx.Done():
			return out, nil
		default:
		}
		body, err := run(ctx, c.smartctl, "-a", "--json=c", dev)
		if err != nil {
			continue
		}
		var s smartView
		if err := json.Unmarshal(body, &s); err != nil {
			continue
		}
		labels := map[string]string{"device": strings.TrimPrefix(dev, "/dev/")}
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
		for _, a := range s.AtaSmartAttributes.Table {
			switch a.Name {
			case "Reallocated_Sector_Ct":
				out = append(out, point(now, metrics.SmartReallocSectors, labels, float64(a.Raw.Value)))
			case "Current_Pending_Sector":
				out = append(out, point(now, metrics.SmartPendingSectors, labels, float64(a.Raw.Value)))
			}
		}
	}
	return out, nil
}

type smartView struct {
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
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
