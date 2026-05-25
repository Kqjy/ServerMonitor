package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"servermonitor/pkg/version"
)

type target struct {
	goos   string
	goarch string
	out    string
}

var targets = []target{
	{"linux", "amd64", "sm-agent-linux-amd64"},
	{"linux", "arm64", "sm-agent-linux-arm64"},
	{"darwin", "amd64", "sm-agent-darwin-amd64"},
	{"darwin", "arm64", "sm-agent-darwin-arm64"},
	{"windows", "amd64", "sm-agent-windows-amd64.exe"},
}

func main() {
	outDir := "internal/server/agentdist/binaries"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup
	errs := make([]error, len(targets))
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t target) {
			defer wg.Done()
			errs[i] = buildAndVerify(t, outDir)
		}(i, t)
	}
	wg.Wait()

	fail := false
	for i, t := range targets {
		if errs[i] != nil {
			fmt.Fprintf(os.Stderr, "FAIL  %s/%s: %v\n", t.goos, t.goarch, errs[i])
			fail = true
			continue
		}
		fmt.Printf("built %s (%s)\n", filepath.Join(outDir, t.out), version.Version)
	}
	if fail {
		os.Exit(1)
	}
}

func buildAndVerify(t target, outDir string) error {
	outPath := filepath.Join(outDir, t.out)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", outPath, "./cmd/agent")
	cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH="+t.goarch, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build: %w: %s", err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("read built binary: %w", err)
	}
	if !bytes.Contains(data, []byte(version.Version)) {
		return fmt.Errorf("built binary missing version string %q", version.Version)
	}
	return nil
}
