package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"servermonitor/internal/server/auth"
)

type actorSlot struct {
	user atomic.Pointer[auth.User]
}

func securityHeaders(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			if strings.HasPrefix(r.URL.Path, "/api/") {
				h.Set("Cache-Control", "no-store")
			}
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "interest-cohort=(), browsing-topics=()")
			h.Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self' 'unsafe-inline'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data:; "+
					"connect-src 'self'; "+
					"font-src 'self' data:; "+
					"object-src 'none'; "+
					"base-uri 'self'; "+
					"frame-ancestors 'none'")
			if secure {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func authRateLimiter(trusted []*net.IPNet) func(http.Handler) http.Handler {
	limiters := newLimiterMap(rate.Every(12*time.Second), 5)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := clientIP(r, trusted)
			lim := limiters.get(key)
			if !lim.Allow() {
				w.Header().Set("Retry-After", "30")
				writeError(w, http.StatusTooManyRequests, "too many attempts")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func remoteIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request, trusted []*net.IPNet) string {
	direct := remoteIP(r)
	if len(trusted) == 0 {
		return direct
	}
	if !ipInNets(net.ParseIP(direct), trusted) {
		return direct
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(parts[i])
			if ip == "" {
				continue
			}
			parsed := net.ParseIP(ip)
			if parsed == nil {
				continue
			}
			if !ipInNets(parsed, trusted) {
				return ip
			}
		}
		first := strings.TrimSpace(parts[0])
		if first != "" {
			return first
		}
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	return direct
}

func auditLogger(pool *pgxpool.Pool, trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			slot := &actorSlot{}
			ctx := context.WithValue(r.Context(), ctxActorSlot, slot)
			next.ServeHTTP(ww, r.WithContext(ctx))

			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				return
			}
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
				return
			}
			actor := "anonymous"
			if u := slot.user.Load(); u != nil {
				actor = u.Username
			}
			logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = pool.Exec(logCtx, `
				INSERT INTO audit_log (actor, method, path, remote_ip, status, extra)
				VALUES ($1, $2, $3, $4, $5, '{}'::jsonb)
			`, actor, r.Method, r.URL.Path, clientIP(r, trusted), ww.Status())
		})
	}
}

var _ = auth.SessionCookie
