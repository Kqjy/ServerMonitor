package api

import (
	"bytes"
	"testing"
	"time"

	"servermonitor/pkg/wgtunnel"
	"servermonitor/pkg/wire"
)

func TestMergePeerStats(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	serverFresh := now.Add(-time.Minute)
	nodeFresh := now.Add(-30 * time.Second)
	serverNewest := now.Add(-10 * time.Second)
	stale := now.Add(-4 * time.Minute)
	tests := []struct {
		name          string
		server        *wgtunnel.PeerStats
		node          []nodePeerStat
		wantLast      time.Time
		wantRx        int64
		wantTx        int64
		wantConnected bool
		wantVia       string
	}{
		{
			name:          "server only",
			server:        &wgtunnel.PeerStats{LastHandshake: serverFresh, RxBytes: 10, TxBytes: 20},
			wantLast:      serverFresh,
			wantRx:        10,
			wantTx:        20,
			wantConnected: true,
		},
		{
			name:          "node only",
			node:          []nodePeerStat{{LastHandshake: nodeFresh, RxBytes: 30, TxBytes: 40, NodeHostname: "node-a"}},
			wantLast:      nodeFresh,
			wantRx:        30,
			wantTx:        40,
			wantConnected: true,
			wantVia:       "node-a",
		},
		{
			name:          "node newer",
			server:        &wgtunnel.PeerStats{LastHandshake: serverFresh, RxBytes: 10, TxBytes: 20},
			node:          []nodePeerStat{{LastHandshake: nodeFresh, RxBytes: 30, TxBytes: 40, NodeHostname: "node-a"}},
			wantLast:      nodeFresh,
			wantRx:        40,
			wantTx:        60,
			wantConnected: true,
			wantVia:       "node-a",
		},
		{
			name:          "server newer",
			server:        &wgtunnel.PeerStats{LastHandshake: serverNewest, RxBytes: 10, TxBytes: 20},
			node:          []nodePeerStat{{LastHandshake: nodeFresh, RxBytes: 30, TxBytes: 40, NodeHostname: "node-a"}},
			wantLast:      serverNewest,
			wantRx:        40,
			wantTx:        60,
			wantConnected: true,
		},
		{
			name: "two nodes",
			node: []nodePeerStat{
				{LastHandshake: serverFresh, RxBytes: 10, TxBytes: 20, NodeHostname: "node-a"},
				{LastHandshake: nodeFresh, RxBytes: 30, TxBytes: 40, NodeHostname: "node-b"},
			},
			wantLast:      nodeFresh,
			wantRx:        40,
			wantTx:        60,
			wantConnected: true,
			wantVia:       "node-b",
		},
		{
			name:          "stale node",
			node:          []nodePeerStat{{LastHandshake: stale, RxBytes: 30, TxBytes: 40, NodeHostname: "node-a"}},
			wantLast:      stale,
			wantRx:        30,
			wantTx:        40,
			wantConnected: false,
			wantVia:       "node-a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			last, rx, tx, connected, via := mergePeerStats(tt.server, tt.node, now)
			if last == nil || !last.Equal(tt.wantLast) {
				t.Fatalf("last = %v, want %v", last, tt.wantLast)
			}
			if rx != tt.wantRx || tx != tt.wantTx || connected != tt.wantConnected || via != tt.wantVia {
				t.Fatalf("result = rx %d tx %d connected %v via %q", rx, tx, connected, via)
			}
		})
	}
}

func TestNodePeerStatsCacheStore(t *testing.T) {
	raw := bytes.Repeat([]byte{5}, 32)
	key, err := wgtunnel.KeyFromBytes(raw)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	cache := NewNodePeerStatsCache()
	cache.Store(1, "node-a", []wire.NodePeerStat{
		{PublicKey: key.String(), LastHandshakeUnix: 1_700_000_000, RxBytes: 10, TxBytes: 20},
		{PublicKey: "malformed", RxBytes: 999},
	})
	cache.Store(2, "node-b", []wire.NodePeerStat{
		{PublicKey: key.String(), LastHandshakeUnix: 1_700_000_010, RxBytes: 30, TxBytes: 40},
	})
	stats := cache.Get(key)
	if len(stats) != 2 {
		t.Fatalf("stats length = %d, want 2", len(stats))
	}
	byHostname := map[string]nodePeerStat{}
	for _, stat := range stats {
		byHostname[stat.NodeHostname] = stat
	}
	if stat := byHostname["node-a"]; !stat.LastHandshake.Equal(time.Unix(1_700_000_000, 0).UTC()) || stat.RxBytes != 10 || stat.TxBytes != 20 {
		t.Fatalf("node-a stat = %+v", stat)
	}
	if stat := byHostname["node-b"]; !stat.LastHandshake.Equal(time.Unix(1_700_000_010, 0).UTC()) || stat.RxBytes != 30 || stat.TxBytes != 40 {
		t.Fatalf("node-b stat = %+v", stat)
	}
	if len(cache.entries) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(cache.entries))
	}
	cache.Remove(key)
	if stats := cache.Get(key); len(stats) != 0 {
		t.Fatalf("stats after remove = %+v", stats)
	}
}
