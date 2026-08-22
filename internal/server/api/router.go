package api

import (
	"context"
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
	"servermonitor/internal/server/ipban"
	"servermonitor/internal/server/sse"
	"servermonitor/internal/server/storage"
	"servermonitor/pkg/agentsig"
	"servermonitor/pkg/restserver"
)

type Router struct {
	*chi.Mux
}

type Deps struct {
	Hosts               *storage.Hosts
	DB                  *storage.DB
	Batcher             *ingest.Batcher
	Hub                 *sse.Hub
	Auth                *auth.Service
	Archive             *archive.Archiver
	Logger              *slog.Logger
	AdminToken          string
	IngestRate          int
	IngestBurst         int
	Version             string
	WebHandler          http.Handler
	Retention           RetentionConfig
	TrustedProxies      []*net.IPNet
	SecureCookies       bool
	TrustProxyTLS       bool
	AgentSigner         *agentsig.Signer
	BackupServer        *restserver.Server
	BackupTargets       *storage.BackupTargets
	BackupTLS           BackupTLSInfo
	BackupTunnel        *restserver.Tunnel
	TunnelPeers         *storage.BackupTunnelStore
	TunnelInfo          BackupTunnelInfo
	BackupPublic        bool
	BackupSecretsSecure bool
	BackupNodes         *storage.BackupNodes
	BackupBrowse        *browseStore
	IPBan               *ipban.Service
}

