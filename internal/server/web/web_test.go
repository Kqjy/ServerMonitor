package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestVerifyBuiltAcceptsRealDist(t *testing.T) {
	sub := fstest.MapFS{
		"index.html":                    &fstest.MapFile{Data: []byte(`<link href="/_app/immutable/entry/start.js">`)},
		"_app/version.json":             &fstest.MapFile{Data: []byte(`{"version":"test"}`)},
		"_app/immutable/entry/start.js": &fstest.MapFile{Data: []byte("export {}")},
		"_app/immutable/nodes/0.js":     &fstest.MapFile{Data: []byte("export {}")},
	}
	if err := verifyBuilt(sub); err != nil {
		t.Fatalf("expected build to pass, got %v", err)
	}
}

func TestVerifyBuiltRejectsMissingReferencedAsset(t *testing.T) {
	sub := fstest.MapFS{
		"index.html":                        &fstest.MapFile{Data: []byte(`<link href="/_app/immutable/entry/missing.js">`)},
		"_app/version.json":                 &fstest.MapFile{Data: []byte(`{"version":"test"}`)},
		"_app/immutable/entry/unrelated.js": &fstest.MapFile{Data: []byte("export {}")},
	}
	if err := verifyBuilt(sub); !errors.Is(err, ErrUnbuiltDist) {
		t.Fatalf("expected ErrUnbuiltDist for a missing referenced asset, got %v", err)
	}
}

func TestHandlerCachePolicy(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/index.html":        "no-cache",
		"/_app/version.json": "no-cache",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if got := rec.Header().Get("Cache-Control"); got != want {
				t.Fatalf("Cache-Control = %q, want %q", got, want)
			}
		})
	}
}

func TestVerifyBuiltRejectsEmptyDist(t *testing.T) {
	sub := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}
	if err := verifyBuilt(sub); !errors.Is(err, ErrUnbuiltDist) {
		t.Fatalf("expected ErrUnbuiltDist, got %v", err)
	}
}

func TestVerifyBuiltRejectsDistWithoutJS(t *testing.T) {
	sub := fstest.MapFS{
		"index.html":                  &fstest.MapFile{Data: []byte("<html></html>")},
		"_app/immutable/assets/0.css": &fstest.MapFile{Data: []byte("body{}")},
	}
	if err := verifyBuilt(sub); !errors.Is(err, ErrUnbuiltDist) {
		t.Fatalf("expected ErrUnbuiltDist when no JS chunks exist, got %v", err)
	}
}
