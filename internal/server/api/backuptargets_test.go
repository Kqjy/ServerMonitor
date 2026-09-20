package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeQuota(t *testing.T) {
	zero := int64(0)
	negative := int64(-1)
	positive := int64(1024)

	if got, err := normalizeQuota(nil); err != nil || got != nil {
		t.Fatalf("normalizeQuota(nil) = %v, %v; want nil, nil", got, err)
	}
	if got, err := normalizeQuota(&zero); err != nil || got != nil {
		t.Fatalf("normalizeQuota(0) = %v, %v; want nil, nil", got, err)
	}
	if got, err := normalizeQuota(&negative); err == nil || got != nil {
		t.Fatalf("normalizeQuota(-1) = %v, %v; want nil, error", got, err)
	}
	if got, err := normalizeQuota(&positive); err != nil || got != &positive || *got != positive {
		t.Fatalf("normalizeQuota(1024) = %v, %v; want same pointer, nil", got, err)
	}
}

type repoNameReporterStub struct {
	reported bool
	err      error
	gotHost  int64
	gotName  string
}

func (s *repoNameReporterStub) HostReportsRepo(_ context.Context, hostID int64, name string) (bool, error) {
	s.gotHost = hostID
	s.gotName = name
	return s.reported, s.err
}

func TestRefuseRepoNameHostAlreadyBacksUp(t *testing.T) {
	cases := []struct {
		name     string
		store    *repoNameReporterStub
		handled  bool
		wantCode int
	}{
		{"free name", &repoNameReporterStub{}, false, http.StatusOK},
		{"name in use", &repoNameReporterStub{reported: true}, true, http.StatusConflict},
		{"lookup failed", &repoNameReporterStub{err: errors.New("db down")}, true, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/backup-targets", nil)
			handled := refuseRepoNameHostAlreadyBacksUp(rec, req, tc.store, 7, "nightly", nodeRepoNameConflict)
			if handled != tc.handled {
				t.Fatalf("handled = %v, want %v", handled, tc.handled)
			}
			if tc.store.gotHost != 7 || tc.store.gotName != "nightly" {
				t.Fatalf("looked up host %d repo %q", tc.store.gotHost, tc.store.gotName)
			}
			if !tc.handled {
				return
			}
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantCode == http.StatusConflict && !strings.Contains(rec.Body.String(), "never delete blobs") {
				t.Fatalf("conflict body = %q", rec.Body.String())
			}
		})
	}
}
