package ipban

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func expectFailure(t *testing.T, got <-chan Failure, ip string, within time.Duration) {
	t.Helper()
	select {
	case f := <-got:
		if f.IP.String() != ip {
			t.Fatalf("failure ip = %s, want %s", f.IP, ip)
		}
	case <-time.After(within):
		t.Fatalf("no failure for %s within %s", ip, within)
	}
}

func waitSourceOK(t *testing.T, src *sshdSource) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if src.Status().State == sourceOK {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("source never became ready: %+v", src.Status())
}

func TestTailFileFollowsAppendsTruncationAndRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.log")
	if err := os.WriteFile(path, []byte("Aug 21 13:00:00 web01 sshd[1]: Failed password for root from 203.0.113.1 port 1 ssh2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan Failure, 16)
	src := newSSHDSource(slog.New(slog.DiscardHandler), func(f Failure) { got <- f }, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		src.tailFile(ctx, path, f)
	}()
	waitSourceOK(t, src)

	appendLine(t, path, "Aug 21 13:35:01 web01 sshd[1234]: Failed password for root from 203.0.113.5 port 1 ssh2")
	appendLine(t, path, "Aug 21 13:35:02 web01 systemd[1]: Started something unrelated.")
	expectFailure(t, got, "203.0.113.5", 3*time.Second)
	select {
	case f := <-got:
		t.Fatalf("unexpected extra failure %+v (pre-existing lines must be skipped)", f)
	case <-time.After(200 * time.Millisecond):
	}

	if err := os.WriteFile(path, []byte("Aug 21 13:35:01 web01 sshd[1234]: Failed password for root from 203.0.113.5 port 1 ssh2\nAug 21 13:36:00 web01 sshd-session[99]: Invalid user x from 203.0.113.6 port 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectFailure(t, got, "203.0.113.6", 6*time.Second)
	select {
	case f := <-got:
		t.Fatalf("truncation replayed an old failure: %+v", f)
	case <-time.After(200 * time.Millisecond):
	}

	if runtime.GOOS != "windows" {
		if err := os.Rename(path, path+".1"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("Aug 21 13:37:00 web01 sshd[7]: Failed password for invalid user admin from 203.0.113.7 port 3 ssh2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		expectFailure(t, got, "203.0.113.7", 6*time.Second)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("tailer did not stop")
	}
}

func TestJournalLineParsing(t *testing.T) {
	got := make(chan Failure, 4)
	src := newSSHDSource(slog.New(slog.DiscardHandler), func(f Failure) { got <- f }, time.Now)
	src.handleJournalLine([]byte(`{"MESSAGE":"Failed password for invalid user admin from 203.0.113.9 port 4242 ssh2","__REALTIME_TIMESTAMP":"1755780000000000","SYSLOG_IDENTIFIER":"sshd"}`))
	select {
	case f := <-got:
		if f.IP.String() != "203.0.113.9" || f.User != "admin" || f.Source != "sshd" {
			t.Fatalf("failure = %+v", f)
		}
		if f.Time.Unix() != 1755780000 {
			t.Fatalf("time = %v", f.Time)
		}
	default:
		t.Fatal("journal line not parsed")
	}
	src.handleJournalLine([]byte(`{"MESSAGE":[70,97],"__REALTIME_TIMESTAMP":"1"}`))
	src.handleJournalLine([]byte(`not json`))
	src.handleJournalLine([]byte(`{"MESSAGE":"Accepted publickey for deploy from 203.0.113.10 port 1 ssh2"}`))
	src.handleJournalLine([]byte(`{"MESSAGE":"Failed password for root from 203.0.113.11 port 1 ssh2","SYSLOG_IDENTIFIER":"cron"}`))
	select {
	case f := <-got:
		t.Fatalf("unexpected failure %+v", f)
	default:
	}
}

func TestJournalPermissionHints(t *testing.T) {
	for _, line := range []string{
		"Hint: You are currently not seeing messages from other users and the system.",
		"No journal files were opened due to insufficient permissions.",
		"Failed to open /var/log/journal: Permission denied",
	} {
		if !isJournalPermissionHint(line) {
			t.Errorf("%q should be a permission hint", line)
		}
	}
	if isJournalPermissionHint("-- No entries --") {
		t.Error("benign journalctl output flagged as a permission hint")
	}
}
