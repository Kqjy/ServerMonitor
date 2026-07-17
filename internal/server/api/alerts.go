package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"servermonitor/pkg/metrics"
)

type hostSelectorJSON struct {
	All  bool              `json:"all,omitempty"`
	IDs  []int64           `json:"ids,omitempty"`
	Tags map[string]string `json:"tags,omitempty"`
}

func validateHostSelector(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{"all":true}`), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var sel hostSelectorJSON
	if err := dec.Decode(&sel); err != nil {
		return nil, errors.New("host_selector: " + err.Error())
	}
	if dec.More() {
		return nil, errors.New("host_selector: unexpected trailing data")
	}
	set := 0
	if sel.All {
		set++
	}
	if len(sel.IDs) > 0 {
		set++
	}
	if len(sel.Tags) > 0 {
		set++
	}
	switch set {
	case 0:
		return nil, errors.New("host_selector must set one of: all=true, non-empty ids, non-empty tags")
	case 1:
	default:
		return nil, errors.New("host_selector must set exactly one of: all, ids, tags")
	}
	canonical, err := json.Marshal(sel)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

type alertRuleDTO struct {
	ID            int32             `json:"id"`
	Name          string            `json:"name"`
	HostSelector  json.RawMessage   `json:"host_selector"`
	Metric        string            `json:"metric"`
	LabelSelector map[string]string `json:"label_selector"`
	Comparator    string            `json:"comparator"`
	Threshold     float64           `json:"threshold"`
	WindowS       int               `json:"window_s"`
	ForS          int               `json:"for_s"`
	Agg           string            `json:"agg"`
	Severity      string            `json:"severity"`
	CooldownS     int               `json:"cooldown_s"`
	ChannelIDs    []int32           `json:"channel_ids"`
	Enabled       bool              `json:"enabled"`
}

func scanRule(rows pgx.Rows) (alertRuleDTO, error) {
	var (
		r        alertRuleDTO
		metricID int16
		hostSel  []byte
		labels   map[string]string
	)
	err := rows.Scan(&r.ID, &r.Name, &hostSel, &metricID, &labels, &r.Comparator,
		&r.Threshold, &r.WindowS, &r.ForS, &r.Agg, &r.Severity, &r.CooldownS,
		&r.ChannelIDs, &r.Enabled)
	if err != nil {
		return r, err
	}
	r.HostSelector = hostSel
	r.LabelSelector = labels
	r.Metric = metrics.ID(metricID).Meta().Name
	return r, nil
}

func listAlertRulesHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(), `
			SELECT id, name, host_selector, metric, label_selector, comparator,
			       threshold, window_s, for_s, agg, severity, cooldown_s, channel_ids, enabled
			FROM alert_rules ORDER BY id ASC
		`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := []alertRuleDTO{}
		for rows.Next() {
			ar, err := scanRule(rows)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			out = append(out, ar)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type ruleInput struct {
	Name          string            `json:"name"`
	HostSelector  json.RawMessage   `json:"host_selector,omitempty"`
	Metric        string            `json:"metric"`
	LabelSelector map[string]string `json:"label_selector"`
	Comparator    string            `json:"comparator"`
	Threshold     float64           `json:"threshold"`
	WindowS       int               `json:"window_s"`
	ForS          int               `json:"for_s"`
	Agg           string            `json:"agg"`
	Severity      string            `json:"severity"`
	CooldownS     int               `json:"cooldown_s"`
	ChannelIDs    []int32           `json:"channel_ids"`
	Enabled       *bool             `json:"enabled,omitempty"`
}

func validateRule(in *ruleInput) (metrics.ID, error) {
	if strings.TrimSpace(in.Name) == "" {
		return 0, errors.New("name required")
	}
	id, ok := metrics.ByName(in.Metric)
	if !ok {
		return 0, errors.New("unknown metric")
	}
	switch in.Comparator {
	case ">", "<", ">=", "<=", "==", "!=":
	default:
		return 0, errors.New("invalid comparator")
	}
	if in.WindowS <= 0 || in.ForS < 0 {
		return 0, errors.New("window_s and for_s must be positive")
	}
	if in.CooldownS < 0 {
		in.CooldownS = 600
	}
	switch strings.ToLower(in.Agg) {
	case "", "avg":
		in.Agg = "avg"
	case "max", "min", "last":
	default:
		return 0, errors.New("invalid agg")
	}
	if in.Severity == "" {
		in.Severity = "warning"
	}
	canonical, err := validateHostSelector(in.HostSelector)
	if err != nil {
		return 0, err
	}
	in.HostSelector = canonical
	return id, nil
}

func createAlertRuleHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in ruleInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		id, err := validateRule(&in)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		labels, _ := json.Marshal(in.LabelSelector)
		var newID int32
		err = pool.QueryRow(r.Context(), `
			INSERT INTO alert_rules (name, host_selector, metric, label_selector, comparator,
			                         threshold, window_s, for_s, agg, severity, cooldown_s, channel_ids, enabled)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id
		`, in.Name, []byte(in.HostSelector), int16(id), labels, in.Comparator,
			in.Threshold, in.WindowS, in.ForS, in.Agg, in.Severity, in.CooldownS, in.ChannelIDs, enabled).Scan(&newID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]int32{"id": newID})
	}
}

func updateAlertRuleHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		var in ruleInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		mid, err := validateRule(&in)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		labels, _ := json.Marshal(in.LabelSelector)
		_, err = pool.Exec(r.Context(), `
			UPDATE alert_rules SET name=$2, host_selector=$3, metric=$4, label_selector=$5,
			                       comparator=$6, threshold=$7, window_s=$8, for_s=$9,
			                       agg=$10, severity=$11, cooldown_s=$12, channel_ids=$13,
			                       enabled=$14, updated_at=now()
			WHERE id=$1
		`, id, in.Name, []byte(in.HostSelector), int16(mid), labels, in.Comparator,
			in.Threshold, in.WindowS, in.ForS, in.Agg, in.Severity, in.CooldownS, in.ChannelIDs, enabled)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteAlertRuleHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		tx, err := pool.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer tx.Rollback(r.Context())
		if _, err := tx.Exec(r.Context(), `
			UPDATE alert_history SET resolved_at = now()
			WHERE rule_id = $1 AND resolved_at IS NULL
		`, id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM alert_rules WHERE id=$1`, id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type alertHistoryRow struct {
	ID         int64             `json:"id"`
	RuleID     int32             `json:"rule_id"`
	RuleName   string            `json:"rule_name"`
	HostID     int64             `json:"host_id"`
	Hostname   string            `json:"hostname"`
	LabelKey   string            `json:"label_key,omitempty"`
	FiredAt    time.Time         `json:"fired_at"`
	ResolvedAt *time.Time        `json:"resolved_at,omitempty"`
	Value      float64           `json:"value"`
	Labels     map[string]string `json:"labels"`
	Severity   string            `json:"severity"`
}

func alertHistoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csvFormat := r.URL.Query().Get("format") == "csv"
		limit := 100
		limitSet := !csvFormat
		if raw, ok := r.URL.Query()["limit"]; ok && len(raw) > 0 {
			if l, err := strconv.Atoi(raw[0]); err == nil && l > 0 && (csvFormat || l <= 1000) {
				limit = l
				limitSet = true
			}
		}
		query := `
			SELECT h.id, h.rule_id, COALESCE(r.name, ''), h.host_id, COALESCE(ho.hostname,''),
			       h.label_key, h.fired_at, h.resolved_at, h.value, h.labels, COALESCE(h.severity,'')
			FROM alert_history h
			LEFT JOIN alert_rules r ON r.id = h.rule_id
			LEFT JOIN hosts ho ON ho.id = h.host_id
			ORDER BY h.fired_at DESC
		`
		args := []any{}
		if limitSet {
			query += " LIMIT $1"
			args = append(args, limit)
		}
		rows, err := pool.Query(r.Context(), query, args...)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		if csvFormat {
			writeAlertHistoryCSV(w, rows)
			return
		}
		out := []alertHistoryRow{}
		for rows.Next() {
			var ah alertHistoryRow
			if err := rows.Scan(&ah.ID, &ah.RuleID, &ah.RuleName, &ah.HostID, &ah.Hostname,
				&ah.LabelKey, &ah.FiredAt, &ah.ResolvedAt, &ah.Value, &ah.Labels, &ah.Severity); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			out = append(out, ah)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeAlertHistoryCSV(w http.ResponseWriter, rows pgx.Rows) {
	filename := "alert-history-" + time.Now().UTC().Format("20060102-150405") + ".csv"
	cw := setCSVHeadersFilename(w, filename)
	_ = cw.Write([]string{"fired_at", "resolved_at", "rule", "severity", "host", "value", "labels"})
	for rows.Next() {
		var ah alertHistoryRow
		if err := rows.Scan(&ah.ID, &ah.RuleID, &ah.RuleName, &ah.HostID, &ah.Hostname,
			&ah.LabelKey, &ah.FiredAt, &ah.ResolvedAt, &ah.Value, &ah.Labels, &ah.Severity); err != nil {
			return
		}
		resolvedAt := ""
		if ah.ResolvedAt != nil {
			resolvedAt = ah.ResolvedAt.UTC().Format(time.RFC3339)
		}
		labels := []byte("{}")
		if ah.Labels != nil {
			if encoded, err := json.Marshal(ah.Labels); err == nil {
				labels = encoded
			}
		}
		_ = cw.Write([]string{
			ah.FiredAt.UTC().Format(time.RFC3339),
			resolvedAt,
			csvSafe(ah.RuleName),
			csvSafe(ah.Severity),
			csvSafe(ah.Hostname),
			formatFloat(ah.Value),
			csvSafe(string(labels)),
		})
	}
	cw.Flush()
}

func clearAlertHistoryHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		olderThanDays := 0
		if values, ok := r.URL.Query()["older_than_days"]; ok {
			if len(values) != 1 || values[0] == "" {
				writeError(w, http.StatusBadRequest, "invalid older_than_days")
				return
			}
			value, err := strconv.Atoi(values[0])
			if err != nil || value < 0 {
				writeError(w, http.StatusBadRequest, "invalid older_than_days")
				return
			}
			olderThanDays = value
		}
		query := `DELETE FROM alert_history WHERE resolved_at IS NOT NULL`
		args := []any{}
		if olderThanDays > 0 {
			query += ` AND fired_at < now() - make_interval(days => $1)`
			args = append(args, olderThanDays)
		}
		result, err := pool.Exec(r.Context(), query, args...)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"deleted": result.RowsAffected()})
	}
}

