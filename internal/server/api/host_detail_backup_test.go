package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"servermonitor/internal/server/storage"
	"servermonitor/pkg/wire"
)

type browseTestHosts struct {
	hosts map[int64]storage.Host
}

func (h browseTestHosts) Get(_ context.Context, id int64) (storage.Host, error) {
	host, ok := h.hosts[id]
	if !ok {
		return storage.Host{}, storage.ErrNotFound
	}
	return host, nil
}

func TestBackupTargetDeletable(t *testing.T) {
	cases := []struct {
		name        string
		storedUsed  int64
		hasObjects  bool
		repoChecked bool
		nodeHosted  bool
		want        bool
	}{
		{"empty, backend confirms empty", 0, false, true, false, true},
		{"empty, no backend configured", 0, false, false, false, true},
		{"stored counter shows data", 4096, false, true, false, false},
		{"counter clear but backend holds objects", 0, true, true, false, false},
		{"zero-byte objects only (bytes==0 but objects exist)", 0, true, true, false, false},
		{"counter clear, backend unknown, stays deletable", 0, true, false, false, true},
		{"node namespace is never hard-deletable", 0, false, false, true, false},
	}
	for _, c := range cases {
		if got := backupTargetDeletable(c.storedUsed, c.hasObjects, c.repoChecked, c.nodeHosted); got != c.want {
			t.Errorf("%s: backupTargetDeletable(%d,%v,%v,%v) = %v, want %v", c.name, c.storedUsed, c.hasObjects, c.repoChecked, c.nodeHosted, got, c.want)
		}
	}
}

