package api

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"servermonitor/internal/server/agentdist"
	"servermonitor/internal/server/storage"
	"servermonitor/pkg/agentsig"
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

func downloadAgentHandler(hosts *storage.Hosts, signer *agentsig.Signer) http.HandlerFunc {
	cache := &binaryDigestCache{}
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Agent-Token")
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
		p, ok := agentdist.Lookup(id)
		if !ok {
			writeError(w, http.StatusNotFound, "no agent for platform "+id)
			return
		}
		digest, err := cache.digestFor(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "digest: "+err.Error())
			return
		}
		f, _, err := agentdist.Open(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer f.Close()

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", p.Size))
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, p.Filename))
		w.Header().Set("Cache-Control", "no-store")
		if signer != nil {
			w.Header().Set(agentsig.SignatureHeader, signer.SignDigest(digest))
			w.Header().Set("X-Agent-Pubkey", signer.PublicKeyHex())
		}
		_, _ = io.Copy(w, f)
	}
}

type binaryDigestCache struct {
	mu  sync.Mutex
	out map[string][]byte
}

func (c *binaryDigestCache) digestFor(id string) ([]byte, error) {
	c.mu.Lock()
	if d, ok := c.out[id]; ok {
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()
	f, _, err := agentdist.Open(id)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	digest, _, err := agentsig.DigestReader(f)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.out == nil {
		c.out = make(map[string][]byte)
	}
	c.out[id] = digest
	c.mu.Unlock()
	return digest, nil
}

func installScriptHandler(kind string, trusted []*net.IPNet, trustProxyTLS bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serverURL := canonicalServerURL(r, trusted, trustProxyTLS)
		if !isSafeRenderURL(serverURL) {
			writeError(w, http.StatusBadRequest, "request host is not a renderable server URL; check Host / X-Forwarded-Host")
			return
		}
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

func isSafeRenderURL(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.Path != "" && u.Path != "/" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip == nil {
		for _, c := range host {
			ok := (c >= 'a' && c <= 'z') ||
				(c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') ||
				c == '-' || c == '.'
			if !ok {
				return false
			}
		}
	}
	if p := u.Port(); p != "" {
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

type serverInfoDTO struct {
	URL      string `json:"url"`
	Version  string `json:"version"`
	Hostname string `json:"hostname,omitempty"`
}

func serverInfoHandler(version string, trusted []*net.IPNet, trustProxyTLS bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, serverInfoDTO{
			URL:     canonicalServerURL(r, trusted, trustProxyTLS),
			Version: version,
		})
	}
}

func canonicalServerURL(r *http.Request, trusted []*net.IPNet, trustProxyTLS bool) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	trustForwarded := trustProxyTLS || (len(trusted) > 0 && ipInNets(net.ParseIP(remoteIP(r)), trusted))
	if trustForwarded {
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
