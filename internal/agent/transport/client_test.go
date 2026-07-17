package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"servermonitor/pkg/wire"
)

func TestRetryableStatus(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{http.StatusMovedPermanently, true},
		{http.StatusFound, true},
		{http.StatusSeeOther, true},
		{http.StatusTemporaryRedirect, true},
		{http.StatusPermanentRedirect, true},
		{http.StatusNotFound, true},
		{http.StatusMethodNotAllowed, true},
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusRequestEntityTooLarge, false},
	}
	for _, tc := range cases {
		if got := retryableStatus(tc.status); got != tc.want {
			t.Errorf("retryableStatus(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestBackupBrowseAckSignals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"backup_browse_pending":true}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", 5*time.Second, false, slog.New(slog.DiscardHandler), nil)
	if err := c.postBytes(context.Background(), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.BrowseSignals():
	case <-time.After(time.Second):
		t.Fatal("backup browse signal was not delivered")
	}
}

func TestBackupBrowseFetchAndPostHelpers(t *testing.T) {
	var posted wire.BackupBrowseResult
	requests := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agent-Token") != "secret-token" {
			t.Errorf("token = %q", r.Header.Get("X-Agent-Token"))
		}
		requests <- r.Method + " " + r.URL.Path
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/backup-browse":
			_, _ = io.WriteString(w, `{"jobs":[{"id":"job1","repo":"repo1","snapshot":"latest","path":"/etc"}],"future_field":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/backup-browse/job1":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode posted result: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-token", 5*time.Second, false, slog.New(slog.DiscardHandler), nil)
	jobs, err := c.FetchBackupBrowseJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "job1" || jobs[0].Path != "/etc" {
		t.Fatalf("jobs = %#v", jobs)
	}
	result := wire.BackupBrowseResult{Entries: []wire.BackupBrowseEntry{{Name: "hosts", Type: "file", Size: 10}}}
	if err := c.PostBackupBrowseResult(context.Background(), "job1", result); err != nil {
		t.Fatal(err)
	}
	if len(posted.Entries) != 1 || posted.Entries[0].Name != "hosts" {
		t.Fatalf("posted = %#v", posted)
	}
	if got := <-requests; got != "GET /api/v1/agent/backup-browse" {
		t.Fatalf("first request = %q", got)
	}
	if got := <-requests; got != "POST /api/v1/agent/backup-browse/job1" {
		t.Fatalf("second request = %q", got)
	}
}

func TestPostBytesRefusesRedirect(t *testing.T) {
	var ingestHit atomic.Bool
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ingestHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer redirector.Close()

	c := New(redirector.URL, "tok", 5*time.Second, false, slog.New(slog.DiscardHandler), nil)
	err := c.postBytes(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("expected an error when the server redirects, got nil")
	}
	if !errors.Is(err, errUnexpectedRedirect) {
		t.Fatalf("expected errUnexpectedRedirect, got %v", err)
	}
	if !isRetryable(err) {
		t.Fatalf("a redirect is a recoverable misconfiguration and must be retryable so data spools; got isRetryable=false for %v", err)
	}
	if ingestHit.Load() {
		t.Fatal("redirect was followed: the POST reached the redirect target, meaning it was downgraded/forwarded")
	}
}

func TestPostBytesMethodNotAllowedIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", 5*time.Second, false, slog.New(slog.DiscardHandler), nil)
	err := c.postBytes(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("expected an error for status 405, got nil")
	}
	if !isRetryable(err) {
		t.Fatalf("405 must be retryable so batches spool instead of being dropped; got isRetryable=false for %v", err)
	}
}
