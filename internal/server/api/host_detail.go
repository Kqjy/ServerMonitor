package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"servermonitor/internal/server/storage"
)

type processSnapshot struct {
	PID      int32     `json:"pid"`
	Name     string    `json:"name"`
	User     string    `json:"user,omitempty"`
	Cmdline  string    `json:"cmdline,omitempty"`
	CPUPct   float32   `json:"cpu_pct"`
	MemRSS   int64     `json:"mem_rss"`
	Status   string    `json:"status,omitempty"`
	NThreads int32     `json:"nthreads,omitempty"`
	Time     time.Time `json:"time"`
}

type containerSnapshot struct {
	CID      string    `json:"cid"`
	Name     string    `json:"name"`
	Image    string    `json:"image,omitempty"`
	State    string    `json:"state"`
	CPUPct   float32   `json:"cpu_pct"`
	MemUsed  int64     `json:"mem_used"`
	MemLimit int64     `json:"mem_limit,omitempty"`
	RxBytes  int64     `json:"rx_bytes,omitempty"`
	TxBytes  int64     `json:"tx_bytes,omitempty"`
	Time     time.Time `json:"time"`
}

func hostProcessesHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if _, err := hosts.Get(r.Context(), hostID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		limit := 50
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 500 {
			limit = l
		}
		at := time.Now()
		if atParam := r.URL.Query().Get("at"); atParam != "" {
			parsed, err := time.Parse(time.RFC3339, atParam)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid at timestamp")
				return
			}
			at = parsed
		}
		switch r.URL.Query().Get("dir") {
		case "":
		case "prev", "next":
			resolved, err := resolveSnapshotAt(r.Context(), db, "processes", hostID, at, r.URL.Query().Get("dir"))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if resolved == nil {
				writeJSON(w, http.StatusOK, []processSnapshot{})
				return
			}
			at = *resolved
		default:
			writeError(w, http.StatusBadRequest, "invalid dir")
			return
		}
		rows, err := db.Pool.Query(r.Context(), `
			WITH latest AS (
			  SELECT DISTINCT ON (pid)
			    time, pid, name, COALESCE(user_,'') AS user_, COALESCE(cmdline,'') AS cmdline,
			    COALESCE(cpu_pct, 0) AS cpu_pct, COALESCE(mem_rss, 0) AS mem_rss,
			    COALESCE(status,'') AS status, COALESCE(nthreads,0) AS nthreads
			  FROM processes
			  WHERE host_id = $1 AND time <= $2 AND time > $2 - INTERVAL '2 minutes'
			  ORDER BY pid, time DESC
			)
			SELECT time, pid, name, user_, cmdline, cpu_pct, mem_rss, status, nthreads
			FROM latest
			ORDER BY cpu_pct DESC NULLS LAST, mem_rss DESC NULLS LAST
			LIMIT $3
		`, hostID, at, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := make([]processSnapshot, 0, limit)
		for rows.Next() {
			var p processSnapshot
			if err := rows.Scan(&p.Time, &p.PID, &p.Name, &p.User, &p.Cmdline,
				&p.CPUPct, &p.MemRSS, &p.Status, &p.NThreads); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func resolveSnapshotAt(ctx context.Context, db *storage.DB, table string, hostID int64, at time.Time, dir string) (*time.Time, error) {
	var query string
	switch table {
	case "processes":
		switch dir {
		case "prev":
			query = `SELECT max(time) FROM processes WHERE host_id = $1 AND time < $2::timestamptz - INTERVAL '1 millisecond'`
		case "next":
			query = `SELECT min(time) FROM processes WHERE host_id = $1 AND time > $2::timestamptz + INTERVAL '1 millisecond'`
		default:
			return nil, errors.New("invalid dir")
		}
	case "containers":
		switch dir {
		case "prev":
			query = `SELECT max(time) FROM containers WHERE host_id = $1 AND time < $2::timestamptz - INTERVAL '1 millisecond'`
		case "next":
			query = `SELECT min(time) FROM containers WHERE host_id = $1 AND time > $2::timestamptz + INTERVAL '1 millisecond'`
		default:
			return nil, errors.New("invalid dir")
		}
	default:
		return nil, errors.New("invalid table")
	}
	var resolved *time.Time
	if err := db.Pool.QueryRow(ctx, query, hostID, at).Scan(&resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

func hostContainersHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if _, err := hosts.Get(r.Context(), hostID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		at := time.Now()
		if atParam := r.URL.Query().Get("at"); atParam != "" {
			parsed, err := time.Parse(time.RFC3339, atParam)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid at timestamp")
				return
			}
			at = parsed
		}
		switch r.URL.Query().Get("dir") {
		case "":
		case "prev", "next":
			resolved, err := resolveSnapshotAt(r.Context(), db, "containers", hostID, at, r.URL.Query().Get("dir"))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if resolved == nil {
				writeJSON(w, http.StatusOK, []containerSnapshot{})
				return
			}
			at = *resolved
		default:
			writeError(w, http.StatusBadRequest, "invalid dir")
			return
		}
		rows, err := db.Pool.Query(r.Context(), `
			SELECT DISTINCT ON (cid)
			       time, cid, COALESCE(name,''), COALESCE(image,''), COALESCE(state,''),
			       COALESCE(cpu_pct,0), COALESCE(mem_used,0), COALESCE(mem_limit,0),
			       COALESCE(rx_bytes,0), COALESCE(tx_bytes,0)
			FROM containers
			WHERE host_id = $1 AND time <= $2 AND time > $2 - INTERVAL '5 minutes'
			ORDER BY cid, time DESC
		`, hostID, at)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := make([]containerSnapshot, 0, 32)
		for rows.Next() {
			var c containerSnapshot
			if err := rows.Scan(&c.Time, &c.CID, &c.Name, &c.Image, &c.State,
				&c.CPUPct, &c.MemUsed, &c.MemLimit, &c.RxBytes, &c.TxBytes); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func hostLabelsHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if _, err := hosts.Get(r.Context(), hostID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "key required")
			return
		}
		rows, err := db.Pool.Query(r.Context(), `
			SELECT DISTINCT labels ->> $2
			FROM metric_points
			WHERE host_id = $1 AND time > now() - INTERVAL '30 minutes'
			  AND labels ? $2
			ORDER BY 1
		`, hostID, key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if v != "" {
				out = append(out, v)
			}
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
