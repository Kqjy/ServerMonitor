package collectors

import (
	"context"
	"testing"

	"servermonitor/pkg/metrics"
)

func TestSelfCollector(t *testing.T) {
	points, err := (&selfCollector{}).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	values := indexPoints(points)
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2: %#v", len(points), points)
	}
	if _, ok := values[metrics.AgentCPUPct]; !ok {
		t.Fatalf("agent CPU point missing: %#v", points)
	}
	if values[metrics.AgentRSSBytes] <= 0 {
		t.Fatalf("agent RSS = %v, want > 0", values[metrics.AgentRSSBytes])
	}
}
