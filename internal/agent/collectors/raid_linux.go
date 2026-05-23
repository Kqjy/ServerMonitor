package collectors

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type raidCollector struct{}

func init() { Register(&raidCollector{}) }

func (c *raidCollector) Name() string        { return "raid" }
func (c *raidCollector) Platforms() []string { return []string{"linux"} }

var raidStatusRe = regexp.MustCompile(`\[([U_]+)\]`)
var raidResyncRe = regexp.MustCompile(`(?:resync|recovery|reshape)\s*=\s*([0-9.]+)%`)

func (c *raidCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	f, err := os.Open("/proc/mdstat")
	if err != nil {
		return nil, nil
	}
	defer f.Close()

	now := time.Now()
	out := make([]wire.Point, 0, 8)
	scanner := bufio.NewScanner(f)
	var currentMD string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "md") {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				currentMD = strings.TrimSuffix(parts[0], ":")
			}
		}
		if currentMD == "" {
			continue
		}
		labels := map[string]string{"array": currentMD}
		if m := raidStatusRe.FindStringSubmatch(line); m != nil {
			degraded := 0.0
			if strings.Contains(m[1], "_") {
				degraded = 1
			}
			out = append(out, point(now, metrics.RaidDegraded, labels, degraded))
		}
		if m := raidResyncRe.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				out = append(out, point(now, metrics.RaidSyncPct, labels, v))
			}
		}
	}
	return out, nil
}