func New(d Deps) *Router {
	r := chi.NewRouter()
	nodePeerStats := NewNodePeerStatsCache()
	nodeHealth := NewNodeHealthCache()
	browse := d.BackupBrowse
	if browse == nil {
		browse = newBrowseStore()
	}
	r.Use(middleware.RequestID)
	r.Use(requestLogger(d.Logger))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(1))
	r.Use(securityHeaders(d.SecureCookies))
	r.Use(csrfIssuer(d.SecureCookies))
	r.Use(auditLogger(d.DB.Pool, d.TrustedProxies))

	timeout := middleware.Timeout(30 * time.Second)

	r.Get("/healthz", healthHandler(d.DB, d.Logger))
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
			r.Post("/ingest", ingestHandler(d.Batcher, d.Hub, d.Hosts, d.AgentSigner, d.Logger, browse, d.IPBan))
			if d.IPBan != nil {
				r.Get("/agent/ipban", agentIPBanConfigHandler(d.IPBan))
			}
			r.Post("/agent/tunnel", tunnelEnrollHandler(d.BackupTunnel, d.TunnelPeers, d.TunnelInfo))
			r.Get("/agent/backup-browse", agentBackupBrowseJobsHandler(browse))
			r.Post("/agent/backup-browse/{jobID}", agentBackupBrowseResultHandler(browse))
			r.Get("/agent/tunnel/nodes", agentTunnelNodesHandler(d.BackupNodes))
			r.Get("/agent/backup-node", agentBackupNodeConfigHandler(d.BackupNodes))
			r.Post("/agent/backup-node/usage", agentBackupNodeUsageHandler(d.BackupNodes, nodePeerStats, nodeHealth))
			if d.BackupTargets != nil {
				r.Get("/agent/backup-config", managedBackupConfigHandler(d.BackupTargets, d.BackupSecretsSecure))
			}
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
				r.Post("/admin/hosts/{id}/archive", archiveHostHandler(d.Hosts, d.BackupNodes))
				r.Post("/admin/hosts/{id}/unarchive", unarchiveHostHandler(d.Hosts))
				r.Delete("/admin/hosts/{id}", deleteHostHandler(d.Hosts, d.BackupTunnel, d.TunnelPeers, d.BackupNodes))
				r.Get("/hosts", listHostsHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}", getHostHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/processes", hostProcessesHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/processes/{pid}/series", hostProcessSeriesHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/containers", hostContainersHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/containers/{cid}/series", hostContainerSeriesHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/ports", hostPortsHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/backups", hostBackupsHandler(d.DB, d.Hosts))
				r.Post("/hosts/{id}/backups/browse", createBackupBrowseHandler(browse, d.DB, d.Hosts))
				r.Get("/hosts/{id}/backups/browse/{jobID}", getBackupBrowseJobHandler(browse, d.Hosts))
				r.Get("/hosts/{id}/labels", hostLabelsHandler(d.DB, d.Hosts))
				r.Get("/hosts/{id}/alerts/active", hostActiveAlertsHandler(d.DB, d.Hosts))
				r.Get("/series", seriesHandler(d.DB, d.Hosts, d.Archive, d.Retention))
				r.Get("/series/multi", multiSeriesHandler(d.DB, d.Hosts, d.Archive, d.Retention))
				r.Get("/series/group", seriesGroupHandler(d.DB, d.Hosts, d.Archive, d.Retention))
				r.Get("/series/batch", seriesBatchHandler(d.DB, d.Hosts, d.Retention))
				r.Get("/metrics", listMetricsHandler())
				r.Get("/stats", statsHandler(d.Batcher))
				r.Get("/retention", retentionHandler(d.Retention))
				r.Get("/storage", storageUsageHandler(d.DB, d.Archive))
				r.Get("/agent/platforms", listPlatformsHandler())
				r.Get("/server/info", serverInfoHandler(d.Version, d.TrustedProxies, d.TrustProxyTLS))

				r.Get("/alerts", listAlertRulesHandler(d.DB.Pool))
				r.Post("/alerts", createAlertRuleHandler(d.DB.Pool))
				r.Put("/alerts/{id}", updateAlertRuleHandler(d.DB.Pool))
				r.Delete("/alerts/{id}", deleteAlertRuleHandler(d.DB.Pool))
				r.Get("/alerts/history", alertHistoryHandler(d.DB.Pool))
				r.Delete("/alerts/history", clearAlertHistoryHandler(d.DB.Pool))

				r.Get("/channels", listChannelsHandler(d.DB.Pool))
				r.Post("/channels", createChannelHandler(d.DB.Pool))
				r.Put("/channels/{id}", updateChannelHandler(d.DB.Pool))
				r.Delete("/channels/{id}", deleteChannelHandler(d.DB.Pool))

				if d.IPBan != nil {
					r.Get("/ipban/settings", ipbanSettingsHandler(d.IPBan, d.TrustedProxies))
					r.Put("/ipban/settings", ipbanUpdateSettingsHandler(d.IPBan, d.TrustedProxies))
					r.Get("/ipban/summary", ipbanSummaryHandler(d.IPBan))
					r.Get("/ipban/hosts", ipbanHostsHandler(d.IPBan))
					r.Put("/ipban/hosts/{id}", ipbanUpdateHostHandler(d.IPBan))
					r.Get("/ipban/active", ipbanActiveHandler(d.IPBan))
					r.Get("/ipban/fleet", ipbanFleetHandler(d.IPBan))
					r.Post("/ipban/fleet", ipbanManualBanHandler(d.IPBan))
					r.Delete("/ipban/fleet/{ip}", ipbanFleetUnbanHandler(d.IPBan))
					r.Post("/ipban/unban", ipbanHostUnbanHandler(d.IPBan))
					r.Get("/ipban/events", ipbanEventsHandler(d.IPBan))
					r.Get("/ipban/stats", ipbanStatsHandler(d.IPBan))
				}

				if d.BackupTargets != nil {
					r.Get("/backup-targets", listBackupTargetsHandler(d.BackupTargets, d.BackupServer, d.BackupTLS, d.BackupSecretsSecure))
					r.Post("/backup-targets", createBackupTargetHandler(d.BackupTargets, d.BackupServer, d.BackupNodes, d.Hosts, d.BackupSecretsSecure))
					r.Patch("/backup-targets/{id}", updateBackupTargetHandler(d.BackupTargets, d.BackupServer))
					r.Post("/backup-targets/{id}/rotate", rotateBackupTargetHandler(d.BackupTargets))
					r.Post("/backup-targets/{id}/measure", measureBackupTargetHandler(d.BackupTargets, d.BackupServer))
					r.Post("/backup-targets/{id}/revoke", revokeBackupTargetHandler(d.BackupTargets))
					r.Delete("/backup-targets/{id}", deleteBackupTargetHandler(d.BackupTargets, d.BackupServer, d.BackupNodes))
					r.Get("/backup-repositories", listBackupTargetsHandler(d.BackupTargets, d.BackupServer, d.BackupTLS, d.BackupSecretsSecure))
					r.Post("/backup-repositories", createBackupTargetHandler(d.BackupTargets, d.BackupServer, d.BackupNodes, d.Hosts, d.BackupSecretsSecure))
					r.Patch("/backup-repositories/{id}", updateBackupTargetHandler(d.BackupTargets, d.BackupServer))
					r.Post("/backup-repositories/{id}/rotate", rotateBackupTargetHandler(d.BackupTargets))
					r.Post("/backup-repositories/{id}/measure", measureBackupTargetHandler(d.BackupTargets, d.BackupServer))
					r.Post("/backup-repositories/{id}/revoke", revokeBackupTargetHandler(d.BackupTargets))
					r.Put("/backup-repositories/{id}/credentials", updateDirectRepositoryCredentialsHandler(d.BackupTargets))
					r.Delete("/backup-repositories/{id}", deleteBackupTargetHandler(d.BackupTargets, d.BackupServer, d.BackupNodes))
					r.Get("/backup-destinations", listBackupDestinationsHandler(d.BackupTargets))
					r.Post("/backup-destinations", createBackupDestinationHandler(d.BackupTargets))
					r.Put("/backup-destinations/{id}/credentials", updateBackupDestinationCredentialsHandler(d.BackupTargets))
					r.Delete("/backup-destinations/{id}", deleteBackupDestinationHandler(d.BackupTargets))
					r.Get("/backup-tunnel", backupTunnelStatusHandler(d.BackupTunnel, d.TunnelPeers, d.TunnelInfo, nodePeerStats))
					r.Delete("/backup-tunnel/peers/{hostID}", revokeTunnelPeerHandler(d.BackupTunnel, d.TunnelPeers, nodePeerStats))
					r.Get("/backup-nodes", listBackupNodesHandler(d.BackupNodes, nodeHealth))
					r.Post("/backup-nodes", promoteBackupNodeHandler(d.BackupNodes, d.Hosts))
					r.Patch("/backup-nodes/{hostID}", updateBackupNodeHandler(d.BackupNodes))
					r.Delete("/backup-nodes/{hostID}", demoteBackupNodeHandler(d.BackupNodes))
				}
			})
		})
	})

	if d.BackupServer != nil && d.BackupPublic {
		r.Mount("/backup", d.BackupServer.Routes())
	}

	if d.WebHandler != nil {
		r.Mount("/", d.WebHandler)
	}

	return &Router{r}
}

func healthHandler(db *storage.DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Pool.Ping(ctx); err != nil {
			logger.Warn("healthz db ping failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "degraded",
				"checks": map[string]string{"db": "error"},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"checks": map[string]string{"db": "ok"},
		})
	}
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
