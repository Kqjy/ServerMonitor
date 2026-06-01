package web

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestVerifyBuiltAcceptsRealDist(t *testing.T) {
	sub := fstest.MapFS{
		"index.html":                    &fstest.MapFile{Data: []byte("<html></html>")},
		"_app/version.json":             &fstest.MapFile{Data: []byte("{}")},
		"_app/immutable/entry/start.js": &fstest.MapFile{Data: []byte("export {}")},
		"_app/immutable/nodes/0.js":     &fstest.MapFile{Data: []byte("export {}")},
	}
	if err := verifyBuilt(sub); err != nil {
		t.Fatalf("expected build to pass, got %v", err)
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
