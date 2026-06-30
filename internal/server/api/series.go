package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"servermonitor/internal/server/archive"
	"servermonitor/internal/server/storage"
	"servermonitor/pkg/metrics"
)

type seriesPoint struct {
	Ts time.Time `json:"ts"`
	V  float64   `json:"v"`
}

type seriesResp struct {
	HostID  int64         `json:"host_id"`
	Metric  string        `json:"metric"`
	Unit    string        `json:"unit"`
	StepSec int           `json:"step_sec"`
	Points  []seriesPoint `json:"points"`
}

type seriesEntry struct {
	Labels map[string]string `json:"labels"`
	Points []seriesPoint     `json:"points"`
}

type multiSeriesResp struct {
	HostID  int64         `json:"host_id"`
	Metric  string        `json:"metric"`
	Unit    string        `json:"unit"`
	StepSec int           `json:"step_sec"`
	SplitBy string        `json:"split_by,omitempty"`
	Series  []seriesEntry `json:"series"`
}

type bucketAcc struct {
	sum float64
	n   int64
}

type labelGroup struct {
	labels  map[string]string
	buckets map[int64]*bucketAcc
}

const (
	caStepMin      = 300
	rollupMinRange = 12 * time.Hour
	rollupLiveTail = 15 * time.Minute
)

func rollupBoundary(now, from, to time.Time, step *int) time.Time {
	if to.Sub(from) < rollupMinRange {
		return time.Time{}
	}
	if *step < caStepMin {
		*step = caStepMin
	}
	b := now.Add(-rollupLiveTail).Unix()
	b -= b % int64(*step)
	return time.Unix(b, 0).UTC()
}

func coldRawClamp(from, rawAvail, coldCutoff, rawStart time.Time) (time.Time, time.Time, bool) {
	lo := from
	if lo.Before(rawAvail) {
		lo = rawAvail
	}
	hi := coldCutoff
	if hi.After(rawStart) {
		hi = rawStart
	}
	return lo, hi, hi.After(lo)
}

func coldRawGaps(lo, hi time.Time, covered []archive.Interval) [][2]time.Time {
	step := time.Duration(caStepMin) * time.Second
	gaps := [][2]time.Time{}
	cursor := lo
	for _, c := range covered {
		gapHi := c.From
		if gapHi.After(hi) {
			gapHi = hi
		}
		if gapHi.After(cursor) {
			gaps = append(gaps, [2]time.Time{cursor, gapHi})
		}
		if covEnd := c.To.Add(step); covEnd.After(cursor) {
			cursor = covEnd
		}
	}
	if hi.After(cursor) {
		gaps = append(gaps, [2]time.Time{cursor, hi})
	}
	return gaps
}

