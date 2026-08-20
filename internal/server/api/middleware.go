package api

import (
	"context"
	"errors"
	"net/http"

	"servermonitor/internal/server/storage"
)

type ctxKey int

const (
	ctxHostID ctxKey = iota + 1
	ctxUser
	ctxActorSlot
)

func requireAgentToken(hosts *storage.Hosts) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get("X-Agent-Token")
			if token == "" {
				writeError(w, http.StatusUnauthorized, "missing agent token")
				return
			}
			id, err := hosts.ResolveToken(r.Context(), token)
			if err != nil {
				if errors.Is(err, storage.ErrTombstoned) {
					writeError(w, http.StatusGone, "host deregistered")
					return
				}
				if errors.Is(err, storage.ErrArchived) {
					writeError(w, http.StatusForbidden, "host archived")
					return
				}
				if errors.Is(err, storage.ErrNotFound) {
					writeError(w, http.StatusUnauthorized, "unknown agent token")
					return
				}
				writeError(w, http.StatusInternalServerError, "token lookup failed")
				return
			}
			ctx := context.WithValue(r.Context(), ctxHostID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func hostIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(ctxHostID).(int64)
	return id, ok
}
