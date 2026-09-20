package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"servermonitor/internal/server/secretbox"
	"servermonitor/internal/server/storage"
	"servermonitor/pkg/restserver"
	"servermonitor/pkg/secretenvelope"
	"servermonitor/pkg/wire"
)

type BackupTLSInfo struct {
	Mode   string `json:"mode"`
	Domain string `json:"domain,omitempty"`
	Secure bool   `json:"secure"`
}

type backupTargetView struct {
	ID                  int64                         `json:"id"`
	Name                string                        `json:"name"`
	HostID              *int64                        `json:"host_id,omitempty"`
	Hostname            string                        `json:"hostname,omitempty"`
	NodeHostID          *int64                        `json:"node_host_id,omitempty"`
	NodeHostname        string                        `json:"node_hostname,omitempty"`
	DestinationID       *int64                        `json:"destination_id,omitempty"`
	DestinationName     string                        `json:"destination_name,omitempty"`
	DestinationKind     string                        `json:"destination_kind,omitempty"`
	NamespacePrefix     string                        `json:"namespace_prefix,omitempty"`
	Managed             bool                          `json:"managed"`
	CredentialScope     string                        `json:"credential_scope,omitempty"`
	StorageBackend      backupStorageBackendView      `json:"storage_backend"`
	RepositoryNamespace backupRepositoryNamespaceView `json:"repository_namespace"`
	DataPath            backupDataPathView            `json:"data_path"`
	QuotaBytes          *int64                        `json:"quota_bytes,omitempty"`
	UsedBytes           int64                         `json:"used_bytes"`
	UsageMeasuredAt     *time.Time                    `json:"usage_measured_at,omitempty"`
	CreatedAt           time.Time                     `json:"created_at"`
	RevokedAt           *time.Time                    `json:"revoked_at,omitempty"`
}

type backupStorageBackendView struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Location string `json:"location,omitempty"`
}

type backupRepositoryNamespaceView struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix,omitempty"`
}

type backupDataPathView struct {
	Kind      string   `json:"kind"`
	Label     string   `json:"label"`
	Hops      []string `json:"hops"`
	Transport string   `json:"transport,omitempty"`
}

type backupTargetsResponse struct {
	Configured     bool                     `json:"configured"`
	Storage        *restserver.BackendInfo  `json:"storage,omitempty"`
	TLS            BackupTLSInfo            `json:"tls"`
	ManagedSecrets backupManagedSecretsView `json:"managed_secrets"`
	Targets        []backupTargetView       `json:"targets"`
	Repositories   []backupTargetView       `json:"repositories"`
	Destinations   []backupDestinationView  `json:"destinations"`
}

type backupManagedSecretsView struct {
	KeyConfigured  bool   `json:"key_configured"`
	SecureDelivery bool   `json:"secure_delivery"`
	Blocker        string `json:"blocker,omitempty"`
}

func managedSecretsView(targets *storage.BackupTargets, secureDelivery bool) backupManagedSecretsView {
	view := backupManagedSecretsView{KeyConfigured: targets.SecretsConfigured(), SecureDelivery: secureDelivery}
	switch {
	case !view.KeyConfigured:
		view.Blocker = "BACKUP_SECRETS_KEY is not set on the server, so S3 credentials cannot be stored encrypted."
	case !view.SecureDelivery:
		view.Blocker = "Direct S3 credentials are delivered to agents over HTTPS only; this server is reachable over plain HTTP."
	}
	return view
}

var backupTargetNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func validBackupTargetName(name string) bool {
	return name != "" && name != "." && name != ".." && backupTargetNameRE.MatchString(name)
}

