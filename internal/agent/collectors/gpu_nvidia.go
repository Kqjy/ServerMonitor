package collectors

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

var nvidiaSMICandidates = map[string][]string{
	"linux": {
		"/usr/bin/nvidia-smi",
		"/usr/local/bin/nvidia-smi",
	},
	"windows": {
		`C:\Windows\System32\nvidia-smi.exe`,
		`C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
	},
}

type gpuCollector struct {
	mu        sync.Mutex
	probed    bool
	bin       string
	available bool
}

func init() { Register(&gpuCollector{}) }

func (c *gpuCollector) Name() string        { return "gpu" }
func (c *gpuCollector) Platforms() []string { return []string{"linux", "windows"} }

func (c *gpuCollector) probe() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probed {
		return
	}
	c.probed = true
	bin := resolveTrustedBin(nvidiaSMICandidates[runtime.GOOS])
	if bin == "" {
		return
	}
	c.bin = bin
	c.available = true
}

var gpuQuery = "index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,fan.speed,clocks.current.graphics,clocks.current.memory"

func (c *gpuCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	c.probe()
	if !c.available {
		return nil, nil
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, c.bin,
		"--query-gpu="+gpuQuery,
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, nil
	}
	now := time.Now()
	points := make([]wire.Point, 0, 16)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := splitCSVTrim(line)
		if len(fields) < 10 {
			continue
		}
		labels := map[string]string{"gpu": fields[0], "name": fields[1]}
		points = appendIfNum(points, now, metrics.GPUUsagePct, labels, fields[2])
		mem := atof(fields[3])
		memTotal := atof(fields[4])
		if mem > 0 {
			points = append(points, point(now, metrics.GPUMemUsed, labels, mem*1024*1024))
		}
		if memTotal > 0 {
			points = append(points, point(now, metrics.GPUMemTotal, labels, memTotal*1024*1024))
			if mem >= 0 {
				points = append(points, point(now, metrics.GPUMemUsedPct, labels, mem*100/memTotal))
			}
		}
		points = appendIfNum(points, now, metrics.GPUTempC, labels, fields[5])
		points = appendIfNum(points, now, metrics.GPUPowerWatts, labels, fields[6])
		points = appendIfNum(points, now, metrics.GPUFanPct, labels, fields[7])
		points = appendIfNum(points, now, metrics.GPUClockMHz, labels, fields[8])
		points = appendIfNum(points, now, metrics.GPUMemClockMHz, labels, fields[9])
	}
	return points, nil
}

func splitCSVTrim(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func atof(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "[Not Supported]" || s == "[N/A]" {
		return -1
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v
}

func appendIfNum(out []wire.Point, t time.Time, id metrics.ID, labels map[string]string, raw string) []wire.Point {
	v := atof(raw)
	if v < 0 {
		return out
	}
	return append(out, point(t, id, labels, v))
}
