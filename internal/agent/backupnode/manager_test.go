package backupnode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"servermonitor/pkg/restserver"
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
	store, err := restserver.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	if err := store.Create(context.Background(), "host1", "data", strings.Repeat("a", 64), 90, strings.NewReader(strings.Repeat("x", 90))); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	if err := measureTargetUsage(context.Background(), store, registry, []wire.NodeTargetInfo{target}); err != nil {
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
