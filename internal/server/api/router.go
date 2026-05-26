package api

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"

	"servermonitor/internal/server/archive"
	"servermonitor/internal/server/auth"
	"servermonitor/internal/server/ingest"
	"servermonitor/internal/server/sse"
	"servermonitor/internal/server/storage"
	"servermonitor/pkg/agentsig"
)

type Router struct {
	*chi.Mux
}

type Deps struct {
	Hosts          *storage.Hosts
	DB             *storage.DB
	Batcher        *ingest.Batcher
	Hub            *sse.Hub
	Auth           *auth.Service
	Archive        *archive.Archiver
	Logger         *slog.Logger
	AdminToken     string
	IngestRate     int
	IngestBurst    int
	Version        string
	WebHandler     http.Handler
	Retention      RetentionConfig
	TrustedProxies []*net.IPNet
	SecureCookies  bool
	TrustProxyTLS  bool
	AgentSigner    *agentsig.Signer
}

func New(d Deps) *Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(d.Logger))
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders(d.SecureCookies))
	r.Use(csrfIssuer(d.SecureCookies))
	r.Use(auditLogger(d.DB.Pool, d.TrustedProxies))

	timeout := middleware.Timeout(30 * time.Second)

	r.Get("/healthz", healthHandler)
	r.With(timeout).Get("/install.sh", installScriptHandler("sh", d.TrustedProxies, d.TrustProxyTLS))
	r.With(timeout).Get("/install.ps1", installScriptHandler("ps1", d.TrustedProxies, d.TrustProxyTLS))

	r.Route("/api/v1", func(r chi.Router) {
		r.With(timeout).Get("/auth/status", authStatusHandler(d.Auth))
		r.With(timeout).Get("/agent/binary", downloadAgentHandler(d.Hosts, d.AgentSigner))
		r.Group(func(r chi.Router) {
			r.Use(timeout)
			r.Use(authRateLimiter(d.TrustedProxies))
			r.Use(csrfVerifier())
			r.Post("/auth/setup", setupHandler(d.Auth, d.SecureCookies))
			r.Post("/auth/login", loginHandler(d.Auth, d.SecureCookies))
		})
		r.With(timeout, csrfVerifier()).Post("/auth/logout", logoutHandler(d.Auth, d.SecureCookies))

		r.Group(func(r chi.Router) {
			r.Use(timeout)
			r.Use(ingestLimiter(d.IngestRate, d.IngestBurst))
			r.Use(requireAgentToken(d.Hosts))
			r.Post("/ingest", ingestHandler(d.Batcher, d.Hub, d.Hosts, d.AgentSigner, d.Logger))
		})

		r.Group(func(r chi.Router) {
			r.Use(requireUser(d.Auth, d.AdminToken, d.SecureCookies))
			r.Use(csrfVerifier())

			r.Get("/stream", d.Hub.HandleSSE)

			r.Group(func(r chi.Router) {
				r.Use(timeout)
				r.Get("/auth/me", meHandler())
				r.Post("/auth/password", changePasswordHandler(d.Auth, d.SecureCookies))
				r.Post("/admin/hosts", registerHostHandler(d.Hosts, d.AgentSigner))
				r.Patch("/admin/hosts/{id}", updateHostHandler(d.DB, d.Hosts))
				r.Post("/admin/hosts/{id}/upgrade", requestHostUpgradeHandler(d.DB, d.Hosts))
				r.Delete("/admin/hosts/{id}", deleteHostHandler(d.Hosts))
				r.Get("/hosts", listHostsHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}", getHostHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/processes", hostProcessesHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/processes/{pid}/series", hostProcessSeriesHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/containers", hostContainersHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/containers/{cid}/series", hostContainerSeriesHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/ports", hostPortsHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/labels", hostLabelsHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/alerts/active", hostActiveAlertsHandler(d.DB, d.Hosts))
				r.Get("/series", seriesHandler(d.DB, d.Hosts, d.Archive, d.Retention))
				r.Get("/series/multi", multiSeriesHandler(d.DB, d.Hosts, d.Archive, d.Retention))
				r.Get("/metrics", listMetricsHandler())
				r.Get("/stats", statsHandler(d.Batcher))
				r.Get("/retention", retentionHandler(d.Retention))
				r.Get("/agent/platforms", listPlatformsHandler())
				r.Get("/server/info", serverInfoHandler(d.Version, d.TrustedProxies, d.TrustProxyTLS))

				r.Get("/alerts", listAlertRulesHandler(d.DB.Pool))
				r.Post("/alerts", createAlertRuleHandler(d.DB.Pool))
				r.Put("/alerts/{id}", updateAlertRuleHandler(d.DB.Pool))
				r.Delete("/alerts/{id}", deleteAlertRuleHandler(d.DB.Pool))
				r.Get("/alerts/history", alertHistoryHandler(d.DB.Pool))

				r.Get("/channels", listChannelsHandler(d.DB.Pool))
				r.Post("/channels", createChannelHandler(d.DB.Pool))
				r.Put("/channels/{id}", updateChannelHandler(d.DB.Pool))
				r.Delete("/channels/{id}", deleteChannelHandler(d.DB.Pool))
			})
		})
	})

	if d.WebHandler != nil {
		r.Mount("/", d.WebHandler)
	}

	return &Router{r}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				logger.Debug("http",
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"dur_ms", time.Since(start).Milliseconds(),
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

func ingestLimiter(rps, burst int) func(http.Handler) http.Handler {
	if rps <= 0 {
		rps = 10
	}
	if burst <= 0 {
		burst = 30
	}
	limiters := newLimiterMap(rate.Limit(rps), burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get("X-Agent-Token")
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			lim := limiters.get(token)
			if !lim.Allow() {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
