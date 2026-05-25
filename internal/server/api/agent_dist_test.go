package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsSafeRenderURL(t *testing.T) {
	good := []string{
		"https://monitor.example.com",
		"https://monitor.example.com/",
		"http://192.168.1.10:8080",
		"https://[::1]:8443",
		"https://a-b.c.d:443",
	}
	bad := []string{
		"",
		"ftp://monitor.example.com",
		"https://monitor.example.com/some/path",
		"https://monitor.example.com?q=1",
		"https://user:pw@monitor.example.com",
		`https://x";rm -rf /;#`,
		`https://x$(whoami)`,
		"https://x;rm",
		"https://x with space",
		"javascript:alert(1)",
		"https://example.com:notaport",
		"https://" + strings.Repeat("a", 300),
	}
	for _, s := range good {
		if !isSafeRenderURL(s) {
			t.Errorf("expected safe: %q", s)
		}
	}
	for _, s := range bad {
		if isSafeRenderURL(s) {
			t.Errorf("expected unsafe: %q", s)
		}
	}
}

func TestInstallScriptHandlerRejectsBadHost(t *testing.T) {
	h := installScriptHandler("sh", nil, false)
	req := httptest.NewRequest("GET", "http://x/install.sh", nil)
	req.Host = "evil;rm"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("want 400 for shell-injectable Host, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInstallScriptHandlerAcceptsCleanHost(t *testing.T) {
	h := installScriptHandler("sh", nil, false)
	req := httptest.NewRequest("GET", "http://x/install.sh", nil)
	req.Host = "monitor.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200 for clean Host, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "monitor.example.com") {
		t.Fatalf("rendered installer missing host: %s", rec.Body.String())
	}
}
