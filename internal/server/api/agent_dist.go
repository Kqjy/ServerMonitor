package api

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"servermonitor/internal/server/agentdist"
	"servermonitor/internal/server/storage"
)

type platformDTO struct {
	ID    string `json:"id"`
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	Label string `json:"label"`
	Size  int64  `json:"size"`
}

func listPlatformsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ps := agentdist.Available()
		out := make([]platformDTO, 0, len(ps))
		for _, p := range ps {
			out = append(out, platformDTO{ID: p.ID, OS: p.OS, Arch: p.Arch, Label: p.Label, Size: p.Size})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func downloadAgentHandler(hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Agent-Token")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "agent token required")
			return
		}
		if _, err := hosts.ResolveToken(r.Context(), token); err != nil {
			if errors.Is(err, storage.ErrTombstoned) {
				writeError(w, http.StatusGone, "host deregistered")
				return
			}
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusForbidden, "invalid agent token")
				return
			}
			writeError(w, http.StatusInternalServerError, "token lookup failed")
			return
		}

		id := r.URL.Query().Get("platform")
		if id == "" {
			id = agentdist.PlatformFromUserAgent(r.Header.Get("User-Agent"))
		}
		f, p, err := agentdist.Open(id)
		if err != nil {
			if errors.Is(err, agentdist.ErrUnknownPlatform) {
				writeError(w, http.StatusNotFound, "no agent for platform "+id)
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer f.Close()

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", p.Size))
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, p.Filename))
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.Copy(w, f)
	}
}

func installScriptHandler(kind string, trusted []*net.IPNet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serverURL := canonicalServerURL(r, trusted)
		var body string
		switch kind {
		case "sh":
			body = agentdist.RenderShellInstaller(serverURL)
			w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		case "ps1":
			body = agentdist.RenderPowerShellInstaller(serverURL)
			w.Header().Set("Content-Type", "application/x-powershell; charset=utf-8")
		default:
			writeError(w, http.StatusNotFound, "unknown script")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, body)
	}
}

type serverInfoDTO struct {
	URL      string `json:"url"`
	Version  string `json:"version"`
	Hostname string `json:"hostname,omitempty"`
}

func serverInfoHandler(version string, trusted []*net.IPNet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, serverInfoDTO{
			URL:     canonicalServerURL(r, trusted),
			Version: version,
		})
	}
}

func canonicalServerURL(r *http.Request, trusted []*net.IPNet) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if len(trusted) > 0 && ipInNets(net.ParseIP(remoteIP(r)), trusted) {
		if v := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); v != "" {
			if i := strings.Index(v, ","); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			scheme = v
		}
		if v := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); v != "" {
			if i := strings.Index(v, ","); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			host = v
		}
	}
	host = strings.TrimRight(host, "/")
	return scheme + "://" + host
}
