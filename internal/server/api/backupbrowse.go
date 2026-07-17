package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"servermonitor/internal/server/storage"
	"servermonitor/pkg/wire"
)

const (
	browseQueuedTTL  = 90 * time.Second
	browseRunningTTL = 6 * time.Minute
	browseResultTTL  = 10 * time.Minute
	browseGlobalCap  = 256
	browseHostCap    = 4
	maxBrowseBody    = 4 << 20
)

var (
	browseSnapshotPattern = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)
	errBrowseHostCap      = errors.New("too many backup browse jobs for this host")
)

type browseJob struct {
	ID       string
	HostID   int64
	Repo     string
	Snapshot string
	Path     string
	State    string
	Created  time.Time
	Picked   time.Time
	Finished time.Time
	Result   *wire.BackupBrowseResult
}

type browseStore struct {
	mu   sync.Mutex
	jobs map[string]*browseJob
	now  func() time.Time
}

type browseHostLookup interface {
	Get(context.Context, int64) (storage.Host, error)
}

func newBrowseStore() *browseStore {
	return &browseStore{jobs: map[string]*browseJob{}, now: time.Now}
}

func (s *browseStore) cleanupLocked(now time.Time) {
	for id, job := range s.jobs {
		switch job.State {
		case "queued":
			if now.Sub(job.Created) > browseQueuedTTL {
				result := wire.BackupBrowseResult{ErrorKind: "agent_unreachable", Error: "the agent has not picked this up yet - it may be offline, busy with an earlier browse, or running a pre-0.4.0 agent"}
				job.State = "failed"
				job.Finished = now
				job.Result = &result
			}
		case "running":
			if now.Sub(job.Picked) > browseRunningTTL {
				result := wire.BackupBrowseResult{ErrorKind: "timed_out", Error: "the agent did not finish the backup browse request in time"}
				job.State = "failed"
				job.Finished = now
				job.Result = &result
			}
		case "done", "failed":
			if !job.Finished.IsZero() && now.Sub(job.Finished) > browseResultTTL {
				delete(s.jobs, id)
			}
		}
	}
}

func (s *browseStore) makeRoomLocked() {
	if len(s.jobs) < browseGlobalCap {
		return
	}
	all := make([]*browseJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		all = append(all, job)
	}
	sort.Slice(all, func(i, j int) bool {
		iFinished := all[i].State == "done" || all[i].State == "failed"
		jFinished := all[j].State == "done" || all[j].State == "failed"
		if iFinished != jFinished {
			return iFinished
		}
		iTime := all[i].Created
		jTime := all[j].Created
		if iFinished && !all[i].Finished.IsZero() {
			iTime = all[i].Finished
		}
		if jFinished && !all[j].Finished.IsZero() {
			jTime = all[j].Finished
		}
		return iTime.Before(jTime)
	})
	delete(s.jobs, all[0].ID)
}

func (s *browseStore) create(hostID int64, repo, snapshot, path string) (*browseJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	active := 0
	var reusable *browseJob
	for _, job := range s.jobs {
		if job.HostID == hostID && job.Repo == repo && job.Snapshot == snapshot && job.Path == path && (job.State == "queued" || job.State == "running" || job.State == "done") {
			jobPriority := 0
			reusablePriority := 0
			if job.State == "done" {
				jobPriority = 1
			}
			if reusable != nil && reusable.State == "done" {
				reusablePriority = 1
			}
			if reusable == nil || jobPriority > reusablePriority || jobPriority == reusablePriority && (job.Created.After(reusable.Created) || job.Created.Equal(reusable.Created) && job.ID > reusable.ID) {
				reusable = job
			}
		}
		if job.HostID == hostID && (job.State == "queued" || job.State == "running") {
			active++
		}
	}
	if reusable != nil {
		copy := *reusable
		return &copy, nil
	}
	if active >= browseHostCap {
		return nil, errBrowseHostCap
	}
	s.makeRoomLocked()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	job := &browseJob{
		ID:       hex.EncodeToString(random),
		HostID:   hostID,
		Repo:     repo,
		Snapshot: snapshot,
		Path:     path,
		State:    "queued",
		Created:  now,
	}
	s.jobs[job.ID] = job
	copy := *job
	return &copy, nil
}

func (s *browseStore) HasPending(hostID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.now().UTC())
	for _, job := range s.jobs {
		if job.HostID == hostID && job.State == "queued" {
			return true
		}
	}
	return false
}

func (s *browseStore) pick(hostID int64) []wire.BackupBrowseJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	queued := make([]*browseJob, 0)
	for _, job := range s.jobs {
		if job.HostID == hostID && job.State == "queued" {
			queued = append(queued, job)
		}
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].Created.Before(queued[j].Created) })
	out := make([]wire.BackupBrowseJob, 0, len(queued))
	for _, job := range queued {
		job.State = "running"
		job.Picked = now
		out = append(out, wire.BackupBrowseJob{ID: job.ID, Repo: job.Repo, Snapshot: job.Snapshot, Path: job.Path})
	}
	return out
}