func toBackupTargetView(t storage.BackupTarget, server *restserver.Server) backupTargetView {
	view := backupTargetView{
		ID:                  t.ID,
		Name:                t.Name,
		HostID:              t.HostID,
		Hostname:            t.Hostname,
		NodeHostID:          t.NodeHostID,
		NodeHostname:        t.NodeHostname,
		DestinationID:       t.DestinationID,
		DestinationName:     t.DestinationName,
		DestinationKind:     t.DestinationKind,
		NamespacePrefix:     t.NamespacePrefix,
		Managed:             t.DestinationID != nil,
		RepositoryNamespace: backupRepositoryNamespaceView{Name: t.Name, Prefix: t.NamespacePrefix},
		QuotaBytes:          t.QuotaBytes,
		UsedBytes:           t.UsedBytes,
		UsageMeasuredAt:     t.UsageMeasuredAt,
		CreatedAt:           t.CreatedAt,
		RevokedAt:           t.RevokedAt,
	}
	switch {
	case t.DestinationID != nil:
		view.CredentialScope = "destination"
		if t.DirectCredentialsScoped {
			view.CredentialScope = "repository"
		}
		view.StorageBackend = backupStorageBackendView{Kind: "external_s3", Label: "External S3-compatible object storage", Location: t.DestinationName}
		view.DataPath = backupDataPathView{Kind: "agent_to_s3_direct", Label: "Agent → S3 directly", Hops: []string{"Agent", "S3"}, Transport: "s3_api"}
	case t.NodeHostID != nil:
		view.StorageBackend = backupStorageBackendView{Kind: "storage_node_disk", Label: "Storage node disk", Location: t.NodeHostname}
		view.DataPath = backupDataPathView{Kind: "agent_to_storage_node", Label: "Agent → storage node", Hops: []string{"Agent", "storage node"}, Transport: "wireguard_udp"}
	case server != nil && server.Backend().Kind == "s3":
		backend := server.Backend()
		view.StorageBackend = backupStorageBackendView{Kind: "server_gateway_s3", Label: "S3-compatible object storage behind ServerMonitor", Location: backend.Location}
		view.DataPath = backupDataPathView{Kind: "agent_via_server_to_s3", Label: "Agent → ServerMonitor → S3", Hops: []string{"Agent", "ServerMonitor", "S3"}}
	case server != nil:
		backend := server.Backend()
		view.StorageBackend = backupStorageBackendView{Kind: "server_disk", Label: "Server disk", Location: backend.Location}
		view.DataPath = backupDataPathView{Kind: "agent_to_server_disk", Label: "Agent → Server disk", Hops: []string{"Agent", "Server disk"}}
	default:
		view.StorageBackend = backupStorageBackendView{Kind: "unavailable", Label: "Legacy server destination (backend unavailable)"}
		view.DataPath = backupDataPathView{Kind: "agent_to_server_unavailable", Label: "Agent → ServerMonitor (backend unavailable)", Hops: []string{"Agent", "ServerMonitor"}}
	}
	return view
}

func listBackupTargetsHandler(targets *storage.BackupTargets, server *restserver.Server, tlsInfo BackupTLSInfo, secureSecretDelivery bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := backupTargetsResponse{Configured: server != nil, TLS: tlsInfo, ManagedSecrets: managedSecretsView(targets, secureSecretDelivery), Targets: []backupTargetView{}, Repositories: []backupTargetView{}, Destinations: []backupDestinationView{}}
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
			view := toBackupTargetView(t, server)
			resp.Targets = append(resp.Targets, view)
			resp.Repositories = append(resp.Repositories, view)
		}
		destinations, err := targets.ListDestinations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, destination := range destinations {
			resp.Destinations = append(resp.Destinations, toBackupDestinationView(destination))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

type createBackupTargetRequest struct {
	Name          string                      `json:"name"`
	HostID        *int64                      `json:"host_id,omitempty"`
	QuotaBytes    *int64                      `json:"quota_bytes,omitempty"`
	NodeHostID    *int64                      `json:"node_host_id,omitempty"`
	DestinationID *int64                      `json:"destination_id,omitempty"`
	S3Credentials *backupS3CredentialsRequest `json:"s3_credentials,omitempty"`
}

const (
	directRepoNameConflict = "this host already backs up a repository with that name; the agent refuses a configuration holding two repositories of the same name, which would stop every backup on the host. Pick another name, or remove the existing repository from the host's backup.toml first"
	nodeRepoNameConflict   = "this host already backs up a repository with that name. Storage nodes never delete blobs, so a new repository under that name would bind onto whatever the previous one left on the node: the host still holds the matching backup.key, restic would decrypt the surviving config instead of initializing a fresh repository, and the agent also refuses a configuration holding two repositories of the same name. Pick another name, or remove the repository from the host's backup.toml and delete its directory under the node's store_dir first"
)

type backupRepoNameReporter interface {
	HostReportsRepo(context.Context, int64, string) (bool, error)
}

func refuseRepoNameHostAlreadyBacksUp(w http.ResponseWriter, r *http.Request, targets backupRepoNameReporter, hostID int64, name, conflict string) bool {
	reported, err := targets.HostReportsRepo(r.Context(), hostID, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return true
	}
	if reported {
		writeError(w, http.StatusConflict, conflict)
		return true
	}
	return false
}

type backupCredentialResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	Managed  bool   `json:"managed,omitempty"`
	DataPath string `json:"data_path,omitempty"`
}

