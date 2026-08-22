package collectors

import (
	"context"
	"errors"
	"io/fs"
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

func TestContainerStatusUnknownBeforeProbe(t *testing.T) {
	c := &containerCollector{}
	if got := c.Status().State; got != dockerStateUnknown {
		t.Fatalf("state before any probe = %q, want %q", got, dockerStateUnknown)
	}
}

func TestContainerStatusAfterFailedProbe(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	c := &containerCollector{}
	c.connect(context.Background())
	if got := c.Status().State; got == dockerStateUnknown || got == dockerStateOK {
		t.Fatalf("state after a failed probe = %q, want a failure state", got)
	}
}

func TestClassifyDockerErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, dockerStateOK},
		{"wrapped permission", fs.ErrPermission, dockerStateNoPerm},
		{"unix permission text", errors.New("dial unix /var/run/docker.sock: connect: permission denied"), dockerStateNoPerm},
		{"windows permission text", errors.New("open //./pipe/docker_engine: Access is denied."), dockerStateNoPerm},
		{"wrapped not exist", fs.ErrNotExist, dockerStateAbsent},
		{"missing socket text", errors.New("dial unix /var/run/docker.sock: connect: no such file or directory"), dockerStateAbsent},
		{"refused", errors.New("dial tcp 127.0.0.1:1: connect: connection refused"), dockerStateUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := classifyDockerErr(tc.err)
			if got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
			if tc.want == dockerStateOK && msg != "" {
				t.Fatalf("message = %q, want empty", msg)
			}
			if len(msg) > 260 {
				t.Fatalf("message not truncated: %d chars", len(msg))
			}
		})
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
