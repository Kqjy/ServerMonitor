package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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

const caStepMin = 300

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

		coldCutoff := now.Add(-coldRetention)
		rawCutoff := now.Add(-rawRetention)
		out := make([]seriesPoint, 0, 600)

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
			coldStep := step
			if coldStep < caStepMin {
				coldStep = caStepMin
			}
			type coldAcc struct {
				sum float64
				n   int
			}
			agg := map[int64]*coldAcc{}
			for _, rec := range recs {
				t := time.UnixMicro(rec.Bucket)
				bucket := (t.Unix() / int64(coldStep)) * int64(coldStep)
				a, ok := agg[bucket]
				if !ok {
					a = &coldAcc{}
					agg[bucket] = a
				}
				a.sum += rec.Avg
				a.n++
			}
			buckets := make([]int64, 0, len(agg))
			for b := range agg {
				buckets = append(buckets, b)
			}
			sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })
			for _, b := range buckets {
				a := agg[b]
				out = append(out, seriesPoint{Ts: time.Unix(b, 0).UTC(), V: a.sum / float64(a.n)})
			}
		}

		caFrom := from
		if caFrom.Before(coldCutoff) {
			caFrom = coldCutoff
		}
		caTo := to
		if caTo.After(rawCutoff) {
			caTo = rawCutoff
		}
		if caTo.After(caFrom) {
			caStep := step
			if caStep < caStepMin {
				caStep = caStepMin
			}
			rows, err := db.Pool.Query(r.Context(), `
				SELECT time_bucket($1::interval, bucket) AS b, avg(avg)
				FROM metric_points_5m
				WHERE host_id = $2 AND metric = $3 AND bucket >= $4 AND bucket < $5
				  AND ($6::jsonb = '{}'::jsonb OR labels @> $6::jsonb)
				GROUP BY b
				ORDER BY b ASC
			`, intervalString(caStep), hostID, int16(metricID), caFrom, caTo, labelArg)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for rows.Next() {
				var (
					t time.Time
					v float64
				)
				if err := rows.Scan(&t, &v); err != nil {
					rows.Close()
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				out = append(out, seriesPoint{Ts: t, V: v})
			}
			rowsErr := rows.Err()
			rows.Close()
			if rowsErr != nil {
				writeError(w, http.StatusInternalServerError, rowsErr.Error())
				return
			}
		}

		rawFrom := from
		if rawFrom.Before(rawCutoff) {
			rawFrom = rawCutoff
		}
		if to.After(rawFrom) {
			rows, err := db.Pool.Query(r.Context(), `
				SELECT time_bucket($1::interval, time) AS bucket, avg(value)
				FROM metric_points
				WHERE host_id = $2 AND metric = $3 AND time >= $4 AND time < $5
				  AND ($6::jsonb = '{}'::jsonb OR labels @> $6::jsonb)
				GROUP BY bucket
				ORDER BY bucket ASC
			`, intervalString(step), hostID, int16(metricID), rawFrom, to, labelArg)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			defer rows.Close()

			for rows.Next() {
				var (
					t time.Time
					v float64
				)
				if err := rows.Scan(&t, &v); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				out = append(out, seriesPoint{Ts: t, V: v})
			}
			if err := rows.Err(); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
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

		coldCutoff := now.Add(-coldRetention)
		rawCutoff := now.Add(-rawRetention)
		grouped := map[string]*seriesEntry{}
		order := []string{}
		ingest := func(labels map[string]string, t time.Time, v float64) {
			key := splitKey(labels, splitBy)
			entry, ok := grouped[key]
			if !ok {
				entryLabels := labels
				if splitBy != "" {
					entryLabels = map[string]string{splitBy: labels[splitBy]}
				}
				entry = &seriesEntry{Labels: entryLabels}
				grouped[key] = entry
				order = append(order, key)
			}
			entry.Points = append(entry.Points, seriesPoint{Ts: t, V: v})
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
			coldStep := step
			if coldStep < caStepMin {
				coldStep = caStepMin
			}
			type coldKey struct {
				labelKey string
				bucket   int64
			}
			type coldAcc struct {
				sum    float64
				n      int
				labels map[string]string
			}
			agg := map[coldKey]*coldAcc{}
			order := []coldKey{}
			for _, rec := range recs {
				var labels map[string]string
				if rec.Labels != "" {
					if err := json.Unmarshal([]byte(rec.Labels), &labels); err != nil {
						continue
					}
				}
				t := time.UnixMicro(rec.Bucket)
				bucket := (t.Unix() / int64(coldStep)) * int64(coldStep)
				k := coldKey{labelKey: splitKey(labels, splitBy), bucket: bucket}
				a, ok := agg[k]
				if !ok {
					a = &coldAcc{labels: labels}
					agg[k] = a
					order = append(order, k)
				}
				a.sum += rec.Avg
				a.n++
			}
			sort.Slice(order, func(i, j int) bool {
				if order[i].labelKey != order[j].labelKey {
					return order[i].labelKey < order[j].labelKey
				}
				return order[i].bucket < order[j].bucket
			})
			for _, k := range order {
				a := agg[k]
				ingest(a.labels, time.Unix(k.bucket, 0).UTC(), a.sum/float64(a.n))
			}
		}

		caFrom := from
		if caFrom.Before(coldCutoff) {
			caFrom = coldCutoff
		}
		caTo := to
		if caTo.After(rawCutoff) {
			caTo = rawCutoff
		}
		if caTo.After(caFrom) {
			caStep := step
			if caStep < caStepMin {
				caStep = caStepMin
			}
			rows, err := db.Pool.Query(r.Context(), `
				SELECT labels, time_bucket($1::interval, bucket) AS b, avg(avg)
				FROM metric_points_5m
				WHERE host_id = $2 AND metric = $3 AND bucket >= $4 AND bucket < $5
				GROUP BY labels, b
				ORDER BY labels, b ASC
			`, intervalString(caStep), hostID, int16(metricID), caFrom, caTo)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for rows.Next() {
				var (
					labels map[string]string
					t      time.Time
					v      float64
				)
				if err := rows.Scan(&labels, &t, &v); err != nil {
					rows.Close()
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				ingest(labels, t, v)
			}
			rowsErr := rows.Err()
			rows.Close()
			if rowsErr != nil {
				writeError(w, http.StatusInternalServerError, rowsErr.Error())
				return
			}
		}

		rawFrom := from
		if rawFrom.Before(rawCutoff) {
			rawFrom = rawCutoff
		}
		if to.After(rawFrom) {
			rows, err := db.Pool.Query(r.Context(), `
				SELECT labels, time_bucket($1::interval, time) AS bucket, avg(value)
				FROM metric_points
				WHERE host_id = $2 AND metric = $3 AND time >= $4 AND time < $5
				GROUP BY labels, bucket
				ORDER BY labels, bucket ASC
			`, intervalString(step), hostID, int16(metricID), rawFrom, to)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			defer rows.Close()

			for rows.Next() {
				var (
					labels map[string]string
					t      time.Time
					v      float64
				)
				if err := rows.Scan(&labels, &t, &v); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				ingest(labels, t, v)
			}
			if err := rows.Err(); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		series := make([]seriesEntry, 0, len(order))
		for _, k := range order {
			series = append(series, *grouped[k])
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
