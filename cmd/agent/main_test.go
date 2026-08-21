package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"servermonitor/pkg/wire"
)

func TestPubkeyHolderSetIfEmpty(t *testing.T) {
	h := &pubkeyHolder{}
	if h.get() != "" {
		t.Fatalf("zero value should be empty, got %q", h.get())
	}
	if !h.setIfEmpty("abc") {
		t.Fatal("setIfEmpty on empty should return true")
	}
	if got := h.get(); got != "abc" {
		t.Fatalf("after set, want abc got %q", got)
	}
	if h.setIfEmpty("def") {
		t.Fatal("setIfEmpty on non-empty should return false")
	}
	if got := h.get(); got != "abc" {
		t.Fatalf("after rejected set, want abc still got %q", got)
	}
}

func TestPubkeyHolderConcurrentSetIfEmpty(t *testing.T) {
	h := &pubkeyHolder{}
	const N = 50
	var wg sync.WaitGroup
	wins := make([]bool, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wins[idx] = h.setIfEmpty("candidate-" + string(rune('A'+idx%26)))
		}(i)
	}
	wg.Wait()
	count := 0
	for _, w := range wins {
		if w {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one setIfEmpty to win, got %d", count)
	}
	if !strings.HasPrefix(h.get(), "candidate-") {
		t.Fatalf("expected a candidate value, got %q", h.get())
	}
}

func TestPersistConfigFieldUpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	original := `server_url    = "https://example"
token         = "tok"
server_pubkey = ""
interval_s    = 10
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persistServerPubkey(path, "abc123"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `server_pubkey = "abc123"`) {
		t.Fatalf("expected pubkey line in output, got:\n%s", got)
	}
	if strings.Count(string(got), "server_pubkey") != 1 {
		t.Fatalf("expected exactly one server_pubkey line, got:\n%s", got)
	}
}

func TestPersistConfigFieldAppendsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	original := `server_url = "https://example"
token      = "tok"
interval_s = 10
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persistServerPubkey(path, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `server_pubkey = "deadbeef"`) {
		t.Fatalf("expected appended pubkey line, got:\n%s", got)
	}
	if !strings.Contains(string(got), `interval_s = 10`) {
		t.Fatalf("expected existing fields preserved, got:\n%s", got)
	}
}

func TestPersistConfigFieldPreservesInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte("interval_s = 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persistInterval(path, 42); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "interval_s = 42") {
		t.Fatalf("expected interval=42, got:\n%s", got)
	}
}

func TestManagedBackupConfigSignatureIncludesCredentialChanges(t *testing.T) {
	base := &wire.ManagedBackupConfig{Version: 5, Repositories: []wire.ManagedBackupRepository{{
		ID: 1, Name: "direct", URL: "s3:s3.amazonaws.com/bucket/host-1", AccessKeyID: "key", SecretAccessKey: "secret-1",
	}}}
	first, err := managedBackupConfigSignature(base)
	if err != nil {
		t.Fatal(err)
	}
	changed := *base
	changed.Repositories = append([]wire.ManagedBackupRepository(nil), base.Repositories...)
	changed.Repositories[0].SecretAccessKey = "secret-2"
	second, err := managedBackupConfigSignature(&changed)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("credential-only change did not change the managed backup signature")
	}
}
