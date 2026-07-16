package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"servermonitor/internal/server/storage"
)

type fakeBackupNodeStore struct {
	node storage.BackupNode
	rows []storage.BackupNode
}

func (s *fakeBackupNodeStore) Get(_ context.Context, hostID int64) (storage.BackupNode, error) {
	if s.node.HostID != hostID {
		return storage.BackupNode{}, storage.ErrNotFound
	}
	return s.node, nil
}

func (s *fakeBackupNodeStore) SetTargetUsage(context.Context, int64, string, int64) error {
	return nil
}

func (s *fakeBackupNodeStore) List(context.Context) ([]storage.BackupNode, error) {
	return s.rows, nil
}

func TestTunnelEndpointForRequest(t *testing.T) {
	cases := []struct {
		name string
		info BackupTunnelInfo
		host string
		want string
	}{
		{"explicit with port", BackupTunnelInfo{Endpoint: "wg.example.com:52000", ListenPort: 51820}, "monitor.example.com", "wg.example.com:52000"},
		{"explicit host only", BackupTunnelInfo{Endpoint: "wg.example.com", ListenPort: 51820}, "monitor.example.com", "wg.example.com:51820"},
		{"derived from host header", BackupTunnelInfo{ListenPort: 51820}, "monitor.example.com", "monitor.example.com:51820"},
		{"derived strips port", BackupTunnelInfo{ListenPort: 51820}, "monitor.example.com:8443", "monitor.example.com:51820"},
		{"ipv6 host header", BackupTunnelInfo{ListenPort: 51820}, "[2001:db8::1]:8443", "[2001:db8::1]:51820"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/agent/tunnel", nil)
			r.Host = tc.host
			if got := tunnelEndpointForRequest(tc.info, r); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentBackupNodeUsageStoresHealth(t *testing.T) {
	store := &fakeBackupNodeStore{node: storage.BackupNode{HostID: 42, Hostname: "node-42"}}
	health := NewNodeHealthCache()
	handler := agentBackupNodeUsageHandler(store, NewNodePeerStatsCache(), health)
	body := bytes.NewBufferString(`{"running":false,"error":"bind failed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/backup-node/usage", body)
	req = req.WithContext(context.WithValue(req.Context(), ctxHostID, int64(42)))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, ok := health.Get(42)
	if !ok {
		t.Fatal("health was not stored")
	}
	if got.Running || got.Error != "bind failed" || got.At.IsZero() {
		t.Fatalf("health = %+v", got)
	}
}

func TestListBackupNodesExposesFreshHealthAndOmitsStaleHealth(t *testing.T) {
	store := &fakeBackupNodeStore{rows: []storage.BackupNode{
		{HostID: 1, Hostname: "failed", CreatedAt: time.Unix(1, 0).UTC()},
		{HostID: 2, Hostname: "running", CreatedAt: time.Unix(2, 0).UTC()},
		{HostID: 3, Hostname: "stale", CreatedAt: time.Unix(3, 0).UTC()},
		{HostID: 4, Hostname: "absent", CreatedAt: time.Unix(4, 0).UTC()},
	}}
	health := NewNodeHealthCache()
	health.Store(1, true, "usage measurement failed")
	health.Store(2, true, "")
	health.mu.Lock()
	health.entries[3] = nodeHealth{Running: false, Error: "old error", At: time.Now().Add(-nodeHealthFreshWindow - time.Second)}
	health.mu.Unlock()
	rec := httptest.NewRecorder()
	listBackupNodesHandler(store, health).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/backup-nodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, node := range response.Nodes {
		byName[node["hostname"].(string)] = node
	}
	if byName["failed"]["node_state"] != "error" || byName["failed"]["node_error"] != "usage measurement failed" || byName["failed"]["reported_at"] == nil {
		t.Fatalf("failed node = %+v", byName["failed"])
	}
	if byName["running"]["node_state"] != "running" || byName["running"]["reported_at"] == nil {
		t.Fatalf("running node = %+v", byName["running"])
	}
	for _, name := range []string{"stale", "absent"} {
		for _, key := range []string{"node_state", "node_error", "reported_at"} {
			if _, ok := byName[name][key]; ok {
				t.Fatalf("%s node unexpectedly has %s: %+v", name, key, byName[name])
			}
		}
	}
}
