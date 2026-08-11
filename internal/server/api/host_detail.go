package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"servermonitor/internal/server/storage"
)

func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

const (
	maxDetailSpan    = 31 * 24 * time.Hour
	maxDetailBuckets = 10000
)

func resolveDetailWindow(r *http.Request, host storage.Host) (from, to time.Time, step int, err error) {
	q := r.URL.Query()
	now := time.Now()
	from, err = parseTimeStrict(q.Get("from"), now.Add(-1*time.Hour))
	if err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("invalid from: %w", err)
	}
	to, err = parseTimeStrict(q.Get("to"), now)
	if err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("invalid to: %w", err)
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, 0, errors.New("to must be after from")
	}
	span := to.Sub(from)
	if span > maxDetailSpan {
		return time.Time{}, time.Time{}, 0, errors.New("span exceeds 31d limit")
	}
	step, _ = strconv.Atoi(q.Get("step"))
	if step <= 0 {
		step = chooseStep(span, host.SampleIntervalS)
	} else if host.SampleIntervalS > 0 && step < host.SampleIntervalS {
		step = host.SampleIntervalS
	}
	spanSec := int(span.Seconds())
	if step > 0 && spanSec/step > maxDetailBuckets {
		step = (spanSec + maxDetailBuckets - 1) / maxDetailBuckets
	}
	return from, to, step, nil
}

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

type portSnapshot struct {
	Proto   string    `json:"proto"`
	Addr    string    `json:"addr"`
	Port    int32     `json:"port"`
	PID     int32     `json:"pid,omitempty"`
	Process string    `json:"process,omitempty"`
	Time    time.Time `json:"time"`
}

type backupRepoSnapshot struct {
	Repo      string          `json:"repo"`
	UpdatedAt time.Time       `json:"updated_at"`
	Status    json.RawMessage `json:"status"`
}