type channelDTO struct {
	ID      int32           `json:"id"`
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

func redactChannelConfig(kind string, config json.RawMessage) json.RawMessage {
	if kind != "smtp" {
		return config
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(config, &m); err != nil {
		return config
	}
	if _, ok := m["password"]; !ok {
		return config
	}
	m["password"] = json.RawMessage(`""`)
	out, err := json.Marshal(m)
	if err != nil {
		return config
	}
	return out
}

func listChannelsHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(), `SELECT id, name, kind, config, enabled FROM notification_channels ORDER BY id`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		out := []channelDTO{}
		for rows.Next() {
			var c channelDTO
			if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Enabled); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			c.Config = redactChannelConfig(c.Kind, c.Config)
			out = append(out, c)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type channelInput struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Config  json.RawMessage `json:"config"`
	Enabled *bool           `json:"enabled,omitempty"`
}

func validateChannel(in *channelInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("name required")
	}
	if in.Kind != "smtp" && in.Kind != "webhook" {
		return errors.New("kind must be smtp or webhook")
	}
	if len(in.Config) == 0 {
		return errors.New("config required")
	}
	return nil
}

func createChannelHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in channelInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := validateChannel(&in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		var id int32
		err := pool.QueryRow(r.Context(), `
			INSERT INTO notification_channels (name, kind, config, enabled)
			VALUES ($1, $2, $3, $4) RETURNING id
		`, in.Name, in.Kind, []byte(in.Config), enabled).Scan(&id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]int32{"id": id})
	}
}

const updateChannelSQL = `
	UPDATE notification_channels SET
	  name=$2,
	  kind=$3,
	  enabled=$5,
	  config = CASE
	    WHEN $3 = 'smtp' AND COALESCE(NULLIF($4::jsonb->>'password',''), '') = ''
	    THEN jsonb_set($4::jsonb, '{password}', COALESCE(config->'password', '""'::jsonb))
	    ELSE $4::jsonb
	  END
	WHERE id=$1
`

func updateChannelHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		var in channelInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := validateChannel(&in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		_, err = pool.Exec(r.Context(), updateChannelSQL, id, in.Name, in.Kind, []byte(in.Config), enabled)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteChannelHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		_, err = pool.Exec(r.Context(), `DELETE FROM notification_channels WHERE id=$1`, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
