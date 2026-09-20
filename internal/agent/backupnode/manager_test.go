package backupnode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"servermonitor/pkg/restserver"
	"servermonitor/pkg/wgtunnel"
	"servermonitor/pkg/wire"
)

func nodeTarget(name, secret string, quota int64) wire.NodeTargetInfo {
	hash := sha256.Sum256([]byte(secret))
	return wire.NodeTargetInfo{Name: name, SecretHash: hex.EncodeToString(hash[:]), QuotaBytes: quota}
}

func TestLocalRegistryRejectsTargetUntilInitialUsageMeasured(t *testing.T) {
	target := nodeTarget("host1", "secret", 100)
	registry := newLocalRegistry([]wire.NodeTargetInfo{target})
	if _, _, _, ok, err := registry.ResolveTarget(context.Background(), "host1", "secret"); err != nil || ok {
		t.Fatalf("unmeasured target resolved: ok=%v err=%v", ok, err)
	}
	storeDir := t.TempDir()
	store, err := restserver.NewDiskStore(storeDir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	if err := store.Create(context.Background(), "host1", "data", strings.Repeat("a", 64), 90, strings.NewReader(strings.Repeat("x", 90))); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	bindings := loadRepositoryBindings(storeDir, []wire.NodeTargetInfo{target}, nil)
	if err := measureTargetUsage(context.Background(), store, registry, bindings, []wire.NodeTargetInfo{target}); err != nil {
		t.Fatalf("measureTargetUsage: %v", err)
	}
	id, quota, used, ok, err := registry.ResolveTarget(context.Background(), "host1", "secret")
	if err != nil || !ok || quota != 100 || used != 90 {
		t.Fatalf("measured target = id=%d quota=%d used=%d ok=%v err=%v", id, quota, used, ok, err)
	}
	if reserved, err := registry.ReserveUsage(context.Background(), id, 11); err != nil || reserved {
		t.Fatalf("over-quota reserve = %v err=%v", reserved, err)
	}
}

func TestLocalRegistryQuotaReservationIsAtomic(t *testing.T) {
	target := nodeTarget("host1", "secret", 10)
	registry := newLocalRegistry([]wire.NodeTargetInfo{target})
	registry.setUsed("host1", 0)
	id, _, _, ok, err := registry.ResolveTarget(context.Background(), "host1", "secret")
	if err != nil || !ok {
		t.Fatalf("resolve target: ok=%v err=%v", ok, err)
	}
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reserved, reserveErr := registry.ReserveUsage(context.Background(), id, 6)
			if reserveErr != nil {
				t.Errorf("reserve: %v", reserveErr)
			}
			results <- reserved
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for reserved := range results {
		if reserved {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful reservations = %d, want 1", succeeded)
	}
}

func TestPeerStatsPayload(t *testing.T) {
	knownKey, err := wgtunnel.KeyFromBytes(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("known key: %v", err)
	}
	zeroKey, err := wgtunnel.KeyFromBytes(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatalf("zero key: %v", err)
	}
	unknownKey, err := wgtunnel.KeyFromBytes(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatalf("unknown key: %v", err)
	}
	self, err := wgtunnel.KeyFromBytes(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatalf("self key: %v", err)
	}
	handshake := time.Unix(1_700_000_000, 0).UTC()
	known := map[wgtunnel.Key]netip.Addr{
		knownKey: netip.MustParseAddr("10.0.0.2"),
		zeroKey:  netip.MustParseAddr("10.0.0.3"),
	}
	stats := []wgtunnel.PeerStats{
		{PublicKey: knownKey, LastHandshake: handshake, RxBytes: 123, TxBytes: 456},
		{PublicKey: zeroKey},
		{PublicKey: unknownKey, LastHandshake: handshake, RxBytes: 999, TxBytes: 999},
	}
	payload := peerStatsPayload(stats, known, self)
	if len(payload) != 2 {
		t.Fatalf("payload length = %d, want 2", len(payload))
	}
	client := payload[0]
	if client.PublicKey != knownKey.String() || client.LastHandshakeUnix != handshake.Unix() || client.RxBytes != 123 || client.TxBytes != 456 {
		t.Fatalf("client payload = %+v", client)
	}
	selfStat := payload[1]
	if selfStat.PublicKey != self.String() || selfStat.LastHandshakeUnix != handshake.Unix() || selfStat.RxBytes != 123 || selfStat.TxBytes != 456 {
		t.Fatalf("self payload = %+v", selfStat)
	}
	if empty := peerStatsPayload(stats[1:], known, self); len(empty) != 0 {
		t.Fatalf("empty payload = %+v", empty)
	}
}

func TestRunReportsStartFailureAndRecovery(t *testing.T) {
	udp, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatalf("bind collision socket: %v", err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	storeDir := t.TempDir()
	payloads := make(chan wire.BackupNodeUsage, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/backup-node":
			_ = json.NewEncoder(w).Encode(wire.BackupNodeConfig{
				Enabled:  true,
				UDPPort:  port,
				StoreDir: storeDir,
			})
		case "/api/v1/agent/tunnel":
			_ = json.NewEncoder(w).Encode(wire.TunnelEnrollResponse{TunnelIP: "10.83.0.2"})
		case "/api/v1/agent/backup-node/usage":
			var payload wire.BackupNodeUsage
			if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
				http.Error(w, decodeErr.Error(), http.StatusBadRequest)
				return
			}
			payloads <- payload
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := New(server.URL, "token", false, t.TempDir(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()

	var failed wire.BackupNodeUsage
	select {
	case failed = <-payloads:
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for failed health report")
	}
	if failed.Running == nil || *failed.Running {
		t.Fatalf("failed running = %v, want false", failed.Running)
	}
	if failed.Error == "" {
		t.Fatal("failed error is empty")
	}

	if err := udp.Close(); err != nil {
		t.Fatalf("release collision socket: %v", err)
	}
	manager.reconcile(ctx)
	manager.reportUsage(ctx)

	var recovered wire.BackupNodeUsage
	select {
	case recovered = <-payloads:
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for recovered health report")
	}
	if recovered.Running == nil || !*recovered.Running {
		t.Fatalf("recovered running = %v, want true", recovered.Running)
	}
	if recovered.Error != "" {
		t.Fatalf("recovered error = %q, want empty", recovered.Error)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not stop")
	}
}

func TestRepositoryBindingsAdoptRepositoriesConfiguredBeforeTheLedgerExisted(t *testing.T) {
	storeDir := t.TempDir()
	target := nodeTarget("existing", "secret", 0)
	bindings := loadRepositoryBindings(storeDir, []wire.NodeTargetInfo{target}, nil)
	bindings.observe(target, 4096)
	if warnings := bindings.pendingWarnings(); len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if _, err := os.Stat(filepath.Join(storeDir, repositoryBindingsFile)); err != nil {
		t.Fatalf("bindings ledger not written: %v", err)
	}
}

func TestRepositoryBindingsWarnWhenANewRepositoryLandsOnLeftoverData(t *testing.T) {
	storeDir := t.TempDir()
	original := nodeTarget("recycled", "first-secret", 0)
	loadRepositoryBindings(storeDir, []wire.NodeTargetInfo{original}, nil)

	recreated := nodeTarget("recycled", "second-secret", 0)
	bindings := loadRepositoryBindings(storeDir, []wire.NodeTargetInfo{recreated}, nil)
	bindings.observe(recreated, 4096)
	warnings := bindings.pendingWarnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "recycled") || !strings.Contains(warnings[0], "4096") {
		t.Fatalf("warnings = %v", warnings)
	}
	if got := nodeStatusError("start failed", warnings); !strings.HasPrefix(got, "start failed; ") {
		t.Fatalf("nodeStatusError = %q", got)
	}
	bindings.save()

	reloaded := loadRepositoryBindings(storeDir, []wire.NodeTargetInfo{recreated}, nil)
	reloaded.observe(recreated, 4096)
	if warnings := reloaded.pendingWarnings(); len(warnings) != 0 {
		t.Fatalf("warning repeated for an already-bound repository: %v", warnings)
	}
	reloaded.observe(nodeTarget("recycled", "third-secret", 0), 4096)
	if warnings := reloaded.pendingWarnings(); len(warnings) != 1 {
		t.Fatalf("rebinding the same name to another repository was not reported: %v", warnings)
	}
	reloaded.forgetUnconfigured(nil)
	if warnings := reloaded.pendingWarnings(); len(warnings) != 0 {
		t.Fatalf("warning survived the repository leaving the configuration: %v", warnings)
	}
}

func TestMeasureTargetUsageServesARewarnedRepository(t *testing.T) {
	storeDir := t.TempDir()
	store, err := restserver.NewDiskStore(storeDir)
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	if err := store.Create(context.Background(), "recycled", "data", strings.Repeat("a", 64), 90, strings.NewReader(strings.Repeat("x", 90))); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	target := nodeTarget("recycled", "secret", 0)
	bindings := loadRepositoryBindings(storeDir, nil, nil)
	registry := newLocalRegistry([]wire.NodeTargetInfo{target})
	if err := measureTargetUsage(context.Background(), store, registry, bindings, []wire.NodeTargetInfo{target}); err != nil {
		t.Fatalf("measureTargetUsage: %v", err)
	}
	if _, _, used, ok, err := registry.ResolveTarget(context.Background(), "recycled", "secret"); err != nil || !ok || used != 90 {
		t.Fatalf("warned repository = used=%d ok=%v err=%v, want served with used=90", used, ok, err)
	}
	if warnings := bindings.pendingWarnings(); len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
}

func TestReplaceTargetsRemeasuresARecreatedRepository(t *testing.T) {
	original := nodeTarget("host1", "first-secret", 0)
	registry := newLocalRegistry([]wire.NodeTargetInfo{original})
	registry.setUsed("host1", 90)
	if added := registry.replaceTargets([]wire.NodeTargetInfo{original}); len(added) != 0 {
		t.Fatalf("unchanged target re-measured: %v", added)
	}
	recreated := nodeTarget("host1", "second-secret", 0)
	added := registry.replaceTargets([]wire.NodeTargetInfo{recreated})
	if len(added) != 1 || added[0].Name != "host1" {
		t.Fatalf("recreated repository was not re-measured: %v", added)
	}
	if again := registry.replaceTargets([]wire.NodeTargetInfo{recreated}); len(again) != 0 {
		t.Fatalf("recreated repository re-measured forever: %v", again)
	}
}

func TestRepositoryBindingsConcurrentSavesConverge(t *testing.T) {
	storeDir := t.TempDir()
	bindings := loadRepositoryBindings(storeDir, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bindings.observe(nodeTarget(fmt.Sprintf("repo%d", i), "secret", 0), 0)
			bindings.save()
		}(i)
	}
	wg.Wait()
	raw, err := os.ReadFile(filepath.Join(storeDir, repositoryBindingsFile))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var bound map[string]string
	if err := json.Unmarshal(raw, &bound); err != nil {
		t.Fatalf("ledger is not valid json after concurrent saves: %v (%s)", err, raw)
	}
	leftovers, err := filepath.Glob(filepath.Join(storeDir, repositoryBindingsFile+".*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp ledger files left behind: %v", leftovers)
	}
}
