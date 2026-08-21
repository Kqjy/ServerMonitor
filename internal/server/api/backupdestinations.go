package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"servermonitor/internal/server/secretbox"
	"servermonitor/internal/server/storage"
)

type backupS3CredentialsRequest struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
}

func (r backupS3CredentialsRequest) storage() storage.BackupS3Credentials {
	return storage.BackupS3Credentials{
		AccessKeyID:     strings.TrimSpace(r.AccessKeyID),
		SecretAccessKey: strings.TrimSpace(r.SecretAccessKey),
		SessionToken:    strings.TrimSpace(r.SessionToken),
	}
}

type backupDestinationView struct {
	ID                    int64  `json:"id"`
	Name                  string `json:"name"`
	Kind                  string `json:"kind"`
	Endpoint              string `json:"endpoint,omitempty"`
	Bucket                string `json:"bucket"`
	Region                string `json:"region,omitempty"`
	PrefixTemplate        string `json:"prefix_template"`
	UsePathStyle          bool   `json:"use_path_style"`
	CredentialsConfigured bool   `json:"credentials_configured"`
	RepositoryCount       int    `json:"repository_count"`
	DataPath              string `json:"data_path"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

func toBackupDestinationView(d storage.BackupDestination) backupDestinationView {
	return backupDestinationView{
		ID:                    d.ID,
		Name:                  d.Name,
		Kind:                  d.Kind,
		Endpoint:              d.Endpoint,
		Bucket:                d.Bucket,
		Region:                d.Region,
		PrefixTemplate:        d.PrefixTemplate,
		UsePathStyle:          d.UsePathStyle,
		CredentialsConfigured: d.CredentialsConfigured,
		RepositoryCount:       d.RepositoryCount,
		DataPath:              "Agent → S3 directly",
		CreatedAt:             d.CreatedAt.UTC().Format(timeFormat),
		UpdatedAt:             d.UpdatedAt.UTC().Format(timeFormat),
	}
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

type createBackupDestinationRequest struct {
	Name           string                      `json:"name"`
	Kind           string                      `json:"kind"`
	Endpoint       string                      `json:"endpoint,omitempty"`
	Bucket         string                      `json:"bucket"`
	Region         string                      `json:"region,omitempty"`
	PrefixTemplate string                      `json:"prefix_template,omitempty"`
	UsePathStyle   bool                        `json:"use_path_style,omitempty"`
	Credentials    *backupS3CredentialsRequest `json:"credentials,omitempty"`
}

func createBackupDestinationHandler(targets *storage.BackupTargets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createBackupDestinationRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		input := storage.BackupDestinationInput{
			Name:           req.Name,
			Kind:           req.Kind,
			Endpoint:       req.Endpoint,
			Bucket:         req.Bucket,
			Region:         req.Region,
			PrefixTemplate: req.PrefixTemplate,
			UsePathStyle:   req.UsePathStyle,
		}
		if req.Credentials != nil {
			input.Credentials = req.Credentials.storage()
		}
		id, err := targets.CreateDestination(r.Context(), input)
		if err != nil {
			backupDestinationMutationError(w, err)
			return
		}
		destination, err := targets.GetDestination(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, toBackupDestinationView(destination))
	}
}

func listBackupDestinationsHandler(targets *storage.BackupTargets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		destinations, err := targets.ListDestinations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]backupDestinationView, 0, len(destinations))
		for _, destination := range destinations {
			out = append(out, toBackupDestinationView(destination))
		}
		writeJSON(w, http.StatusOK, map[string]any{"destinations": out})
	}
}

func updateBackupDestinationCredentialsHandler(targets *storage.BackupTargets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		var req backupS3CredentialsRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := targets.SetDestinationCredentials(r.Context(), id, req.storage()); err != nil {
			backupDestinationMutationError(w, err)
			return
		}
		destination, err := targets.GetDestination(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toBackupDestinationView(destination))
	}
}

func updateDirectRepositoryCredentialsHandler(targets *storage.BackupTargets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseBackupTargetID(w, r)
		if !ok {
			return
		}
		target, err := targets.Get(r.Context(), id)
		if err != nil {
			backupTargetLookupError(w, err)
			return
		}
		if target.DestinationID == nil {
			writeError(w, http.StatusBadRequest, "repository is not a direct S3 repository")
			return
		}
		var req backupS3CredentialsRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := targets.SetDirectRepositoryCredentials(r.Context(), id, req.storage()); err != nil {
			backupDestinationMutationError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteBackupDestinationHandler(targets *storage.BackupTargets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if err := targets.DeleteDestination(r.Context(), id); err != nil {
			backupDestinationMutationError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func backupDestinationMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrBackupTargetNameTaken):
		writeError(w, http.StatusConflict, "a backup repository with that name already exists")
	case errors.Is(err, storage.ErrBackupDestinationNameTaken):
		writeError(w, http.StatusConflict, "a backup destination with that name already exists")
	case errors.Is(err, storage.ErrBackupDestinationInUse):
		writeError(w, http.StatusConflict, "destination still has repository namespaces; revoke and delete those records first")
	case errors.Is(err, storage.ErrBackupCredentialsMissing):
		writeError(w, http.StatusBadRequest, "configure destination credentials or provide scoped credentials for this repository")
	case errors.Is(err, secretbox.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "set BACKUP_SECRETS_KEY before storing managed backup credentials")
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "backup destination not found")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