func queryRows(ctx context.Context, db *storage.DB, sql string, args []any, scan func(pgx.Rows) error) error {
	rows, err := db.Pool.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func seriesHandler(db *storage.DB, hosts *storage.Hosts, ar *archive.Archiver, ret RetentionConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		hostIDs := q.Get("host")
		metricName := q.Get("metric")
		if hostIDs == "" || metricName == "" {
			writeError(w, http.StatusBadRequest, "host and metric are required")
			return
		}
		hostID, err := strconv.ParseInt(hostIDs, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid host")
			return
		}
		metricID, ok := metrics.ByName(metricName)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown metric")
			return
		}

		now := time.Now()
		from := parseTime(q.Get("from"), now.Add(-1*time.Hour))
		to := parseTime(q.Get("to"), now)
		if !to.After(from) {
			writeError(w, http.StatusBadRequest, "to must be after from")
			return
		}
		rawRetention := ret.RawCutoff
		coldRetention := ret.Aggregate5mCutoff

		labelSel := q.Get("labels")
		labelArg := []byte("{}")
		var labelMap map[string]string
		if labelSel != "" {
			labelArg = []byte(labelSel)
			if err := json.Unmarshal(labelArg, &labelMap); err != nil {
				writeError(w, http.StatusBadRequest, "invalid labels json")
				return
			}
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

		step, _ := strconv.Atoi(q.Get("step"))
		if step <= 0 {
			step = chooseStep(to.Sub(from), host.SampleIntervalS)
		} else if host.SampleIntervalS > 0 && step < host.SampleIntervalS {
			step = host.SampleIntervalS
		}
		step = clampStepBuckets(to.Sub(from), step)

		coldCutoff := now.Add(-coldRetention)
		rawAvail := now.Add(-rawRetention)
		rawStart := rawAvail
		if b := rollupBoundary(now, from, to, &step); b.After(rawStart) {
			rawStart = b
		}
		acc := map[int64]*bucketAcc{}
		add := func(t time.Time, sum float64, n int64) {
			k := t.Unix()
			a := acc[k]
			if a == nil {
				a = &bucketAcc{}
				acc[k] = a
			}
			a.sum += sum
			a.n += n
		}
		coldStep := step
		if coldStep < caStepMin {
			coldStep = caStepMin
		}
		addSlot := func(slot time.Time, v float64) {
			bucket := (slot.Unix() / int64(coldStep)) * int64(coldStep)
			add(time.Unix(bucket, 0).UTC(), v, 1)
		}

		readRaw := func(lo, hi time.Time) error {
			return queryRows(r.Context(), db, `
				SELECT time_bucket($1::interval, time) AS bucket, sum(value), count(*)
				FROM metric_points
				WHERE host_id = $2 AND metric = $3 AND time >= $4 AND time < $5
				  AND ($6::jsonb = '{}'::jsonb OR labels @> $6::jsonb)
				GROUP BY bucket
				ORDER BY bucket ASC
			`, []any{intervalString(step), hostID, int16(metricID), lo, hi, labelArg}, func(rows pgx.Rows) error {
				var (
					t   time.Time
					sum float64
					n   int64
				)
				if err := rows.Scan(&t, &sum, &n); err != nil {
					return err
				}
				add(t, sum, n)
				return nil
			})
		}
		readColdRaw := func(lo, hi time.Time) error {
			return queryRows(r.Context(), db, `
				SELECT time_bucket($1::interval, time) AS slot, avg(value)
				FROM metric_points
				WHERE host_id = $2 AND metric = $3 AND time >= $4 AND time < $5
				  AND ($6::jsonb = '{}'::jsonb OR labels @> $6::jsonb)
				GROUP BY slot
				ORDER BY slot ASC
			`, []any{intervalString(caStepMin), hostID, int16(metricID), lo, hi, labelArg}, func(rows pgx.Rows) error {
				var (
					t time.Time
					v float64
				)
				if err := rows.Scan(&t, &v); err != nil {
					return err
				}
				addSlot(t, v)
				return nil
			})
		}

		var coldCovered []archive.Interval
		if ar != nil && from.Before(coldCutoff) && rawAvail.Before(coldCutoff) {
			covered, err := ar.CoverageIntervals(r.Context(), hostID, from, coldCutoff)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "archive coverage failed: "+err.Error())
				return
			}
			coldCovered = covered
		}
		if lo, hi, ok := coldRawClamp(from, rawAvail, coldCutoff, rawStart); ok {
			for _, g := range coldRawGaps(lo, hi, coldCovered) {
				if err := readColdRaw(g[0], g[1]); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		}

		if ar != nil && from.Before(coldCutoff) {
			coldTo := to
			if coldTo.After(coldCutoff) {
				coldTo = coldCutoff
			}
			recs, err := ar.Read(r.Context(), hostID, int16(metricID), from, coldTo, labelMap)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "archive read failed: "+err.Error())
				return
			}
			for _, rec := range recs {
				addSlot(time.UnixMicro(rec.Bucket), rec.Avg)
			}
		}

		caFrom := from
		if caFrom.Before(coldCutoff) {
			caFrom = coldCutoff
		}
		caTo := to
		if caTo.After(rawStart) {
			caTo = rawStart
		}
		if caTo.After(caFrom) {
			caStep := step
			if caStep < caStepMin {
				caStep = caStepMin
			}
			err := queryRows(r.Context(), db, `
				SELECT time_bucket($1::interval, bucket) AS b, sum(avg), count(*)
				FROM metric_points_5m
				WHERE host_id = $2 AND metric = $3 AND bucket >= $4 AND bucket < $5
				  AND ($6::jsonb = '{}'::jsonb OR labels @> $6::jsonb)
				GROUP BY b
				ORDER BY b ASC
			`, []any{intervalString(caStep), hostID, int16(metricID), caFrom, caTo, labelArg}, func(rows pgx.Rows) error {
				var (
					t   time.Time
					sum float64
					n   int64
				)
				if err := rows.Scan(&t, &sum, &n); err != nil {
					return err
				}
				add(t, sum, n)
				return nil
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		rawFrom := from
		if rawFrom.Before(rawStart) {
			rawFrom = rawStart
		}
		if to.After(rawFrom) {
			if err := readRaw(rawFrom, to); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		out := make([]seriesPoint, 0, len(acc))
		for k, a := range acc {
			out = append(out, seriesPoint{Ts: time.Unix(k, 0).UTC(), V: a.sum / float64(a.n)})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Ts.Before(out[j].Ts) })

		if strings.EqualFold(q.Get("format"), "csv") {
			writeSeriesCSV(w, host.Hostname, metricName, metricID.Meta().Unit, out)
			return
		}
		writeJSON(w, http.StatusOK, seriesResp{
			HostID:  hostID,
			Metric:  metricName,
			Unit:    metricID.Meta().Unit,
			StepSec: step,
			Points:  out,
		})
	}
}

func multiSeriesHandler(db *storage.DB, hosts *storage.Hosts, ar *archive.Archiver, ret RetentionConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		hostIDs := q.Get("host")
		metricName := q.Get("metric")
		splitBy := q.Get("split_by")
		if hostIDs == "" || metricName == "" {
			writeError(w, http.StatusBadRequest, "host and metric are required")
			return
		}
		hostID, err := strconv.ParseInt(hostIDs, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid host")
			return
		}
		metricID, ok := metrics.ByName(metricName)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown metric")
			return
		}

		now := time.Now()
		from := parseTime(q.Get("from"), now.Add(-1*time.Hour))
		to := parseTime(q.Get("to"), now)
		if !to.After(from) {
			writeError(w, http.StatusBadRequest, "to must be after from")
			return
		}
		rawRetention := ret.RawCutoff
		coldRetention := ret.Aggregate5mCutoff

		host, err := hosts.Get(r.Context(), hostID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "host not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		step, _ := strconv.Atoi(q.Get("step"))
		if step <= 0 {
			step = chooseStep(to.Sub(from), host.SampleIntervalS)
		} else if host.SampleIntervalS > 0 && step < host.SampleIntervalS {
			step = host.SampleIntervalS
		}
		step = clampStepBuckets(to.Sub(from), step)

		coldCutoff := now.Add(-coldRetention)
		rawAvail := now.Add(-rawRetention)
		rawStart := rawAvail
		if b := rollupBoundary(now, from, to, &step); b.After(rawStart) {
			rawStart = b
		}
		groups := map[string]*labelGroup{}
		order := []string{}
		add := func(labels map[string]string, t time.Time, sum float64, n int64) {
			key := splitKey(labels, splitBy)
			g, ok := groups[key]
			if !ok {
				g = &labelGroup{labels: labels, buckets: map[int64]*bucketAcc{}}
				groups[key] = g
				order = append(order, key)
			}
			k := t.Unix()
			a := g.buckets[k]
			if a == nil {
				a = &bucketAcc{}
				g.buckets[k] = a
			}
			a.sum += sum
			a.n += n
		}
		coldStep := step
		if coldStep < caStepMin {
			coldStep = caStepMin
		}
		addSlot := func(labels map[string]string, slot time.Time, v float64) {
			bucket := (slot.Unix() / int64(coldStep)) * int64(coldStep)
			add(labels, time.Unix(bucket, 0).UTC(), v, 1)
		}

		readRaw := func(lo, hi time.Time) error {
			return queryRows(r.Context(), db, `
				SELECT labels, time_bucket($1::interval, time) AS bucket, sum(value), count(*)
				FROM metric_points
				WHERE host_id = $2 AND metric = $3 AND time >= $4 AND time < $5
				GROUP BY labels, bucket
				ORDER BY labels, bucket ASC
			`, []any{intervalString(step), hostID, int16(metricID), lo, hi}, func(rows pgx.Rows) error {
				var (
					labels map[string]string
					t      time.Time
					sum    float64
					n      int64
				)
				if err := rows.Scan(&labels, &t, &sum, &n); err != nil {
					return err
				}
				add(labels, t, sum, n)
				return nil
			})
		}
		readColdRaw := func(lo, hi time.Time) error {
			return queryRows(r.Context(), db, `
				SELECT labels, time_bucket($1::interval, time) AS slot, avg(value)
				FROM metric_points
				WHERE host_id = $2 AND metric = $3 AND time >= $4 AND time < $5
				GROUP BY labels, slot
				ORDER BY labels, slot ASC
			`, []any{intervalString(caStepMin), hostID, int16(metricID), lo, hi}, func(rows pgx.Rows) error {
				var (
					labels map[string]string
					t      time.Time
					v      float64
				)
				if err := rows.Scan(&labels, &t, &v); err != nil {
					return err
				}
				addSlot(labels, t, v)
				return nil
			})
		}

		var coldCovered []archive.Interval
		if ar != nil && from.Before(coldCutoff) && rawAvail.Before(coldCutoff) {
			covered, err := ar.CoverageIntervals(r.Context(), hostID, from, coldCutoff)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "archive coverage failed: "+err.Error())
				return
			}
			coldCovered = covered
		}
		if lo, hi, ok := coldRawClamp(from, rawAvail, coldCutoff, rawStart); ok {
			for _, g := range coldRawGaps(lo, hi, coldCovered) {
				if err := readColdRaw(g[0], g[1]); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		}

		if ar != nil && from.Before(coldCutoff) {
			coldTo := to
			if coldTo.After(coldCutoff) {
				coldTo = coldCutoff
			}
			recs, err := ar.Read(r.Context(), hostID, int16(metricID), from, coldTo, nil)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "archive read failed: "+err.Error())
				return
			}
			for _, rec := range recs {
				var labels map[string]string
				if rec.Labels != "" {
					if err := json.Unmarshal([]byte(rec.Labels), &labels); err != nil {
						continue
					}
				}
				addSlot(labels, time.UnixMicro(rec.Bucket), rec.Avg)
			}
		}

		caFrom := from
		if caFrom.Before(coldCutoff) {
			caFrom = coldCutoff
		}
		caTo := to
		if caTo.After(rawStart) {
			caTo = rawStart
		}
		if caTo.After(caFrom) {
			caStep := step
			if caStep < caStepMin {
				caStep = caStepMin
			}
			err := queryRows(r.Context(), db, `
				SELECT labels, time_bucket($1::interval, bucket) AS b, sum(avg), count(*)
				FROM metric_points_5m
				WHERE host_id = $2 AND metric = $3 AND bucket >= $4 AND bucket < $5
				GROUP BY labels, b
				ORDER BY labels, b ASC
			`, []any{intervalString(caStep), hostID, int16(metricID), caFrom, caTo}, func(rows pgx.Rows) error {
				var (
					labels map[string]string
					t      time.Time
					sum    float64
					n      int64
				)
				if err := rows.Scan(&labels, &t, &sum, &n); err != nil {
					return err
				}
				add(labels, t, sum, n)
				return nil
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		rawFrom := from
		if rawFrom.Before(rawStart) {
			rawFrom = rawStart
		}
		if to.After(rawFrom) {
			if err := readRaw(rawFrom, to); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		series := make([]seriesEntry, 0, len(order))
		for _, key := range order {
			g := groups[key]
			pts := make([]seriesPoint, 0, len(g.buckets))
			for k, a := range g.buckets {
				pts = append(pts, seriesPoint{Ts: time.Unix(k, 0).UTC(), V: a.sum / float64(a.n)})
			}
			sort.Slice(pts, func(i, j int) bool { return pts[i].Ts.Before(pts[j].Ts) })
			series = append(series, seriesEntry{Labels: g.labels, Points: pts})
		}

		if strings.EqualFold(q.Get("format"), "csv") {
			writeMultiSeriesCSV(w, host.Hostname, metricName, metricID.Meta().Unit, splitBy, series)
			return
		}
		writeJSON(w, http.StatusOK, multiSeriesResp{
			HostID:  hostID,
			Metric:  metricName,
			Unit:    metricID.Meta().Unit,
			StepSec: step,
			SplitBy: splitBy,
			Series:  series,
		})
	}
}

type batchSeriesEntry struct {
	Points []seriesPoint `json:"points"`
}

type batchSeriesResp struct {
	Metric  string                      `json:"metric"`
	Unit    string                      `json:"unit"`
	StepSec int                         `json:"step_sec"`
	Hosts   map[string]batchSeriesEntry `json:"hosts"`
}

type hostBucketKey struct {
	host   int64
	bucket int64
}

type batchBucket struct {
	sum float64
	n   int64
	max float64
	has bool
}

func (a *batchBucket) addAvg(sum float64, n int64) {
	a.sum += sum
	a.n += n
}

func (a *batchBucket) addMax(v float64) {
	if !a.has || v > a.max {
		a.max = v
		a.has = true
	}
}

func (a *batchBucket) value(aggMax bool) float64 {
	if aggMax {
		return a.max
	}
	if a.n > 0 {
		return a.sum / float64(a.n)
	}
	return 0
}

const batchSeriesMaxHosts = 200

func seriesBatchHandler(db *storage.DB, hosts *storage.Hosts, ret RetentionConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		hostsParam := q.Get("hosts")
		metricName := q.Get("metric")
		if hostsParam == "" || metricName == "" {
			writeError(w, http.StatusBadRequest, "hosts and metric are required")
			return
		}
		metricID, ok := metrics.ByName(metricName)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown metric")
			return
		}

		parts := strings.Split(hostsParam, ",")
		requested := make([]int64, 0, len(parts))
		seen := map[int64]struct{}{}
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			id, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid host id: "+p)
				return
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			requested = append(requested, id)
		}
		if len(requested) == 0 {
			writeError(w, http.StatusBadRequest, "no host ids provided")
			return
		}
		if len(requested) > batchSeriesMaxHosts {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("too many host ids (max %d)", batchSeriesMaxHosts))
			return
		}

		hostIDs := make([]int64, 0, len(requested))
		minInterval := 0
		for _, id := range requested {
			h, err := hosts.Get(r.Context(), id)
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					continue
				}
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			hostIDs = append(hostIDs, id)
			if h.SampleIntervalS > 0 && (minInterval == 0 || h.SampleIntervalS < minInterval) {
				minInterval = h.SampleIntervalS
			}
		}
		if len(hostIDs) == 0 {
			writeError(w, http.StatusNotFound, "no matching hosts")
			return
		}
		if minInterval <= 0 {
			minInterval = 10
		}

		labelSel := q.Get("labels")
		labelArg := []byte("{}")
		if labelSel != "" {
			var labelMap map[string]string
			if err := json.Unmarshal([]byte(labelSel), &labelMap); err != nil {
				writeError(w, http.StatusBadRequest, "invalid labels json")
				return
			}
			labelArg = []byte(labelSel)
		}

		now := time.Now()
		from := parseTime(q.Get("from"), now.Add(-1*time.Hour))
		to := parseTime(q.Get("to"), now)
		if !to.After(from) {
			writeError(w, http.StatusBadRequest, "to must be after from")
			return
		}

		step, _ := strconv.Atoi(q.Get("step"))
		if step <= 0 {
			step = chooseStep(to.Sub(from), minInterval)
		} else if step < minInterval {
			step = minInterval
		}
		step = clampStepBuckets(to.Sub(from), step)

		aggMax := strings.EqualFold(q.Get("agg"), "max")

		result := make(map[string]batchSeriesEntry, len(hostIDs))
		for _, id := range hostIDs {
			result[strconv.FormatInt(id, 10)] = batchSeriesEntry{Points: []seriesPoint{}}
		}

		rawAgg, caAgg := "sum(value), count(*)", "sum(avg), count(*)"
		if aggMax {
			rawAgg, caAgg = "max(value)", "max(max)"
		}
		acc := map[hostBucketKey]*batchBucket{}
		bucketFor := func(hostID int64, t time.Time) *batchBucket {
			k := hostBucketKey{host: hostID, bucket: t.Unix()}
			a := acc[k]
			if a == nil {
				a = &batchBucket{}
				acc[k] = a
			}
			return a
		}
		addRow := func(rows pgx.Rows) error {
			var (
				hostID int64
				t      time.Time
			)
			if aggMax {
				var v float64
				if err := rows.Scan(&hostID, &t, &v); err != nil {
					return err
				}
				bucketFor(hostID, t).addMax(v)
				return nil
			}
			var (
				sum float64
				n   int64
			)
			if err := rows.Scan(&hostID, &t, &sum, &n); err != nil {
				return err
			}
			bucketFor(hostID, t).addAvg(sum, n)
			return nil
		}

		rawAvail := now.Add(-ret.RawCutoff)
		if ret.RawCutoff <= 0 {
			rawAvail = from
		}
		rawStart := rawAvail
		if b := rollupBoundary(now, from, to, &step); b.After(rawStart) {
			rawStart = b
		}
		caStep := step
		if caStep < caStepMin {
			caStep = caStepMin
		}

		readRaw := func(lo, hi time.Time) error {
			return queryRows(r.Context(), db, `
				SELECT host_id, time_bucket($1::interval, time) AS bucket, `+rawAgg+`
				FROM metric_points
				WHERE host_id = ANY($2) AND metric = $3 AND time >= $4 AND time < $5
				  AND ($6::jsonb = '{}'::jsonb OR labels @> $6::jsonb)
				GROUP BY host_id, bucket
				ORDER BY host_id, bucket ASC
			`, []any{intervalString(step), hostIDs, int16(metricID), lo, hi, labelArg}, addRow)
		}
		readColdRawAvg := func(lo, hi time.Time) error {
			return queryRows(r.Context(), db, `
				SELECT host_id, time_bucket($1::interval, time) AS slot, avg(value)
				FROM metric_points
				WHERE host_id = ANY($2) AND metric = $3 AND time >= $4 AND time < $5
				  AND ($6::jsonb = '{}'::jsonb OR labels @> $6::jsonb)
				GROUP BY host_id, slot
				ORDER BY host_id, slot ASC
			`, []any{intervalString(caStepMin), hostIDs, int16(metricID), lo, hi, labelArg}, func(rows pgx.Rows) error {
				var (
					hostID int64
					t      time.Time
					v      float64
				)
				if err := rows.Scan(&hostID, &t, &v); err != nil {
					return err
				}
				bucket := (t.Unix() / int64(caStep)) * int64(caStep)
				bucketFor(hostID, time.Unix(bucket, 0).UTC()).addAvg(v, 1)
				return nil
			})
		}

		caFrom := from
		var coldCutoff time.Time
		if ret.Aggregate5mCutoff > 0 {
			coldCutoff = now.Add(-ret.Aggregate5mCutoff)
			if caFrom.Before(coldCutoff) {
				caFrom = coldCutoff
			}
		}
		if !coldCutoff.IsZero() {
			if lo, hi, ok := coldRawClamp(from, rawAvail, coldCutoff, rawStart); ok {
				readCold := readColdRawAvg
				if aggMax {
					readCold = readRaw
				}
				if err := readCold(lo, hi); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		}
		caTo := to
		if caTo.After(rawStart) {
			caTo = rawStart
		}
		if caTo.After(caFrom) {
			err := queryRows(r.Context(), db, `
				SELECT host_id, time_bucket($1::interval, bucket) AS b, `+caAgg+`
				FROM metric_points_5m
				WHERE host_id = ANY($2) AND metric = $3 AND bucket >= $4 AND bucket < $5
				  AND ($6::jsonb = '{}'::jsonb OR labels @> $6::jsonb)
				GROUP BY host_id, b
				ORDER BY host_id, b ASC
			`, []any{intervalString(caStep), hostIDs, int16(metricID), caFrom, caTo, labelArg}, addRow)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		rawFrom := from
		if rawFrom.Before(rawStart) {
			rawFrom = rawStart
		}
		if to.After(rawFrom) {
			if err := readRaw(rawFrom, to); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		for k, a := range acc {
			key := strconv.FormatInt(k.host, 10)
			entry := result[key]
			entry.Points = append(entry.Points, seriesPoint{Ts: time.Unix(k.bucket, 0).UTC(), V: a.value(aggMax)})
			result[key] = entry
		}
		for _, id := range hostIDs {
			pts := result[strconv.FormatInt(id, 10)].Points
			sort.Slice(pts, func(i, j int) bool { return pts[i].Ts.Before(pts[j].Ts) })
		}

		writeJSON(w, http.StatusOK, batchSeriesResp{
			Metric:  metricName,
			Unit:    metricID.Meta().Unit,
			StepSec: step,
			Hosts:   result,
		})
	}
}

func splitKey(labels map[string]string, by string) string {
	if by != "" {
		return labels[by]
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(labels[k])
		sb.WriteByte(';')
	}
	return sb.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func parseTime(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	if strings.HasPrefix(s, "-") {
		if d, err := time.ParseDuration(s[1:]); err == nil {
			return time.Now().Add(-d)
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0)
	}
	return def
}

func parseTimeStrict(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if strings.HasPrefix(s, "-") {
		d, err := time.ParseDuration(s[1:])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid relative duration %q", s)
		}
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q", s)
}

var stepBuckets = []int{1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600}

func chooseStep(d time.Duration, intervalS int) int {
	if intervalS <= 0 {
		intervalS = 10
	}
	const targetPoints = 720
	raw := int(d.Seconds()) / targetPoints
	if raw < intervalS {
		raw = intervalS
	}
	for _, b := range stepBuckets {
		if b >= raw {
			return b
		}
	}
	return stepBuckets[len(stepBuckets)-1]
}

func intervalString(stepSec int) string {
	return strconv.Itoa(stepSec) + " seconds"
}

func clampStepBuckets(span time.Duration, step int) int {
	spanSec := int(span.Seconds())
	if step > 0 && spanSec/step > maxDetailBuckets {
		return (spanSec + maxDetailBuckets - 1) / maxDetailBuckets
	}
	return step
}

func listMetricsHandler() http.HandlerFunc {
	type item struct {
		ID   int16  `json:"id"`
		Name string `json:"name"`
		Unit string `json:"unit"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		all := metrics.All()
		out := make([]item, 0, len(all))
		for id, m := range all {
			out = append(out, item{ID: int16(id), Name: m.Name, Unit: m.Unit})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func statsHandler(b interface {
	Stats() (int, uint64, uint64)
}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, f, d := b.Stats()
		writeJSON(w, http.StatusOK, map[string]any{
			"queued":  q,
			"flushed": f,
			"dropped": d,
		})
	}
}

func csvFilename(hostname, metric string) string {
	safe := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
				b.WriteRune(r)
			default:
				b.WriteByte('_')
			}
		}
		out := b.String()
		if out == "" {
			return "data"
		}
		return out
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	return safe(hostname) + "_" + safe(metric) + "_" + stamp + ".csv"
}

func setCSVHeaders(w http.ResponseWriter, hostname, metric string) *csv.Writer {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, csvFilename(hostname, metric)))
	w.WriteHeader(http.StatusOK)
	return csv.NewWriter(w)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func writeSeriesCSV(w http.ResponseWriter, hostname, metric, unit string, points []seriesPoint) {
	cw := setCSVHeaders(w, hostname, metric)
	header := "value"
	if unit != "" {
		header = "value_" + unit
	}
	_ = cw.Write([]string{"ts", header})
	for _, p := range points {
		_ = cw.Write([]string{p.Ts.UTC().Format(time.RFC3339Nano), formatFloat(p.V)})
	}
	cw.Flush()
}

func writeMultiSeriesCSV(w http.ResponseWriter, hostname, metric, unit, splitBy string, series []seriesEntry) {
	cw := setCSVHeaders(w, hostname, metric)
	valueHeader := "value"
	if unit != "" {
		valueHeader = "value_" + unit
	}
	labelCol := splitBy
	if labelCol == "" {
		labelCol = "labels"
	}
	_ = cw.Write([]string{"ts", csvSafe(labelCol), valueHeader})
	for _, s := range series {
		var labelVal string
		if splitBy != "" {
			labelVal = s.Labels[splitBy]
		} else if len(s.Labels) > 0 {
			b, _ := json.Marshal(s.Labels)
			labelVal = string(b)
		}
		for _, p := range s.Points {
			_ = cw.Write([]string{p.Ts.UTC().Format(time.RFC3339Nano), csvSafe(labelVal), formatFloat(p.V)})
		}
	}
	cw.Flush()
}
