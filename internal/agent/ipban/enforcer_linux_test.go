//go:build linux

package ipban

import (
	"context"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"
)

func requireEnv(t *testing.T, name string) {
	t.Helper()
	if os.Getenv(name) != "1" {
		t.Skipf("set %s=1 to run this integration test", name)
	}
}

func TestNFTEnforcerRoundTrip(t *testing.T) {
	requireEnv(t, "SM_IPBAN_NFT_TEST")
	e, err := NewEnforcer()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() {
		_ = e.Close()
		_ = Teardown()
	})
	if err := e.Setup(); err != nil {
		t.Fatalf("second setup must be idempotent: %v", err)
	}
	ip4 := netip.MustParseAddr("203.0.113.55")
	ip6 := netip.MustParseAddr("2001:db8::55")
	if err := e.AddLocal(ip4, 90*time.Second); err != nil {
		t.Fatalf("add local v4: %v", err)
	}
	if err := e.AddLocal(ip6, 90*time.Second); err != nil {
		t.Fatalf("add local v6: %v", err)
	}
	if err := e.ReplaceFleet([]FleetEntry{
		{IP: netip.MustParseAddr("198.51.100.1"), Timeout: 120 * time.Second},
		{IP: netip.MustParseAddr("2001:db8::1"), Timeout: 120 * time.Second},
	}); err != nil {
		t.Fatalf("replace fleet: %v", err)
	}
	local, fleet, err := e.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(local) != 2 || len(fleet) != 2 {
		t.Fatalf("active local=%v fleet=%v", local, fleet)
	}
	for _, entry := range append(local, fleet...) {
		if entry.Expires <= 0 || entry.Expires > 120*time.Second {
			t.Fatalf("expiry out of range: %+v", entry)
		}
	}
	if err := e.AddLocal(ip4, 300*time.Second); err != nil {
		t.Fatalf("re-add local v4: %v", err)
	}
	local, _, err = e.Active()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range local {
		if entry.IP == ip4 {
			found = true
			if entry.Expires <= 90*time.Second {
				t.Fatalf("re-add should extend the timeout, got %s", entry.Expires)
			}
		}
	}
	if !found {
		t.Fatal("v4 entry missing after re-add")
	}
	if err := e.RemoveLocal(ip4); err != nil {
		t.Fatalf("remove local: %v", err)
	}
	if err := e.RemoveLocal(ip4); err != nil {
		t.Fatalf("removing a missing element must be a no-op: %v", err)
	}
	local, _, err = e.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].IP != ip6 {
		t.Fatalf("local after remove = %v", local)
	}
	if err := e.ReplaceFleet(nil); err != nil {
		t.Fatalf("clear fleet: %v", err)
	}
	if err := e.FlushLocal(); err != nil {
		t.Fatalf("flush local: %v", err)
	}
	local, fleet, err = e.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 0 || len(fleet) != 0 {
		t.Fatalf("sets should be empty: local=%v fleet=%v", local, fleet)
	}
	snaps, err := Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snaps) != 4 {
		t.Fatalf("snapshot sets = %d", len(snaps))
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Teardown(); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if err := Teardown(); err != nil {
		t.Fatalf("second teardown must be a no-op: %v", err)
	}
	snaps, err = Snapshot()
	if err != nil || snaps != nil {
		t.Fatalf("snapshot after teardown = %v, %v", snaps, err)
	}
}

func TestJournalSourceReceivesLoggerLines(t *testing.T) {
	requireEnv(t, "SM_IPBAN_JOURNAL_TEST")
	bin := trustedBinary(journalctlCandidates)
	if bin == "" {
		t.Skip("journalctl not present")
	}
	got := make(chan Failure, 16)
	src := newSSHDSource(slog.New(slog.NewTextHandler(os.Stderr, nil)), func(f Failure) { got <- f }, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan [2]string, 1)
	go func() {
		state, msg := src.runJournal(ctx, bin)
		result <- [2]string{state, msg}
	}()
	waitSourceOK(t, src)
	time.Sleep(time.Second)
	if out, err := exec.Command("logger", "-t", "sshd", "Failed password for invalid user probe from 203.0.113.99 port 4444 ssh2").CombinedOutput(); err != nil {
		t.Fatalf("logger: %v %s", err, out)
	}
	select {
	case f := <-got:
		if f.IP.String() != "203.0.113.99" || f.User != "probe" {
			t.Fatalf("failure = %+v", f)
		}
	case <-result:
		t.Fatalf("journal source exited early: %+v", src.Status())
	case <-time.After(10 * time.Second):
		t.Fatalf("no failure received via journald; status %+v", src.Status())
	}
	cancel()
	select {
	case r := <-result:
		if r[0] != sourceOK {
			t.Fatalf("unexpected exit state %v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("journal source did not stop")
	}
}
