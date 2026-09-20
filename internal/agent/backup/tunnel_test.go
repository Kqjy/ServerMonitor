package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"servermonitor/pkg/wgtunnel"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func tunnelTestConfig(t *testing.T, dir string, settings *TunnelSettings) Config {
	t.Helper()
	return Config{
		StatusPath: filepath.Join(dir, "backup-status.json"),
		ResticPath: "restic",
		Paths:      []string{"/etc"},
		PruneMode:  "external",
		Retention:  Retention{Daily: 7},
		Repos: []Repo{
			{Name: "viatunnel", TunnelName: "viatunnel", PasswordFile: filepath.Join(dir, "backup.key")},
			{Name: "direct", URL: "s3:s3.example.com/bucket/repo", PasswordFile: filepath.Join(dir, "backup.key")},
		},
		Tunnel: settings,
	}
}

func TestPrepareTunnelEndToEnd(t *testing.T) {
	dir := t.TempDir()

	serverKey, err := wgtunnel.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := wgtunnel.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(udp.LocalAddr().(*net.UDPAddr).Port)
	udp.Close()

	serverIP := netip.MustParseAddr("10.99.0.1")
	clientIP := netip.MustParseAddr("10.99.0.2")
	server, err := wgtunnel.Start(wgtunnel.DeviceConfig{
		PrivateKey: serverKey,
		Address:    serverIP,
		ListenPort: port,
	}, []wgtunnel.Peer{{
		PublicKey:  clientKey.Public(),
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(clientIP, 32)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ln, err := server.Listen(8443)
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "repo=%s", r.URL.Path)
	}))

	keyPath := filepath.Join(dir, "tunnel.key")
	if err := os.WriteFile(keyPath, []byte(clientKey.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := tunnelTestConfig(t, dir, &TunnelSettings{
		PrivateKeyFile:  keyPath,
		ServerPublicKey: serverKey.Public().String(),
		Endpoint:        fmt.Sprintf("127.0.0.1:%d", port),
		LocalIP:         clientIP.String(),
		ServerIP:        serverIP.String(),
		RestPort:        8443,
	})

	prepared, session := PrepareTunnel(cfg, Options{Logger: testLogger()})
	if session == nil {
		t.Fatal("expected a tunnel session")
	}
	defer session.Close()

	if prepared.Repos[1].URL != "s3:s3.example.com/bucket/repo" {
		t.Fatalf("non-tunnel repo URL changed: %s", prepared.Repos[1].URL)
	}
	wantPrefix := "rest:http://"
	got := prepared.Repos[0].URL
	if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("tunnel repo URL not rewritten: %s", got)
	}

	httpURL := got[len("rest:"):]
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(httpURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "repo=/viatunnel" {
		t.Fatalf("unexpected response through tunnel: %q", body)
	}
}

func TestPrepareTunnelNodeDestination(t *testing.T) {
	dir := t.TempDir()

	nodeKey, err := wgtunnel.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := wgtunnel.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(udp.LocalAddr().(*net.UDPAddr).Port)
	udp.Close()

	nodeIP := netip.MustParseAddr("10.99.1.7")
	clientIP := netip.MustParseAddr("10.99.1.9")
	node, err := wgtunnel.Start(wgtunnel.DeviceConfig{
		PrivateKey: nodeKey,
		Address:    nodeIP,
		ListenPort: port,
	}, []wgtunnel.Peer{{
		PublicKey:  clientKey.Public(),
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(clientIP, 32)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	ln, err := node.Listen(8443)
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "node-repo=%s", r.URL.Path)
	}))

	keyPath := filepath.Join(dir, "tunnel.key")
	if err := os.WriteFile(keyPath, []byte(clientKey.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		StatusPath: filepath.Join(dir, "backup-status.json"),
		ResticPath: "restic",
		Paths:      []string{"/etc"},
		PruneMode:  "external",
		Retention:  Retention{Daily: 7},
		Repos: []Repo{
			{Name: "onnode", TunnelName: "onnode", TunnelNode: "nas-01", PasswordFile: filepath.Join(dir, "backup.key")},
		},
		Tunnel: &TunnelSettings{
			PrivateKeyFile: keyPath,
			LocalIP:        clientIP.String(),
			Nodes: []TunnelNodeSettings{{
				Host:      "nas-01",
				PublicKey: nodeKey.Public().String(),
				Endpoint:  fmt.Sprintf("127.0.0.1:%d", port),
				IP:        nodeIP.String(),
				RestPort:  8443,
			}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("node-only tunnel config rejected: %v", err)
	}

	prepared, session := PrepareTunnel(cfg, Options{Logger: testLogger()})
	if session == nil {
		t.Fatal("expected a tunnel session")
	}
	defer session.Close()

	got := prepared.Repos[0].URL
	if !strings.HasPrefix(got, "rest:http://") {
		t.Fatalf("node repo URL not rewritten: %s", got)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(strings.TrimPrefix(got, "rest:"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "node-repo=/onnode" {
		t.Fatalf("unexpected response through node tunnel: %q", body)
	}
}

func TestPrepareTunnelFailureIsolatesRepos(t *testing.T) {
	dir := t.TempDir()
	cfg := tunnelTestConfig(t, dir, &TunnelSettings{
		PrivateKeyFile:  filepath.Join(dir, "missing.key"),
		ServerPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		Endpoint:        "127.0.0.1:51999",
		LocalIP:         "10.99.0.2",
		ServerIP:        "10.99.0.1",
		RestPort:        8443,
	})

	prepared, session := PrepareTunnel(cfg, Options{Logger: testLogger()})
	if session != nil {
		session.Close()
		t.Fatal("expected no session on key failure")
	}
	if prepared.Repos[0].tunnelErr == nil {
		t.Fatal("tunnel repo should carry the tunnel error")
	}
	if prepared.Repos[1].tunnelErr != nil {
		t.Fatal("non-tunnel repo must not carry a tunnel error")
	}

	_, err := resticCommandAction(context.Background(), prepared, prepared.Repos[0], dir, []string{"snapshots"}, "snapshots", Options{Logger: testLogger(), Runner: failRunner{t}})
	if err == nil {
		t.Fatal("expected tunnel repo command to fail fast")
	}
}

type failRunner struct{ t *testing.T }

func (f failRunner) Run(ctx context.Context, cmd Command) (CommandResult, error) {
	f.t.Fatal("restic must not run for a repo with a tunnel error")
	return CommandResult{}, nil
}

func TestTunnelRepoValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := tunnelTestConfig(t, dir, nil)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("tunnel repo without [tunnel] section must fail validation")
	}

	cfg = tunnelTestConfig(t, dir, &TunnelSettings{
		PrivateKeyFile:  filepath.Join(dir, "tunnel.key"),
		ServerPublicKey: "pub",
		Endpoint:        "example.com:51820",
		LocalIP:         "10.83.0.2",
		ServerIP:        "10.83.0.1",
		RestPort:        8443,
	})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid tunnel config rejected: %v", err)
	}

	both := cfg
	both.Repos = append([]Repo(nil), cfg.Repos...)
	both.Repos[0].URL = "rest:https://example/repo"
	if err := both.Validate(); err == nil {
		t.Fatal("repo with both url and tunnel_name must fail validation")
	}
}

func TestWriteTunnelSettingsMergesToml(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.toml")
	original := `status_path = "` + filepath.ToSlash(filepath.Join(dir, "status.json")) + `"
paths = ["/etc"]
prune_mode = "external"

[[repo]]
name = "viatunnel"
tunnel_name = "viatunnel"
password_file = "` + filepath.ToSlash(filepath.Join(dir, "backup.key")) + `"
`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := TunnelSettings{
		PrivateKeyFile:  filepath.Join(dir, "tunnel.key"),
		ServerPublicKey: "serverpub",
		Endpoint:        "monitor.example.com:51820",
		LocalIP:         "10.83.0.9",
		ServerIP:        "10.83.0.1",
		RestPort:        8443,
	}
	if err := writeTunnelSettings(configPath, settings); err != nil {
		t.Fatal(err)
	}
	if err := writeTunnelSettings(configPath, settings); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tunnel == nil {
		t.Fatal("tunnel section missing after merge")
	}
	if cfg.Tunnel.Endpoint != "monitor.example.com:51820" || cfg.Tunnel.RestPort != 8443 {
		t.Fatalf("tunnel settings wrong after merge: %+v", cfg.Tunnel)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].TunnelName != "viatunnel" {
		t.Fatalf("repo lost after merge: %+v", cfg.Repos)
	}
	if cfg.PruneMode != "external" {
		t.Fatalf("prune mode lost after merge: %s", cfg.PruneMode)
	}
}

type failingReader struct {
	prefix string
	err    error
	sent   bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, r.prefix)
		return n, nil
	}
	return 0, r.err
}

func TestProxyCopyLogsFailuresWithDirectionAndBytes(t *testing.T) {
	var logged strings.Builder
	session := &TunnelSession{logger: slog.New(slog.NewTextHandler(&logged, nil))}
	transfer := newTunnelTransfer()
	done := make(chan struct{}, 2)

	var sent atomic.Int64
	session.proxyCopy(io.Discard, &failingReader{prefix: "0123456789", err: errors.New("connection reset by peer")}, "10.83.0.2:8000", "upload", &sent, transfer, done)
	<-done
	if sent.Load() != 10 {
		t.Fatalf("counted bytes = %d, want 10", sent.Load())
	}
	line := logged.String()
	for _, want := range []string{"backup tunnel copy failed", "target=10.83.0.2:8000", "direction=upload", "bytes=10", "connection reset by peer"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %q missing %q", line, want)
		}
	}

	logged.Reset()
	var received atomic.Int64
	session.proxyCopy(io.Discard, &failingReader{err: net.ErrClosed}, "10.83.0.2:8000", "download", &received, transfer, done)
	<-done
	if logged.Len() != 0 {
		t.Fatalf("shutdown close logged: %q", logged.String())
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestStalledTransferIsReportedWithoutCancelling(t *testing.T) {
	logged := &syncBuffer{}
	session := &TunnelSession{logger: slog.New(slog.NewTextHandler(logged, nil))}
	transfer := newTunnelTransfer()
	transfer.sent.Store(4096)
	stopped := make(chan struct{})
	go session.watchStalledTransfer("10.83.0.2:8000", transfer, 5*time.Millisecond, stopped)

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logged.String(), "backup tunnel connection made no progress") {
		if time.Now().After(deadline) {
			close(stopped)
			t.Fatalf("stall was never reported: %q", logged.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stopped)
	if line := logged.String(); !strings.Contains(line, "sent_bytes=4096") || !strings.Contains(line, "target=10.83.0.2:8000") {
		t.Fatalf("stall log = %q", line)
	}
	if transfer.idleFor(time.Now()) <= 0 {
		t.Fatal("idle duration did not advance")
	}
}

func TestTransferProgressResetsIdleClock(t *testing.T) {
	transfer := newTunnelTransfer()
	if idle := transfer.idleFor(time.Now().Add(5 * time.Minute)); idle < 5*time.Minute {
		t.Fatalf("idle = %s, want at least 5m", idle)
	}
	var count atomic.Int64
	reader := &tunnelProgressReader{src: strings.NewReader("0123456789"), count: &count, transfer: transfer}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if count.Load() != 10 {
		t.Fatalf("counted bytes = %d, want 10", count.Load())
	}
	if idle := transfer.idleFor(time.Now()); idle > time.Second {
		t.Fatalf("idle after progress = %s", idle)
	}
}