type hostBackupsResponse struct {
	Repos []backupRepoSnapshot `json:"repos"`
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

func hostBackupsHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
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
		rows, err := db.Pool.Query(r.Context(), `
			SELECT repo, updated_at, payload
			FROM backup_status
			WHERE host_id = $1
			ORDER BY repo ASC
		`, hostID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := make([]backupRepoSnapshot, 0)
		for rows.Next() {
			var repo backupRepoSnapshot
			var payload []byte
			if err := rows.Scan(&repo.Repo, &repo.UpdatedAt, &payload); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			repo.Status = json.RawMessage(payload)
			out = append(out, repo)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, hostBackupsResponse{Repos: out})
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
	case "ports":
		switch dir {
		case "prev":
			query = `SELECT max(time) FROM ports WHERE host_id = $1 AND time < $2::timestamptz - INTERVAL '1 millisecond'`
		case "next":
			query = `SELECT min(time) FROM ports WHERE host_id = $1 AND time > $2::timestamptz + INTERVAL '1 millisecond'`
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

type processPoint struct {
	Ts     time.Time `json:"ts"`
	CPUPct float64   `json:"cpu_pct"`
	MemRSS int64     `json:"mem_rss"`
}

type processSeriesResp struct {
	HostID  int64          `json:"host_id"`
	PID     int32          `json:"pid"`
	Name    string         `json:"name,omitempty"`
	Cmdline string         `json:"cmdline,omitempty"`
	StepSec int            `json:"step_sec"`
	Points  []processPoint `json:"points"`
}

type containerPoint struct {
	Ts        time.Time `json:"ts"`
	CPUPct    float64   `json:"cpu_pct"`
	CPUMax    float64   `json:"cpu_max"`
	MemUsed   int64     `json:"mem_used"`
	MemMax    int64     `json:"mem_max"`
	RxRate    float64   `json:"rx_rate"`
	TxRate    float64   `json:"tx_rate"`
	IORateMax float64   `json:"io_rate_max"`
}

type containerSeriesResp struct {
	HostID  int64            `json:"host_id"`
	CID     string           `json:"cid"`
	Name    string           `json:"name,omitempty"`
	Image   string           `json:"image,omitempty"`
	StepSec int              `json:"step_sec"`
	Points  []containerPoint `json:"points"`
}

func hostProcessSeriesHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		pid64, err := strconv.ParseInt(chi.URLParam(r, "pid"), 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid pid")
			return
		}
		pid := int32(pid64)
		host, err := hosts.Get(r.Context(), hostID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		from, to, step, err := resolveDetailWindow(r, host)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		q := r.URL.Query()
		anchor, err := parseTimeStrict(q.Get("anchor"), to)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid anchor: "+err.Error())
			return
		}
		if anchor.Before(from) {
			anchor = from
		} else if anchor.After(to) {
			anchor = to
		}
		gapSec := host.SampleIntervalS * 3
		if gapSec < 60 {
			gapSec = 60
		}

		nameFilter := q.Get("name")
		latestArgs := []any{hostID, pid}
		latestNameClause := ""
		if nameFilter != "" {
			latestNameClause = " AND name = $3"
			latestArgs = append(latestArgs, nameFilter)
		}
		seriesArgs := []any{intervalString(gapSec), hostID, pid, from, to, anchor, intervalString(step)}
		seriesNameClause := ""
		if nameFilter != "" {
			seriesNameClause = " AND name = $8"
			seriesArgs = append(seriesArgs, nameFilter)
		}

		var name, cmdline string
		latestSQL := `
			SELECT COALESCE(name,''), COALESCE(cmdline,'')
			FROM processes
			WHERE host_id = $1 AND pid = $2` + latestNameClause + `
			ORDER BY time DESC
			LIMIT 1
		`
		if err := db.Pool.QueryRow(r.Context(), latestSQL, latestArgs...).Scan(&name, &cmdline); err != nil {
			if isCtxErr(err) {
				return
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		seriesSQL := `
			WITH lagged AS (
			  SELECT time, cpu_pct, mem_rss,
			         time - lag(time) OVER (ORDER BY time) AS dt
			  FROM processes
			  WHERE host_id = $2 AND pid = $3 AND time >= $4 AND time < $5` + seriesNameClause + `
			),
			gapped AS (
			  SELECT time, cpu_pct, mem_rss,
			         sum(CASE WHEN dt > $1::interval THEN 1 ELSE 0 END) OVER (ORDER BY time) AS run_id
			  FROM lagged
			),
			target AS (
			  SELECT run_id FROM gapped WHERE time <= $6 ORDER BY time DESC LIMIT 1
			)
			SELECT time_bucket($7::interval, time) AS b,
			       COALESCE(avg(cpu_pct), 0)::float8 AS cpu,
			       COALESCE(avg(mem_rss), 0)::bigint AS rss
			FROM gapped
			WHERE run_id = (SELECT run_id FROM target)
			GROUP BY b
			ORDER BY b ASC
		`
		rows, err := db.Pool.Query(r.Context(), seriesSQL, seriesArgs...)
		if err != nil {
			if isCtxErr(err) {
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		points := make([]processPoint, 0, 256)
		for rows.Next() {
			var p processPoint
			if err := rows.Scan(&p.Ts, &p.CPUPct, &p.MemRSS); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			points = append(points, p)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, processSeriesResp{
			HostID:  hostID,
			PID:     pid,
			Name:    name,
			Cmdline: cmdline,
			StepSec: step,
			Points:  points,
		})
	}
}

func hostContainerSeriesHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		cid := chi.URLParam(r, "cid")
		if cid == "" {
			writeError(w, http.StatusBadRequest, "invalid cid")
			return
		}
		host, err := hosts.Get(r.Context(), hostID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		from, to, step, err := resolveDetailWindow(r, host)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		var name, image string
		if err := db.Pool.QueryRow(r.Context(), `
			SELECT COALESCE(name,''), COALESCE(image,'')
			FROM containers
			WHERE host_id = $1 AND cid = $2
			ORDER BY time DESC
			LIMIT 1
		`, hostID, cid).Scan(&name, &image); err != nil {
			if isCtxErr(err) {
				return
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		rows, err := db.Pool.Query(r.Context(), `
			WITH samples AS (
			  SELECT time, cpu_pct, mem_used, rx_bytes, tx_bytes,
			         lag(rx_bytes) OVER s AS rx_prev,
			         lag(tx_bytes) OVER s AS tx_prev,
			         EXTRACT(EPOCH FROM (time - lag(time) OVER s)) AS dt
			  FROM containers
			  WHERE host_id = $2 AND cid = $3 AND time >= $4 AND time < $5
			  WINDOW s AS (ORDER BY time)
			),
			rated AS (
			  SELECT time, cpu_pct, mem_used, rx_bytes, tx_bytes,
			         CASE
			           WHEN rx_bytes IS NULL OR rx_prev IS NULL OR dt IS NULL OR rx_bytes < rx_prev THEN 0
			           ELSE (rx_bytes - rx_prev)::float8 / GREATEST(dt, 1)
			         END
			       + CASE
			           WHEN tx_bytes IS NULL OR tx_prev IS NULL OR dt IS NULL OR tx_bytes < tx_prev THEN 0
			           ELSE (tx_bytes - tx_prev)::float8 / GREATEST(dt, 1)
			         END AS io_rate
			  FROM samples
			),
			bucketed AS (
			  SELECT time_bucket($1::interval, time) AS b,
			         max(time) AS bmax,
			         COALESCE(avg(cpu_pct), 0)::float8 AS cpu,
			         COALESCE(max(cpu_pct), 0)::float8 AS cpu_max,
			         COALESCE(avg(mem_used), 0)::bigint AS mem,
			         COALESCE(max(mem_used), 0)::bigint AS mem_max,
			         COALESCE(max(io_rate), 0)::float8 AS io_rate_max,
			         last(rx_bytes, time) AS rx_last,
			         last(tx_bytes, time) AS tx_last
			  FROM rated
			  GROUP BY b
			)
			SELECT b, cpu, cpu_max, mem, mem_max,
			       CASE
			         WHEN rx_last IS NULL OR lag(rx_last) OVER w IS NULL THEN 0
			         WHEN rx_last < lag(rx_last) OVER w THEN 0
			         ELSE (rx_last - lag(rx_last) OVER w)::float8
			              / GREATEST(EXTRACT(EPOCH FROM (bmax - lag(bmax) OVER w)), 1)
			       END AS rx_rate,
			       CASE
			         WHEN tx_last IS NULL OR lag(tx_last) OVER w IS NULL THEN 0
			         WHEN tx_last < lag(tx_last) OVER w THEN 0
			         ELSE (tx_last - lag(tx_last) OVER w)::float8
			              / GREATEST(EXTRACT(EPOCH FROM (bmax - lag(bmax) OVER w)), 1)
			       END AS tx_rate,
			       io_rate_max
			FROM bucketed
			WINDOW w AS (ORDER BY b)
			ORDER BY b ASC
		`, intervalString(step), hostID, cid, from, to)
		if err != nil {
			if isCtxErr(err) {
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		points := make([]containerPoint, 0, 256)
		for rows.Next() {
			var p containerPoint
			if err := rows.Scan(&p.Ts, &p.CPUPct, &p.CPUMax, &p.MemUsed, &p.MemMax, &p.RxRate, &p.TxRate, &p.IORateMax); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			points = append(points, p)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, containerSeriesResp{
			HostID:  hostID,
			CID:     cid,
			Name:    name,
			Image:   image,
			StepSec: step,
			Points:  points,
		})
	}
}

func hostPortsHandler(db *storage.DB, hosts *storage.Hosts) http.HandlerFunc {
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
			resolved, err := resolveSnapshotAt(r.Context(), db, "ports", hostID, at, r.URL.Query().Get("dir"))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if resolved == nil {
				writeJSON(w, http.StatusOK, []portSnapshot{})
				return
			}
			at = *resolved
		default:
			writeError(w, http.StatusBadRequest, "invalid dir")
			return
		}
		rows, err := db.Pool.Query(r.Context(), `
			WITH latest AS (
			  SELECT DISTINCT ON (proto, addr, port)
			         time, proto, addr, port,
			         COALESCE(pid, 0) AS pid,
			         COALESCE(process, '') AS process
			  FROM ports
			  WHERE host_id = $1 AND time <= $2 AND time > $2 - INTERVAL '2 minutes'
			  ORDER BY proto, addr, port, time DESC
			)
			SELECT time, proto, addr, port, pid, process
			FROM latest
			ORDER BY port ASC, proto ASC, addr ASC
		`, hostID, at)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := make([]portSnapshot, 0, 64)
		for rows.Next() {
			var p portSnapshot
			if err := rows.Scan(&p.Time, &p.Proto, &p.Addr, &p.Port, &p.PID, &p.Process); err != nil {
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
