package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSRFIssuerSetsCookieOnGET(t *testing.T) {
	h := csrfIssuer(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil))
	cookies := rec.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == csrfCookie && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected sm_csrf cookie to be set, got %+v", cookies)
	}
}

func TestCSRFIssuerKeepsExistingCookie(t *testing.T) {
	h := csrfIssuer(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "existing"})
	h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookie {
			t.Fatalf("issuer should not overwrite existing csrf cookie, got %q", c.Value)
		}
	}
}

func TestCSRFVerifierBlocksMissingCookie(t *testing.T) {
	called := false
	h := csrfVerifier()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/hosts", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if called {
		t.Fatalf("handler should not have been called")
	}
	if !strings.Contains(rec.Body.String(), "csrf") {
		t.Fatalf("expected csrf error, got %q", rec.Body.String())
	}
}

func TestCSRFVerifierBlocksMismatch(t *testing.T) {
	h := csrfVerifier()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/hosts", nil)
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "tokenA"})
	req.Header.Set(csrfHeader, "tokenB")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestCSRFVerifierAcceptsMatch(t *testing.T) {
	called := false
	h := csrfVerifier()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/hosts", nil)
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "matching-token"})
	req.Header.Set(csrfHeader, "matching-token")
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatalf("handler should have run; rec=%d %q", rec.Code, rec.Body.String())
	}
}

func TestCSRFVerifierSkipsSafeMethods(t *testing.T) {
	called := false
	h := csrfVerifier()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil))
	if !called {
		t.Fatalf("GET should bypass csrf check")
	}
}

func TestCSRFVerifierSkipsTokenAuth(t *testing.T) {
	for _, header := range []string{"X-Admin-Token", "X-Agent-Token"} {
		t.Run(header, func(t *testing.T) {
			called := false
			h := csrfVerifier()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/hosts", nil)
			req.Header.Set(header, "any-value")
			h.ServeHTTP(rec, req)
			if !called {
				t.Fatalf("%s should bypass csrf", header)
			}
		})
	}
}
