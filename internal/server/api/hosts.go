package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"servermonitor/internal/server/alerting"
	"servermonitor/internal/server/storage"
	"servermonitor/pkg/agentsig"
	"servermonitor/pkg/metrics"
	"servermonitor/pkg/restserver"
	"servermonitor/pkg/version"
)

type collectorStatusDTO struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type hostDTO struct {
	ID                    int64                         `json:"id"`
	Hostname              string                        `json:"hostname"`
	OS                    string                        `json:"os,omitempty"`
	Arch                  string                        `json:"arch,omitempty"`
	Kernel                string                        `json:"kernel,omitempty"`
	AgentVersion          string                        `json:"agent_version,omitempty"`
	LatestAgentVersion    string                        `json:"latest_agent_version,omitempty"`
	UpdateAvailable       bool                          `json:"update_available,omitempty"`
	AutoUpgrade           bool                          `json:"auto_upgrade"`
	SupportsRemoteUpgrade bool                          `json:"supports_remote_upgrade"`
	ExternallyManaged     bool                          `json:"externally_managed,omitempty"`
	UpgradeStalled        bool                          `json:"upgrade_stalled,omitempty"`
	UpgradePending        bool                          `json:"upgrade_pending,omitempty"`
	Upgrading             bool                          `json:"upgrading,omitempty"`
	SampleIntervalS       int                           `json:"sample_interval_s"`
	EnabledCollectors     []string                      `json:"enabled_collectors,omitempty"`
	CollectorStatus       map[string]collectorStatusDTO `json:"collector_status,omitempty"`
	Tags                  map[string]string             `json:"tags,omitempty"`
	LastSeenISO           string                        `json:"last_seen,omitempty"`
	CreatedAtISO          string                        `json:"created_at"`
	FiringAlerts          int                           `json:"firing_alerts,omitempty"`
	FiringSeverity        string                        `json:"firing_severity,omitempty"`
}

const minRemoteUpgradeVersion = "0.1.1"

const upgradeStallWindow = 15 * time.Minute

func supportsRemoteUpgrade(agentVersion string) bool {
	if agentVersion == "" {
		return false
	}
	return !version.IsNewer(minRemoteUpgradeVersion, agentVersion)
}

func toDTO(h storage.Host) hostDTO {
	updateAvailable := h.AgentVersion != "" && version.IsNewer(version.Version, h.AgentVersion)
	stalled := h.UpgradeStallSince != nil && time.Since(*h.UpgradeStallSince) >= upgradeStallWindow
	d := hostDTO{
		ID:                    h.ID,
		Hostname:              h.Hostname,
		OS:                    h.OS,
		Arch:                  h.Arch,
		Kernel:                h.Kernel,
		AgentVersion:          h.AgentVersion,
		LatestAgentVersion:    version.Version,
		UpdateAvailable:       updateAvailable,
		AutoUpgrade:           h.AutoUpgrade,
		SupportsRemoteUpgrade: supportsRemoteUpgrade(h.AgentVersion),
		ExternallyManaged:     h.ExternallyManaged,
		UpgradeStalled:        stalled,
		UpgradePending:        h.UpgradeRequestedAt != nil,
		Upgrading:             updateAvailable && !stalled && h.UpgradeDispatchedAt != nil,
		SampleIntervalS:       h.SampleIntervalS,
		EnabledCollectors:     h.EnabledCollectors,
		Tags:                  h.Tags,
		CreatedAtISO:          h.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if len(h.CollectorStatus) > 0 {
		d.CollectorStatus = make(map[string]collectorStatusDTO, len(h.CollectorStatus))
		for k, v := range h.CollectorStatus {
			d.CollectorStatus[k] = collectorStatusDTO{State: v.State, Message: v.Message}
		}
	}
	if h.LastSeen != nil {
		d.LastSeenISO = h.LastSeen.UTC().Format("2006-01-02T15:04:05Z")
	}
	return d
}

type firingSummary struct {
	Count    int
	Severity string
}

func loadFiringSummaries(ctx context.Context, pool *pgxpool.Pool) (map[int64]firingSummary, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.host_id, count(*), max(`+alerting.RankCaseSQL+`)
		FROM alert_states s
		JOIN alert_rules r ON r.id = s.rule_id
		WHERE s.state = 'firing' AND r.enabled
		GROUP BY s.host_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]firingSummary{}
	for rows.Next() {
		var hostID int64
		var count, sev int
		if err := rows.Scan(&hostID, &count, &sev); err != nil {
			return nil, err
		}
		out[hostID] = firingSummary{Count: count, Severity: alerting.SeverityName(sev)}
	}
	return out, rows.Err()
}

