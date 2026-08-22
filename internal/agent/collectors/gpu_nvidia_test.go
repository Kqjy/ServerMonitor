package collectors

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestGPUStatusUnknownBeforeProbe(t *testing.T) {
	c := &gpuCollector{}
	if got := c.Status().State; got != gpuStateUnknown {
		t.Fatalf("state before any probe = %q, want %q", got, gpuStateUnknown)
	}
}

func TestGPUProbeMarksBinaryMissing(t *testing.T) {
	c := &gpuCollector{}
	saved := nvidiaSMICandidates
	nvidiaSMICandidates = map[string][]string{}
	defer func() { nvidiaSMICandidates = saved }()
	c.probe()
	if c.available {
		t.Fatal("expected the collector to stay unavailable with no candidate binary")
	}
	if got := c.Status().State; got != gpuStateBinary {
		t.Fatalf("state = %q, want %q", got, gpuStateBinary)
	}
}

func TestGPUErrMessagePrefersStderr(t *testing.T) {
	err := &exec.ExitError{Stderr: []byte("NVIDIA-SMI has failed because it couldn't communicate with the driver\nsecond line\n")}
	got := gpuErrMessage(err)
	if !strings.HasPrefix(got, "NVIDIA-SMI has failed") {
		t.Fatalf("message = %q, want the stderr text", got)
	}
	if strings.Contains(got, "second line") {
		t.Fatalf("message = %q, want only the first stderr line", got)
	}
}

func TestGPUErrMessageTruncates(t *testing.T) {
	got := gpuErrMessage(errors.New(strings.Repeat("x", 500)))
	if len([]rune(got)) != 200 {
		t.Fatalf("message length = %d, want 200", len([]rune(got)))
	}
}
