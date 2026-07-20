package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"servermonitor/internal/server/storage"
	"servermonitor/pkg/restserver"
)

type BackupTLSInfo struct {
	Mode   string `json:"mode"`
	Domain string `json:"domain,omitempty"`
	Secure bool   `json:"secure"`
}

type backupTargetView struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	HostID          *int64     `json:"host_id,omitempty"`
	Hostname        string     `json:"hostname,omitempty"`
	NodeHostID      *int64     `json:"node_host_id,omitempty"`
	NodeHostname    string     `json:"node_hostname,omitempty"`
	QuotaBytes      *int64     `json:"quota_bytes,omitempty"`
	UsedBytes       int64      `json:"used_bytes"`
	UsageMeasuredAt *time.Time `json:"usage_measured_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

type backupTargetsResponse struct {
	Configured bool                    `json:"configured"`
	Storage    *restserver.BackendInfo `json:"storage,omitempty"`
	TLS        BackupTLSInfo           `json:"tls"`
	Targets    []backupTargetView      `json:"targets"`
}

var backupTargetNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func validBackupTargetName(name string) bool {
	return name != "" && name != "." && name != ".." && backupTargetNameRE.MatchString(name)
}

func toBackupTargetView(t storage.BackupTarget) backupTargetView {
	return backupTargetView{
		ID:              t.ID,
		Name:            t.Name,
		HostID:          t.HostID,
		Hostname:        t.Hostname,
		NodeHostID:      t.NodeHostID,
		NodeHostname:    t.NodeHostname,
		QuotaBytes:      t.QuotaBytes,
		UsedBytes:       t.UsedBytes,
		UsageMeasuredAt: t.UsageMeasuredAt,
		CreatedAt:       t.CreatedAt,
		RevokedAt:       t.RevokedAt,
	}
}

func listBackupTargetsHandler(targets *storage.BackupTargets, server *restserver.Server, tlsInfo BackupTLSInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := backupTargetsResponse{Configured: server != nil, TLS: tlsInfo, Targets: []backupTargetView{}}
		if server != nil {
			backend := server.Backend()
			resp.Storage = &backend
		}
		rows, err := targets.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, t := range rows {
			resp.Targets = append(resp.Targets, toBackupTargetView(t))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

type createBackupTargetRequest struct {
	Name       string `json:"name"`
	HostID     *int64 `json:"host_id,omitempty"`
	QuotaBytes *int64 `json:"quota_bytes,omitempty"`
	NodeHostID *int64 `json:"node_host_id,omitempty"`
}

type backupCredentialResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func createBackupTargetHandler(targets *storage.BackupTargets, server *restserver.Server, nodes *storage.BackupNodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createBackupTargetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		name := strings.TrimSpace(req.Name)
		if !validBackupTargetName(name) {
			writeError(w, http.StatusBadRequest, "name must be non-empty and use only letters, digits, dot, dash or underscore")
			return
		}
		if req.QuotaBytes != nil && *req.QuotaBytes < 0 {
			writeError(w, http.StatusBadRequest, "quota_bytes must be >= 0")
			return
		}
		if req.NodeHostID != nil {
			if nodes == nil {
				writeError(w, http.StatusBadRequest, "backup nodes are not available")
				return
			}
			if _, err := nodes.Get(r.Context(), *req.NodeHostID); err != nil {
				writeError(w, http.StatusBadRequest, "node_host_id does not refer to a promoted backup node")
				return
			}
			if req.HostID == nil {
				writeError(w, http.StatusBadRequest, "node-hosted repositories require host_id (the host that backs up to it) so the node can admit its tunnel peer")
				return
			}
		} else if server == nil {
			writeError(w, http.StatusBadRequest, "this server has no storage backend (set BACKUP_DIR or BACKUP_S3_BUCKET) - pick a backup node destination instead")
			return
		}
		if server != nil && req.NodeHostID == nil {
			has, err := server.RepoHasObjects(r.Context(), name)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if has {
				writeError(w, http.StatusConflict, "a repository with that name already holds data on the backend; choose another name or reclaim it on the backend first")
				return
			}
		}
		secret, err := generateToken(32)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		id, err := targets.Create(r.Context(), name, req.HostID, req.QuotaBytes, secret, req.NodeHostID)
		if err != nil {
			if errors.Is(err, storage.ErrBackupTargetNameTaken) {
				writeError(w, http.StatusConflict, "a backup target with that name already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, backupCredentialResponse{ID: id, Name: name, Password: secret})
	}
}

type updateBackupTargetRequest struct {
	QuotaBytes *int64 `json:"quota_bytes"`
}

func updateBackupTargetHandler(targets *storage.BackupTargets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBackupTargetID(w, r)
		if !ok {
			return
		}
		var req updateBackupTargetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		quota, err := normalizeQuota(req.QuotaBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := targets.SetQuota(r.Context(), id, quota); err != nil {
			backupTargetLookupError(w, err)
			return
		}
		updated, err := targets.Get(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toBackupTargetView(updated))
	}
}

func normalizeQuota(v *int64) (*int64, error) {
	if v == nil {
		return nil, nil
	}
	if *v < 0 {
		return nil, errors.New("quota_bytes must be >= 0")
	}
	if *v == 0 {
		return nil, nil
	}
	return v, nil
}

func rotateBackupTargetHandler(targets *storage.BackupTargets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBackupTargetID(w, r)
		if !ok {
			return
		}
		existing, err := targets.Get(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		secret, err := generateToken(32)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := targets.Rotate(r.Context(), id, secret); err != nil {
			backupTargetLookupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, backupCredentialResponse{ID: id, Name: existing.Name, Password: secret})
	}
}

func revokeBackupTargetHandler(targets *storage.BackupTargets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBackupTargetID(w, r)
		if !ok {
			return
		}
		if err := targets.Revoke(r.Context(), id); err != nil {
			backupTargetLookupError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func backupTargetDeletable(storedUsedBytes int64, repoHasObjects, repoChecked, nodeHosted bool) bool {
	if nodeHosted {
		return false
	}
	if storedUsedBytes > 0 {
		return false
	}
	if repoChecked && repoHasObjects {
		return false
	}
	return true
}

func deleteBackupTargetHandler(targets *storage.BackupTargets, server *restserver.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBackupTargetID(w, r)
		if !ok {
			return
		}
		t, err := targets.Get(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		if t.NodeHostID != nil {
			writeError(w, http.StatusConflict, "node-hosted backup targets cannot be deleted because their storage namespace cannot be verified empty; revoke them instead")
			return
		}
		var repoHasObjects bool
		repoChecked := server != nil
		if repoChecked {
			repoHasObjects, err = server.RepoHasObjects(r.Context(), t.Name)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if !backupTargetDeletable(t.UsedBytes, repoHasObjects, repoChecked, false) {
			writeError(w, http.StatusConflict, "backup target still holds data; revoke it instead, then reclaim space on the storage backend")
			return
		}
		if err := targets.Delete(r.Context(), id); err != nil {
			backupTargetLookupError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func measureBackupTargetHandler(targets *storage.BackupTargets, server *restserver.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBackupTargetID(w, r)
		if !ok {
			return
		}
		t, err := targets.Get(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		if t.NodeHostID != nil {
			writeError(w, http.StatusBadRequest, "node-hosted repositories report usage from the node agent; nothing to measure here")
			return
		}
		if server == nil {
			writeError(w, http.StatusServiceUnavailable, "backup server not configured")
			return
		}
		used, err := server.RepoUsage(r.Context(), t.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := targets.SetUsage(r.Context(), id, used); err != nil {
			backupTargetLookupError(w, err)
			return
		}
		updated, err := targets.Get(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toBackupTargetView(updated))
	}
}

func parseBackupTargetID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func backupTargetLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "backup target not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
