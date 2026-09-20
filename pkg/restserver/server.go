package restserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const restV2ContentType = "application/vnd.x.restic.rest.v2"

const uploadIdleTimeout = 3 * time.Minute

type Registry interface {
	ResolveTarget(ctx context.Context, repo, secret string) (id int64, quotaBytes int64, usedBytes int64, ok bool, err error)
	ReserveUsage(ctx context.Context, id int64, delta int64) (bool, error)
	ReleaseUsage(ctx context.Context, id int64, delta int64) error
}

type Server struct {
	store    Store
	registry Registry
	maxBlob  int64
	logger   *slog.Logger
}

type target struct {
	id    int64
	quota int64
	used  int64
}

func New(store Store, registry Registry, maxBlob int64, logger *slog.Logger) *Server {
	if maxBlob <= 0 {
		maxBlob = 1 << 30
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{store: store, registry: registry, maxBlob: maxBlob, logger: logger}
}

func (s *Server) Backend() BackendInfo { return s.store.Backend() }

func (s *Server) RepoUsage(ctx context.Context, repo string) (int64, error) {
	return s.store.RepoUsage(ctx, repo)
}

func (s *Server) RepoHasObjects(ctx context.Context, repo string) (bool, error) {
	return s.store.RepoHasObjects(ctx, repo)
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{repo}", s.auth(s.handleCreateRepo))
	r.Post("/{repo}/", s.auth(s.handleCreateRepo))
	r.Delete("/{repo}", s.auth(s.forbid))
	r.Delete("/{repo}/", s.auth(s.forbid))

	r.Head("/{repo}/config", s.auth(s.handleHeadConfig))
	r.Get("/{repo}/config", s.auth(s.handleGetConfig))
	r.Post("/{repo}/config", s.auth(s.handlePostConfig))
	r.Delete("/{repo}/config", s.auth(s.forbid))

	r.Get("/{repo}/{type}/", s.auth(s.handleList))
	r.Head("/{repo}/{type}/{name}", s.auth(s.handleHead))
	r.Get("/{repo}/{type}/{name}", s.auth(s.handleGet))
	r.Post("/{repo}/{type}/{name}", s.auth(s.handlePost))
	r.Delete("/{repo}/{type}/{name}", s.auth(s.handleDelete))
	return r
}

func (s *Server) auth(next func(http.ResponseWriter, *http.Request, string, target)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repo := chi.URLParam(r, "repo")
		if err := validName(repo); err != nil {
			http.Error(w, "invalid repository", http.StatusBadRequest)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != repo {
			s.unauthorized(w)
			return
		}
		id, quota, used, ok, err := s.registry.ResolveTarget(r.Context(), repo, pass)
		if err != nil {
			s.logger.Warn("resolve backup target", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok {
			s.unauthorized(w)
			return
		}
		next(w, r, repo, target{id: id, quota: quota, used: used})
	}
}

func (s *Server) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="servermonitor-backup"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (s *Server) forbid(w http.ResponseWriter, r *http.Request, repo string, t target) {
	http.Error(w, "append-only: operation not permitted", http.StatusForbidden)
}

func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request, repo string, t target) {
	if err := s.store.EnsureRepo(r.Context(), repo); err != nil {
		s.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleHeadConfig(w http.ResponseWriter, r *http.Request, repo string, t target) {
	s.head(w, r, repo, configType, configType)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request, repo string, t target) {
	s.get(w, r, repo, configType, configType)
}

func (s *Server) handlePostConfig(w http.ResponseWriter, r *http.Request, repo string, t target) {
	s.post(w, r, repo, configType, configType, t)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request, repo string, t target) {
	typ := chi.URLParam(r, "type")
	if err := validType(typ); err != nil {
		http.Error(w, "invalid type", http.StatusNotFound)
		return
	}
	infos, err := s.store.List(r.Context(), repo, typ)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), restV2ContentType) {
		w.Header().Set("Content-Type", restV2ContentType)
		_ = json.NewEncoder(w).Encode(infos)
		return
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v1")
	_ = json.NewEncoder(w).Encode(names)
}

func (s *Server) handleHead(w http.ResponseWriter, r *http.Request, repo string, t target) {
	typ := chi.URLParam(r, "type")
	if err := validType(typ); err != nil {
		http.Error(w, "invalid type", http.StatusNotFound)
		return
	}
	s.head(w, r, repo, typ, chi.URLParam(r, "name"))
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, repo string, t target) {
	typ := chi.URLParam(r, "type")
	if err := validType(typ); err != nil {
		http.Error(w, "invalid type", http.StatusNotFound)
		return
	}
	s.get(w, r, repo, typ, chi.URLParam(r, "name"))
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request, repo string, t target) {
	typ := chi.URLParam(r, "type")
	if err := validType(typ); err != nil {
		http.Error(w, "invalid type", http.StatusNotFound)
		return
	}
	s.post(w, r, repo, typ, chi.URLParam(r, "name"), t)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, repo string, t target) {
	typ := chi.URLParam(r, "type")
	if err := validType(typ); err != nil {
		http.Error(w, "invalid type", http.StatusNotFound)
		return
	}
	if !deletableType(typ) {
		http.Error(w, "append-only: only locks may be removed", http.StatusForbidden)
		return
	}
	freed, err := s.store.DeleteLock(r.Context(), repo, chi.URLParam(r, "name"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	if freed > 0 {
		if releaseErr := s.registry.ReleaseUsage(context.WithoutCancel(r.Context()), t.id, freed); releaseErr != nil {
			s.logger.Warn("backup usage release after lock delete", "err", releaseErr)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) head(w http.ResponseWriter, r *http.Request, repo, typ, name string) {
	size, err := s.store.Stat(r.Context(), repo, typ, name)
	if err != nil {
		s.storeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, repo, typ, name string) {
	rc, _, err := s.store.Open(r.Context(), repo, typ, name)
	if err != nil {
		s.storeError(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, time.Time{}, rc)
}

func (s *Server) post(w http.ResponseWriter, r *http.Request, repo, typ, name string, t target) {
	if r.ContentLength < 0 {
		http.Error(w, "content-length required", http.StatusLengthRequired)
		return
	}
	if r.ContentLength > s.maxBlob {
		http.Error(w, "object too large", http.StatusRequestEntityTooLarge)
		return
	}
	reserved, err := s.registry.ReserveUsage(r.Context(), t.id, r.ContentLength)
	if err != nil {
		s.logger.Warn("backup usage reservation", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !reserved && (deletableType(typ) || !s.objectPresent(r.Context(), repo, typ, name)) {
		http.Error(w, "quota exceeded", http.StatusRequestEntityTooLarge)
		return
	}

	var body io.Reader = http.MaxBytesReader(w, r.Body, r.ContentLength)
	ctrl := http.NewResponseController(w)
	if deadlineErr := ctrl.SetReadDeadline(time.Now().Add(uploadIdleTimeout)); deadlineErr == nil {
		defer func() { _ = ctrl.SetReadDeadline(time.Time{}) }()
		body = &progressDeadlineReader{src: body, setDeadline: ctrl.SetReadDeadline, window: uploadIdleTimeout, extended: time.Now()}
	}

	createErr := s.store.Create(r.Context(), repo, typ, name, r.ContentLength, body)
	if createErr == nil {
		if !reserved {
			s.reserveAfterUnaccountedCommit(r, t.id)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if errors.Is(createErr, ErrExistsIdentical) {
		if reserved {
			s.releaseUsage(r, t.id, r.ContentLength)
		}
		_, _ = io.Copy(io.Discard, body)
		w.WriteHeader(http.StatusOK)
		return
	}

	var maxErr *http.MaxBytesError
	definitelyNotCommitted := errors.Is(createErr, ErrExists) || errors.Is(createErr, ErrExistsCorrupt) || errors.Is(createErr, ErrInvalidRef) || errors.As(createErr, &maxErr)
	if definitelyNotCommitted {
		if reserved {
			s.releaseUsage(r, t.id, r.ContentLength)
		}
	} else if reserved {
		s.logger.Warn("backup usage reservation retained after ambiguous store failure", "err", createErr)
	} else {
		s.logger.Error("possible unaccounted backup write after ambiguous store failure on a duplicate retry", "err", createErr, "bytes", r.ContentLength)
	}
	switch {
	case errors.Is(createErr, ErrExistsCorrupt):
		s.logger.Error("stored backup object does not match its own name; refusing idempotent retry", "repo", repo, "type", typ, "name", name, "err", createErr)
		http.Error(w, "object already exists", http.StatusForbidden)
	case errors.Is(createErr, ErrExists):
		http.Error(w, "object already exists", http.StatusForbidden)
	case maxErr != nil:
		http.Error(w, "object too large", http.StatusRequestEntityTooLarge)
	default:
		s.storeError(w, createErr)
	}
}

func (s *Server) objectPresent(ctx context.Context, repo, typ, name string) bool {
	_, err := s.store.Stat(ctx, repo, typ, name)
	return err == nil
}

func (s *Server) releaseUsage(r *http.Request, id, delta int64) {
	if err := s.registry.ReleaseUsage(context.WithoutCancel(r.Context()), id, delta); err != nil {
		s.logger.Warn("backup usage reservation release", "err", err)
	}
}

func (s *Server) reserveAfterUnaccountedCommit(r *http.Request, id int64) {
	ok, err := s.registry.ReserveUsage(context.WithoutCancel(r.Context()), id, r.ContentLength)
	if err != nil || !ok {
		s.logger.Error("backup object committed without a usage reservation; recorded usage now under-reports the store", "err", err, "bytes", r.ContentLength)
	}
}

type progressDeadlineReader struct {
	src         io.Reader
	setDeadline func(time.Time) error
	window      time.Duration
	extended    time.Time
}

func (p *progressDeadlineReader) Read(b []byte) (int, error) {
	n, err := p.src.Read(b)
	if n > 0 {
		if now := time.Now(); now.Sub(p.extended) > p.window/4 {
			if setErr := p.setDeadline(now.Add(p.window)); setErr == nil {
				p.extended = now
			}
		}
	}
	return n, err
}

func (s *Server) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrExists):
		http.Error(w, "object already exists", http.StatusForbidden)
	case errors.Is(err, ErrInvalidRef):
		http.Error(w, "invalid object reference", http.StatusBadRequest)
	default:
		s.logger.Warn("backup store error", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
