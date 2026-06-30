package collectors

import (
	"context"
	"testing"
	"time"
)

func TestContainerConnectGated(t *testing.T) {
	c := &containerCollector{nextProbe: time.Now().Add(time.Hour)}
	if cli := c.connect(context.Background()); cli != nil {
		t.Fatal("expected nil while probe is gated; got a client (probe ran early)")
	}
}

func TestContainerConnectArmsBackoffOnFailure(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	c := &containerCollector{}
	if cli := c.connect(context.Background()); cli != nil {
		t.Fatal("expected nil connecting to an unreachable daemon")
	}
	if c.nextProbe.IsZero() || time.Until(c.nextProbe) <= 0 {
		t.Fatal("expected backoff to be armed after a failed probe")
	}
	if c.cli != nil {
		t.Fatal("expected no client cached after a failed probe")
	}
}

func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name       string
		prev, cur  cpuSample
		onlineCPUs int
		want       float64
	}{
		{"half of one core", cpuSample{100, 1000}, cpuSample{150, 1100}, 1, 50},
		{"all four cores", cpuSample{0, 1000}, cpuSample{400, 1400}, 4, 400},
		{"counter reset", cpuSample{500, 2000}, cpuSample{10, 2100}, 2, 0},
		{"system reset", cpuSample{100, 2000}, cpuSample{200, 1000}, 2, 0},
		{"no system delta", cpuSample{100, 1000}, cpuSample{200, 1000}, 2, 0},
		{"no cpu delta", cpuSample{100, 1000}, cpuSample{100, 1100}, 2, 0},
		{"zero online cpus treated as one", cpuSample{0, 1000}, cpuSample{50, 1100}, 0, 50},
	}
	for _, tc := range cases {
		if got := cpuPercent(tc.prev, tc.cur, tc.onlineCPUs); got != tc.want {
			t.Errorf("%s: cpuPercent(%v,%v,%d)=%v want %v", tc.name, tc.prev, tc.cur, tc.onlineCPUs, got, tc.want)
		}
	}
}