func TestHostBackupsResponseShape(t *testing.T) {
	updatedAt := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	resp := hostBackupsResponse{Repos: []backupRepoSnapshot{
		{
			Repo:      "alpha",
			UpdatedAt: updatedAt,
			Status:    json.RawMessage(`{"name":"alpha","success":true,"snapshots":[{"id":"snap-a"}]}`),
		},
	}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	want := `{"repos":[{"repo":"alpha","updated_at":"2026-07-03T03:00:00Z","status":{"name":"alpha","success":true,"snapshots":[{"id":"snap-a"}]}}]}`
	if string(data) != want {
		t.Fatalf("response json = %s, want %s", data, want)
	}
	empty, err := json.Marshal(hostBackupsResponse{Repos: []backupRepoSnapshot{}})
	if err != nil {
		t.Fatalf("marshal empty response: %v", err)
	}
	if string(empty) != `{"repos":[]}` {
		t.Fatalf("empty response json = %s", empty)
	}
}

func TestBrowseStoreLifecycleAndHostIsolation(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	store := newBrowseStore()
	store.now = func() time.Time { return now }
	job, err := store.create(10, "repo1", "latest", "/")
	if err != nil {
		t.Fatal(err)
	}
	if !store.HasPending(10) || store.HasPending(20) {
		t.Fatal("pending state was not isolated to host 10")
	}
	if jobs := store.pick(20); len(jobs) != 0 {
		t.Fatalf("host 20 fetched host 10 jobs: %#v", jobs)
	}
	jobs := store.pick(10)
	if len(jobs) != 1 || jobs[0].ID != job.ID || store.HasPending(10) {
		t.Fatalf("picked jobs = %#v", jobs)
	}
	result := wire.BackupBrowseResult{Entries: []wire.BackupBrowseEntry{{Name: "etc", Type: "dir"}}}
	if store.finish(20, job.ID, result) {
		t.Fatal("host 20 posted a result for host 10")
	}
	if !store.finish(10, job.ID, result) {
		t.Fatal("host 10 could not finish its running job")
	}
	got, ok := store.get(10, job.ID)
	if !ok || got.State != "done" || got.Result == nil || got.Result.Entries[0].Name != "etc" {
		t.Fatalf("stored job = %#v", got)
	}
	if _, ok := store.get(20, job.ID); ok {
		t.Fatal("host 20 read host 10 job")
	}
}

func TestBrowseStoreTimeoutsAndCap(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	store := newBrowseStore()
	store.now = func() time.Time { return now }
	job, err := store.create(1, "repo", "latest", "/")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(91 * time.Second)
	timedOut, ok := store.get(1, job.ID)
	if !ok || timedOut.State != "failed" || timedOut.Result == nil || timedOut.Result.ErrorKind != "agent_unreachable" {
		t.Fatalf("queued timeout = %#v", timedOut)
	}

	store = newBrowseStore()
	store.now = func() time.Time { return now }
	for i := 0; i < browseHostCap; i++ {
		if _, err := store.create(2, "repo", "latest", "/"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.create(2, "repo", "latest", "/"); !errors.Is(err, errBrowseHostCap) {
		t.Fatalf("fifth active job error = %v", err)
	}
}

func TestBackupBrowseValidation(t *testing.T) {
	valid := []struct {
		repo, snapshot, path string
	}{{"repo", "latest", "/"}, {"repo", "aB12", "/var/lib"}}
	for _, tc := range valid {
		if err := validateBackupBrowse(tc.repo, tc.snapshot, tc.path); err != nil {
			t.Fatalf("valid browse rejected: %v", err)
		}
	}
	invalid := []struct {
		repo, snapshot, path string
	}{{"", "latest", "/"}, {"repo", "-latest", "/"}, {"repo", "latest", "relative"}, {"repo", "latest", "/etc/../root"}, {"repo", "latest", "/bad\x00path"}}
	for _, tc := range invalid {
		if err := validateBackupBrowse(tc.repo, tc.snapshot, tc.path); err == nil {
			t.Fatalf("invalid browse accepted: %#v", tc)
		}
	}
}

func TestCreateBackupBrowseHandlerValidationRepoAndCap(t *testing.T) {
	hosts := browseTestHosts{hosts: map[int64]storage.Host{1: {ID: 1}}}
	knownRepo := func(_ context.Context, _ int64, repo string) (bool, error) { return repo == "known", nil }
	request := func(store *browseStore, body string) *httptest.ResponseRecorder {
		router := chi.NewRouter()
		router.Post("/api/v1/hosts/{id}/backups/browse", createBackupBrowseHandlerWithRepoLookup(store, hosts, knownRepo))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/hosts/1/backups/browse", bytes.NewBufferString(body)))
		return rec
	}

	for _, body := range []string{
		`{"repo":"known","snapshot":"-latest","path":"/"}`,
		`{"repo":"known","snapshot":"latest","path":"relative"}`,
		`{"repo":"known","snapshot":"latest","path":"/etc/../root"}`,
	} {
		rec := request(newBrowseStore(), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid request status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	rec := request(newBrowseStore(), `{"repo":"missing","snapshot":"latest","path":"/"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown repo status = %d, body = %s", rec.Code, rec.Body.String())
	}

	store := newBrowseStore()
	for i := 0; i < browseHostCap; i++ {
		if _, err := store.create(1, "known", "latest", "/"); err != nil {
			t.Fatal(err)
		}
	}
	rec = request(store, `{"repo":"known","snapshot":"latest","path":"/"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("cap status = %d, body = %s", rec.Code, rec.Body.String())
	}

	store = newBrowseStore()
	rec = request(store, `{"repo":"known","snapshot":"latest","path":"/srv"}`)
	if rec.Code != http.StatusAccepted || !store.HasPending(1) {
		t.Fatalf("accepted status = %d, pending = %v, body = %s", rec.Code, store.HasPending(1), rec.Body.String())
	}
}

func TestAgentBackupBrowseResultStrictDecode(t *testing.T) {
	store := newBrowseStore()
	job, err := store.create(1, "repo", "latest", "/")
	if err != nil {
		t.Fatal(err)
	}
	store.pick(1)
	router := chi.NewRouter()
	router.Post("/api/v1/agent/backup-browse/{jobID}", agentBackupBrowseResultHandler(store))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/backup-browse/"+job.ID, bytes.NewBufferString(`{"entries":[],"unknown":true}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxHostID, int64(1)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestGetBackupBrowseJobHandlerReturnsResult(t *testing.T) {
	store := newBrowseStore()
	job, err := store.create(7, "repo", "latest", "/")
	if err != nil {
		t.Fatal(err)
	}
	store.pick(7)
	if !store.finish(7, job.ID, wire.BackupBrowseResult{Entries: []wire.BackupBrowseEntry{{Name: "srv", Type: "dir"}}}) {
		t.Fatal("finish failed")
	}
	router := chi.NewRouter()
	router.Get("/api/v1/hosts/{id}/backups/browse/{jobID}", getBackupBrowseJobHandler(store, browseTestHosts{hosts: map[int64]storage.Host{7: {ID: 7}}}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hosts/7/backups/browse/"+job.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Status string                   `json:"status"`
		Result *wire.BackupBrowseResult `json:"result"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "done" || response.Result == nil || response.Result.Entries[0].Name != "srv" {
		t.Fatalf("response = %#v", response)
	}
}
