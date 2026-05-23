package collectors

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type wifiCollector struct{}

func init() { Register(&wifiCollector{}) }

func (c *wifiCollector) Name() string        { return "wifi" }
func (c *wifiCollector) Platforms() []string { return []string{"linux"} }

func (c *wifiCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	f, err := os.Open("/proc/net/wireless")
	if err != nil {
		return nil, nil
	}
	defer f.Close()

	now := time.Now()
	out := make([]wire.Point, 0, 4)
	scanner := bufio.NewScanner(f)
	headers := 0
	for scanner.Scan() {
		line := scanner.Text()
		if headers < 2 {
			headers++
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		iface := strings.TrimSuffix(fields[0], ":")
		if iface == "" {
			continue
		}
		labels := map[string]string{"iface": iface}
		if level, err := strconv.ParseFloat(strings.TrimSuffix(fields[3], "."), 64); err == nil {
			out = append(out, point(now, metrics.WifiSignal, labels, level))
		}
	}
	return out, nil
}
