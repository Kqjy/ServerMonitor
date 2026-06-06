package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
