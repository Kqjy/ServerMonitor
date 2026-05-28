package transport

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteHealthNoCorruptionUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	healthPath := filepath.Join(dir, "health.json")

	c := &Client{logger: slog.New(slog.DiscardHandler)}
	c.SetHealthPath(healthPath)
	c.SetCurrentInterval(10)

	const goroutines = 32
	const writesPerG = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < writesPerG; j++ {
				c.writeHealth()
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(healthPath)
	if err != nil {
		t.Fatalf("read final health.json: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("health.json corrupted after concurrent writes: %v; raw=%q", err, data)
	}
	if _, ok := rec["last_push_at"]; !ok {
		t.Fatalf("missing last_push_at: %v", rec)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "health.json" {
			continue
		}
		t.Errorf("leftover tmp artifact in dir: %s", e.Name())
	}
}
