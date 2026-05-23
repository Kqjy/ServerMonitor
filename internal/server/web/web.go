package web

import (
	"bytes"
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

var ErrUnbuiltDist = errors.New("web/dist looks unbuilt: run `npm --prefix web run build` before `go build ./cmd/server`")

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
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r)
	}), nil
}

func verifyBuilt(sub fs.FS) error {
	hasJS := false
	walkErr := fs.WalkDir(sub, "_app/immutable", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".js") {
			hasJS = true
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil || !hasJS {
		return ErrUnbuiltDist
	}
	return nil
}