func createBackupTargetHandler(targets *storage.BackupTargets, server *restserver.Server, nodes *storage.BackupNodes, hosts *storage.Hosts, secureSecretDelivery bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createBackupTargetRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
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
		if req.NodeHostID != nil && req.DestinationID != nil {
			writeError(w, http.StatusBadRequest, "choose exactly one destination")
			return
		}
		if req.DestinationID != nil {
			if !secureSecretDelivery {
				writeError(w, http.StatusServiceUnavailable, "direct S3 credential delivery requires HTTPS (native TLS, ACME, or TRUST_PROXY_TLS=1)")
				return
			}
			if req.HostID == nil {
				writeError(w, http.StatusBadRequest, "direct S3 repositories require host_id")
				return
			}
			if req.QuotaBytes != nil && *req.QuotaBytes > 0 {
				writeError(w, http.StatusBadRequest, "direct S3 quotas must be configured at the object-storage provider")
				return
			}
			destination, err := targets.GetDestination(r.Context(), *req.DestinationID)
			if err != nil {
				backupTargetLookupError(w, err)
				return
			}
			host, err := hosts.Get(r.Context(), *req.HostID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "host_id does not refer to an active host")
				return
			}
			if refuseRepoNameHostAlreadyBacksUp(w, r, targets, host.ID, name, directRepoNameConflict) {
				return
			}
			prefix, err := storage.ExpandDirectPrefix(destination.PrefixTemplate, host.ID, host.Hostname, name)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			secret, err := generateToken(32)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			credentials := storage.BackupS3Credentials{}
			if req.S3Credentials != nil {
				credentials = req.S3Credentials.storage()
			}
			id, err := targets.CreateDirectRepository(r.Context(), name, host.ID, req.QuotaBytes, secret, prefix, destination.ID, credentials)
			if err != nil {
				backupDestinationMutationError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, backupCredentialResponse{ID: id, Name: name, Managed: true, DataPath: "Agent → S3 directly"})
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
			if refuseRepoNameHostAlreadyBacksUp(w, r, targets, *req.HostID, name, nodeRepoNameConflict) {
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
				writeError(w, http.StatusConflict, "a backup repository with that name already exists")
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

func updateBackupTargetHandler(targets *storage.BackupTargets, server *restserver.Server) http.HandlerFunc {
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
		existing, err := targets.Get(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		if existing.DestinationID != nil {
			writeError(w, http.StatusBadRequest, "direct S3 quotas are enforced by the object-storage provider, not ServerMonitor")
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
		writeJSON(w, http.StatusOK, toBackupTargetView(updated, server))
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
		if existing.DestinationID != nil {
			writeError(w, http.StatusBadRequest, "direct S3 repositories use managed S3 credentials; update those credentials instead of rotating a gateway password")
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

type backupTargetDeleteStore interface {
	Get(context.Context, int64) (storage.BackupTarget, error)
	Delete(context.Context, int64) error
}

type backupNodeDeleteStore interface {
	Get(context.Context, int64) (storage.BackupNode, error)
}

func deleteBackupTargetHandler(targets *storage.BackupTargets, server *restserver.Server, nodes *storage.BackupNodes) http.HandlerFunc {
	if nodes == nil {
		return deleteBackupTargetHandlerWithStores(targets, server, nil)
	}
	return deleteBackupTargetHandlerWithStores(targets, server, nodes)
}

func deleteBackupTargetHandlerWithStores(targets backupTargetDeleteStore, server *restserver.Server, nodes backupNodeDeleteStore) http.HandlerFunc {
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
		if t.DestinationID != nil {
			if t.RevokedAt == nil {
				writeError(w, http.StatusConflict, "revoke the direct repository before deleting its control-plane record; S3 objects are never deleted by this action")
				return
			}
			if err := targets.Delete(r.Context(), id); err != nil {
				backupTargetLookupError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if t.NodeHostID != nil {
			if nodes == nil {
				writeError(w, http.StatusInternalServerError, "backup nodes are not available")
				return
			}
			node, nodeErr := nodes.Get(r.Context(), *t.NodeHostID)
			if nodeErr != nil && !errors.Is(nodeErr, storage.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, nodeErr.Error())
				return
			}
			if nodeErr == nil && !node.HostMissing && !node.Archived {
				writeError(w, http.StatusConflict, "node-hosted backup repositories cannot be deleted while their storage node is active or offline; revoke them instead, or delete after the node is demoted, archived, or removed")
				return
			}
			if err := targets.Delete(r.Context(), id); err != nil {
				backupTargetLookupError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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
			writeError(w, http.StatusConflict, "backup repository still holds data; revoke it instead, then reclaim space on the storage backend")
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
		if t.DestinationID != nil {
			writeError(w, http.StatusBadRequest, "direct S3 usage is measured by the object-storage provider; backup traffic does not pass through ServerMonitor")
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
		writeJSON(w, http.StatusOK, toBackupTargetView(updated, server))
	}
}

func managedBackupConfigHandler(targets *storage.BackupTargets, secureSecretDelivery bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, ok := hostIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no host")
			return
		}
		repositories, err := targets.ManagedRepositoriesForHost(r.Context(), hostID)
		if err != nil {
			if errors.Is(err, secretbox.ErrNotConfigured) {
				writeError(w, http.StatusServiceUnavailable, "managed backup secrets are unavailable; configure BACKUP_SECRETS_KEY")
				return
			}
			writeError(w, http.StatusInternalServerError, "managed backup configuration is unavailable")
			return
		}
		if len(repositories) > 0 && !secureSecretDelivery {
			writeError(w, http.StatusServiceUnavailable, "managed backup credentials are not delivered over plaintext HTTP")
			return
		}
		resp := wire.ManagedBackupConfig{Repositories: []wire.ManagedBackupRepository{}}
		for _, repository := range repositories {
			if repository.Version > resp.Version {
				resp.Version = repository.Version
			}
			resp.Repositories = append(resp.Repositories, wire.ManagedBackupRepository{
				ID:              repository.ID,
				Name:            repository.Name,
				URL:             repository.URL,
				S3Region:        repository.S3Region,
				S3PathStyle:     repository.S3PathStyle,
				AccessKeyID:     repository.Credentials.AccessKeyID,
				SecretAccessKey: repository.Credentials.SecretAccessKey,
				SessionToken:    repository.Credentials.SessionToken,
			})
		}
		plaintext, err := json.Marshal(resp)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "managed backup configuration is unavailable")
			return
		}
		envelope, err := secretenvelope.Seal(r.Header.Get("X-Agent-Token"), secretenvelope.ManagedBackupConfigPurpose, plaintext)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "managed backup configuration is unavailable")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, envelope)
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
		writeError(w, http.StatusNotFound, "backup repository not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
