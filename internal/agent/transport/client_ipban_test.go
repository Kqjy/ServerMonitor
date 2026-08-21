package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIPBanAckSignalsKeepLatestVersion(t *testing.T) {
	var version atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ipban_version":`+itoa(version.Load())+`}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", 5*time.Second, false, slog.New(slog.DiscardHandler), nil)
	version.Store(7)
	if err := c.postBytes(context.Background(), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-c.IPBanSignals():
		if v != 7 {
			t.Fatalf("version = %d, want 7", v)
		}
	case <-time.After(time.Second):
		t.Fatal("ipban signal was not delivered")
	}
	version.Store(8)
	if err := c.postBytes(context.Background(), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	version.Store(9)
	if err := c.postBytes(context.Background(), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-c.IPBanSignals():
		if v != 9 {
			t.Fatalf("coalesced version = %d, want 9", v)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced ipban signal was not delivered")
	}
	version.Store(0)
	if err := c.postBytes(context.Background(), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-c.IPBanSignals():
		t.Fatalf("unexpected signal %d for a zero version", v)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFetchIPBanConfig(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agent-Token") != "secret-token" {
			t.Errorf("token = %q", r.Header.Get("X-Agent-Token"))
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/agent/ipban" {
			http.NotFound(w, r)
			return
		}
		code := int(status.Load())
		if code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		_, _ = io.WriteString(w, `{"version":42,"policy":{"detect":true,"enforce":false,"apply_fleet":true,"mode":"normal","max_retry":5,"find_time_s":600,"ban_time_s":3600,"ban_time_max_s":604800,"ban_private":false},"allowlist":["203.0.113.0/24"],"fleet":[{"ip":"198.51.100.7","expires_at":"2030-01-01T00:00:00Z"}],"unban":[{"ip":"198.51.100.8","at":"2026-08-21T00:00:00Z"}],"future_field":1}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-token", 5*time.Second, false, slog.New(slog.DiscardHandler), nil)
	cfg, err := c.FetchIPBanConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 42 || !cfg.Policy.Detect || cfg.Policy.MaxRetry != 5 || len(cfg.Allowlist) != 1 || len(cfg.Fleet) != 1 || len(cfg.Unban) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
	status.Store(http.StatusNoContent)
	cfg, err = c.FetchIPBanConfig(context.Background())
	if err != nil || cfg != nil {
		t.Fatalf("204 should yield nil config, got %+v %v", cfg, err)
	}
	status.Store(http.StatusGone)
	if _, err := c.FetchIPBanConfig(context.Background()); !errors.Is(err, ErrDeregistered) {
		t.Fatalf("410 should deregister, got %v", err)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