func loadFiringSummary(ctx context.Context, pool *pgxpool.Pool, hostID int64) (firingSummary, error) {
	var count, sev int
	err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(max(`+alerting.RankCaseSQL+`), 0)
		FROM alert_states s
		JOIN alert_rules r ON r.id = s.rule_id
		WHERE s.host_id = $1 AND s.state = 'firing' AND r.enabled
	`, hostID).Scan(&count, &sev)
	if err != nil {
		return firingSummary{}, err
	}
	return firingSummary{Count: count, Severity: alerting.SeverityName(sev)}, nil
}

func attachFiring(ctx context.Context, pool *pgxpool.Pool, dtos []*hostDTO) {
	if len(dtos) == 0 {
		return
	}
	if len(dtos) == 1 {
		f, err := loadFiringSummary(ctx, pool, dtos[0].ID)
		if err != nil {
			return
		}
		dtos[0].FiringAlerts = f.Count
		dtos[0].FiringSeverity = f.Severity
		return
	}
	firing, err := loadFiringSummaries(ctx, pool)
	if err != nil {
		return
	}
	for _, d := range dtos {
		if f, ok := firing[d.ID]; ok {
			d.FiringAlerts = f.Count
			d.FiringSeverity = f.Severity
		}
	}
}

func listHostsHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			all     []storage.Host
			listErr error
			firing  map[int64]firingSummary
			wg      sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			all, listErr = hosts.List(r.Context())
		}()
		go func() {
			defer wg.Done()
			firing, _ = loadFiringSummaries(r.Context(), db.Pool)
		}()
		wg.Wait()
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr.Error())
			return
		}
		out := make([]hostDTO, 0, len(all))
		for _, h := range all {
			d := toDTO(h)
			if f, ok := firing[h.ID]; ok {
				d.FiringAlerts = f.Count
				d.FiringSeverity = f.Severity
			}
			out = append(out, d)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func getHostHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h, err := hosts.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		d := toDTO(h)
		attachFiring(r.Context(), db.Pool, []*hostDTO{&d})
		writeJSON(w, http.StatusOK, d)
	}
}

type activeAlertDTO struct {
	RuleID   int32     `json:"rule_id"`
	RuleName string    `json:"rule_name"`
	Severity string    `json:"severity"`
	Metric   string    `json:"metric"`
	LabelKey string    `json:"label_key,omitempty"`
	Since    time.Time `json:"since"`
	Value    *float64  `json:"value,omitempty"`
}

func hostActiveAlertsHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if _, err := hosts.Get(r.Context(), id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		rows, err := db.Pool.Query(r.Context(), `
			SELECT s.rule_id, r.name, r.severity, r.metric, s.label_key, s.since, s.last_value
			FROM alert_states s
			JOIN alert_rules r ON r.id = s.rule_id
			WHERE s.host_id = $1 AND s.state = 'firing' AND r.enabled
			ORDER BY s.since DESC
			LIMIT 50
		`, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := []activeAlertDTO{}
		for rows.Next() {
			var a activeAlertDTO
			var metricID int16
			if err := rows.Scan(&a.RuleID, &a.RuleName, &a.Severity, &metricID, &a.LabelKey, &a.Since, &a.Value); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			a.Metric = metrics.ID(metricID).Meta().Name
			out = append(out, a)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type registerRequest struct {
	Hostname        string `json:"hostname"`
	SampleIntervalS int    `json:"sample_interval_s"`
}

type registerResponse struct {
	HostID          int64  `json:"host_id"`
	Token           string `json:"token"`
	SampleIntervalS int    `json:"sample_interval_s"`
	ServerPubkey    string `json:"server_pubkey,omitempty"`
}

func registerHostHandler(hosts *storage.Hosts, signer *agentsig.Signer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.Hostname == "" {
			writeError(w, http.StatusBadRequest, "hostname required")
			return
		}
		interval := req.SampleIntervalS
		if interval <= 0 {
			interval = 10
		}
		if interval < 1 || interval > 3600 {
			writeError(w, http.StatusBadRequest, "sample_interval_s must be between 1 and 3600")
			return
		}
		token, err := generateToken(32)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		id, err := hosts.Register(r.Context(), req.Hostname, token, interval)
		if err != nil {
			if errors.Is(err, storage.ErrHostnameTaken) {
				writeError(w, http.StatusConflict, "another live host already uses that hostname")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := registerResponse{HostID: id, Token: token, SampleIntervalS: interval}
		if signer != nil {
			resp.ServerPubkey = signer.PublicKeyHex()
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

type updateHostRequest struct {
	Hostname        *string `json:"hostname,omitempty"`
	SampleIntervalS *int    `json:"sample_interval_s,omitempty"`
	AutoUpgrade     *bool   `json:"auto_upgrade,omitempty"`
}

func updateHostHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		var req updateHostRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.Hostname != nil {
			name := strings.TrimSpace(*req.Hostname)
			if name == "" {
				writeError(w, http.StatusBadRequest, "hostname cannot be empty")
				return
			}
			req.Hostname = &name
		}
		if req.SampleIntervalS != nil {
			if *req.SampleIntervalS < 1 || *req.SampleIntervalS > 3600 {
				writeError(w, http.StatusBadRequest, "sample_interval_s must be between 1 and 3600")
				return
			}
		}
		if err := hosts.Update(r.Context(), id, storage.HostUpdate{
			Hostname:        req.Hostname,
			SampleIntervalS: req.SampleIntervalS,
			AutoUpgrade:     req.AutoUpgrade,
		}); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			if errors.Is(err, storage.ErrHostnameTaken) {
				writeError(w, http.StatusConflict, "another live host already uses that hostname")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h, err := hosts.Get(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		d := toDTO(h)
		attachFiring(r.Context(), db.Pool, []*hostDTO{&d})
		writeJSON(w, http.StatusOK, d)
	}
}

func requestHostUpgradeHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h, err := hosts.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if h.ExternallyManaged {
			writeError(w, http.StatusPreconditionFailed, "host is externally managed (containerized or read-only filesystem); upgrade by rebuilding its agent image, bumping SM_AGENT_IMAGE, and redeploying, not via remote upgrade")
			return
		}
		if !supportsRemoteUpgrade(h.AgentVersion) {
			writeError(w, http.StatusPreconditionFailed, "agent version "+h.AgentVersion+" does not support remote upgrade; manually re-install to "+minRemoteUpgradeVersion+" or later")
			return
		}
		if !version.IsNewer(version.Version, h.AgentVersion) {
			writeError(w, http.StatusConflict, "agent is already running the latest version")
			return
		}
		if err := hosts.RequestUpgrade(r.Context(), id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h, err = hosts.Get(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		d := toDTO(h)
		attachFiring(r.Context(), db.Pool, []*hostDTO{&d})
		writeJSON(w, http.StatusOK, d)
	}
}

func deleteHostHandler(hosts *storage.Hosts, tunnel *restserver.Tunnel, peers *storage.BackupTunnelStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := hosts.Delete(r.Context(), id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if tunnel != nil && peers != nil {
			if peer, peerErr := peers.GetPeer(r.Context(), id); peerErr == nil {
				_ = peers.DeletePeer(r.Context(), id)
				_ = tunnel.RemovePeer(peer.PublicKey)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func generateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
