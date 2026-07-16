package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

var ErrUnbuiltDist = errors.New("web/dist looks unbuilt: run `npm --prefix web run build` before `go build ./cmd/server`")

var indexAssetPattern = regexp.MustCompile(`(?:href|src)=["']/?(_app/immutable/[^"'?#]+)`)

func Handler() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	if err := verifyBuilt(sub); err != nil {
		return nil, err
	}
	indexFile, err := sub.Open("index.html")
	if err != nil {
		return nil, err
	}
	indexBytes, err := io.ReadAll(indexFile)
	_ = indexFile.Close()
	if err != nil {
		return nil, err
	}
	startTime := time.Now()

	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", startTime, bytes.NewReader(indexBytes))
	}

	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" || path == "index.html" {
			serveIndex(w, r)
			return
		}
		if f, err := sub.Open(path); err == nil {
			_ = f.Close()
			if path == "_app/version.json" {
				w.Header().Set("Cache-Control", "no-cache")
			} else if strings.HasPrefix(path, "_app/immutable/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r)
	}), nil
}

func verifyBuilt(sub fs.FS) error {
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return fmt.Errorf("%w: read index.html: %v", ErrUnbuiltDist, err)
	}
	refs := indexAssetPattern.FindAllSubmatch(index, -1)
	hasJS := false
	for _, ref := range refs {
		path := string(ref[1])
		if strings.HasSuffix(path, ".js") {
			hasJS = true
		}
		if _, err := fs.Stat(sub, path); err != nil {
			return fmt.Errorf("%w: index references missing asset %s", ErrUnbuiltDist, path)
		}
	}
	if !hasJS {
		return fmt.Errorf("%w: index has no JavaScript entry", ErrUnbuiltDist)
	}
	manifest, err := fs.ReadFile(sub, "_app/version.json")
	if err != nil {
		return fmt.Errorf("%w: read version manifest: %v", ErrUnbuiltDist, err)
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &parsed); err != nil || strings.TrimSpace(parsed.Version) == "" {
		return fmt.Errorf("%w: invalid version manifest", ErrUnbuiltDist)
	}
	return nil
}