func (s *browseStore) finish(hostID int64, id string, result wire.BackupBrowseResult) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	job, ok := s.jobs[id]
	lateTimedOut := ok && job.State == "failed" && job.Result != nil && job.Result.ErrorKind == "timed_out"
	if !ok || job.HostID != hostID || job.State != "running" && !lateTimedOut {
		return false
	}
	job.Result = &result
	job.Finished = now
	job.State = "done"
	if result.Error != "" {
		job.State = "failed"
	}
	return true
}

func (s *browseStore) get(hostID int64, id string) (*browseJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.now().UTC())
	job, ok := s.jobs[id]
	if !ok || job.HostID != hostID {
		return nil, false
	}
	copy := *job
	if job.Result != nil {
		result := *job.Result
		result.Entries = append([]wire.BackupBrowseEntry(nil), job.Result.Entries...)
		copy.Result = &result
	}
	return &copy, true
}

func validateBackupBrowse(repo, snapshot, path string) error {
	if strings.TrimSpace(repo) == "" {
		return errors.New("repo is required")
	}
	if snapshot != "latest" && !browseSnapshotPattern.MatchString(snapshot) {
		return errors.New("snapshot must be latest or 4 to 64 hexadecimal characters")
	}
	if len(path) == 0 || len(path) > 4096 || path[0] != '/' || strings.ContainsRune(path, 0) {
		return errors.New("path must be an absolute snapshot path of at most 4096 bytes")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return errors.New("path must not contain a .. segment")
		}
	}
	return nil
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing data after json")
		}
		return err
	}
	return nil
}

func browseHostID(r *http.Request, hosts browseHostLookup) (int64, int, error) {
	hostID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return 0, http.StatusBadRequest, errors.New("invalid id")
	}
	if _, err := hosts.Get(r.Context(), hostID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return 0, http.StatusNotFound, errors.New("host not found")
		}
		return 0, http.StatusInternalServerError, err
	}
	return hostID, 0, nil
}

func createBackupBrowseHandler(store *browseStore, db *storage.DB, hosts browseHostLookup) http.HandlerFunc {
	return createBackupBrowseHandlerWithRepoLookup(store, hosts, func(ctx context.Context, hostID int64, repo string) (bool, error) {
		var exists int
		err := db.Pool.QueryRow(ctx, `SELECT 1 FROM backup_status WHERE host_id=$1 AND repo=$2`, hostID, repo).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	})
}

func createBackupBrowseHandlerWithRepoLookup(store *browseStore, hosts browseHostLookup, repoExists func(context.Context, int64, string) (bool, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, status, err := browseHostID(r, hosts)
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
		var body struct {
			Repo     string `json:"repo"`
			Snapshot string `json:"snapshot"`
			Path     string `json:"path"`
		}
		if err := decodeStrictJSON(w, r, 16<<10, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if err := validateBackupBrowse(body.Repo, body.Snapshot, body.Path); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		exists, err := repoExists(r.Context(), hostID, body.Repo)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "backup repo lookup failed")
			return
		}
		if !exists {
			writeError(w, http.StatusNotFound, "backup repo not found")
			return
		}
		job, err := store.create(hostID, body.Repo, body.Snapshot, body.Path)
		if errors.Is(err, errBrowseHostCap) {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "create backup browse job failed")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID})
	}
}

func getBackupBrowseJobHandler(store *browseStore, hosts browseHostLookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, status, err := browseHostID(r, hosts)
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
		job, ok := store.get(hostID, chi.URLParam(r, "jobID"))
		if !ok {
			writeError(w, http.StatusNotFound, "backup browse job not found")
			return
		}
		response := struct {
			Status string                   `json:"status"`
			Result *wire.BackupBrowseResult `json:"result,omitempty"`
		}{Status: job.State, Result: job.Result}
		writeJSON(w, http.StatusOK, response)
	}
}

func agentBackupBrowseJobsHandler(store *browseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, ok := hostIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no host")
			return
		}
		jobs := store.pick(hostID)
		if len(jobs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, wire.BackupBrowseJobs{Jobs: jobs})
	}
}

func agentBackupBrowseResultHandler(store *browseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, ok := hostIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no host")
			return
		}
		var result wire.BackupBrowseResult
		if err := decodeStrictJSON(w, r, maxBrowseBody, &result); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if !store.finish(hostID, chi.URLParam(r, "jobID"), result) {
			writeError(w, http.StatusNotFound, "running backup browse job not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
