package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"servermonitor/internal/server/notify"
	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type AlertBroadcaster interface {
	BroadcastAlert(ev wire.AlertEvent)
}

type Rule struct {
	ID            int32
	Name          string
	HostSelector  HostSelector
	Metric        metrics.ID
	LabelSelector map[string]string
	Comparator    string
	Threshold     float64
	WindowS       int
	ForS          int
	Agg           string
	Severity      string
	CooldownS     int
	ChannelIDs    []int32
	Enabled       bool
}

type HostSelector struct {
	All  bool              `json:"all,omitempty"`
	IDs  []int64           `json:"ids,omitempty"`
	Tags map[string]string `json:"tags,omitempty"`
}

type Channel struct {
	ID      int32
	Name    string
	Kind    string
	Config  json.RawMessage
	Enabled bool
}

type Engine struct {
	pool        *pgxpool.Pool
	dispatcher  *notify.Dispatcher
	broadcaster AlertBroadcaster
	logger      *slog.Logger
	checkPeriod time.Duration

	mu       sync.Mutex
	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func New(pool *pgxpool.Pool, dispatcher *notify.Dispatcher, broadcaster AlertBroadcaster, logger *slog.Logger) *Engine {
	return &Engine{
		pool:        pool,
		dispatcher:  dispatcher,
		broadcaster: broadcaster,
		logger:      logger,
		checkPeriod: 15 * time.Second,
		stopCh:      make(chan struct{}),
	}
}

func (e *Engine) Start(ctx context.Context) {
	e.wg.Add(1)
	go e.run(ctx)
}

func (e *Engine) Stop() {
	e.stopOnce.Do(func() { close(e.stopCh) })
	e.wg.Wait()
}

func (e *Engine) run(ctx context.Context) {
	defer e.wg.Done()
	t := time.NewTicker(e.checkPeriod)
	defer t.Stop()
	e.evaluateAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-t.C:
			e.evaluateAll(ctx)
		}
	}
}

func (e *Engine) evaluateAll(ctx context.Context) {
	rules, err := e.loadRules(ctx)
	if err != nil {
		e.logger.Warn("alerting: load rules", "err", err)
		return
	}
	channels, err := e.loadChannels(ctx)
	if err != nil {
		e.logger.Warn("alerting: load channels", "err", err)
		return
	}
	rulesByID := make(map[int32]Rule, len(rules))
	matched := map[int32]map[int64]ruleHost{}
	matchedKnown := map[int32]bool{}
	for _, r := range rules {
		rulesByID[r.ID] = r
		if !r.Enabled {
			continue
		}
		hosts, err := e.matchedHosts(ctx, r)
		if err != nil {
			e.logger.Warn("alerting: match hosts", "rule", r.Name, "err", err)
			continue
		}
		m := make(map[int64]ruleHost, len(hosts))
		for _, h := range hosts {
			m[h.id] = h
		}
		matched[r.ID] = m
		matchedKnown[r.ID] = true
		if err := e.evaluateRule(ctx, r, hosts, channels); err != nil {
			e.logger.Warn("alerting: evaluate", "rule", r.Name, "err", err)
		}
	}
	e.sweepOrphans(ctx, rulesByID, matched, matchedKnown, channels)
}

func (e *Engine) loadRules(ctx context.Context) ([]Rule, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT id, name, host_selector, metric, label_selector, comparator, threshold,
		       window_s, for_s, agg, severity, cooldown_s, channel_ids, enabled
		FROM alert_rules
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var (
			r          Rule
			hostSelRaw []byte
			labelSel   map[string]string
			metricID   int16
			channelArr []int32
		)
		if err := rows.Scan(&r.ID, &r.Name, &hostSelRaw, &metricID, &labelSel,
			&r.Comparator, &r.Threshold, &r.WindowS, &r.ForS, &r.Agg,
			&r.Severity, &r.CooldownS, &channelArr, &r.Enabled); err != nil {
			return nil, err
		}
		if len(hostSelRaw) > 0 {
			if err := json.Unmarshal(hostSelRaw, &r.HostSelector); err != nil {
				e.logger.Warn("alerting: host_selector unmarshal", "rule", r.Name, "rule_id", r.ID, "err", err)
				r.HostSelector = HostSelector{}
			}
		}
		r.LabelSelector = labelSel
		r.Metric = metrics.ID(metricID)
		r.ChannelIDs = channelArr
		out = append(out, r)
	}
	return out, rows.Err()
}

func (e *Engine) loadChannels(ctx context.Context) (map[int32]Channel, error) {
	rows, err := e.pool.Query(ctx, `SELECT id, name, kind, config, enabled FROM notification_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int32]Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Enabled); err != nil {
			return nil, err
		}
		out[c.ID] = c
	}
	return out, rows.Err()
}

func (e *Engine) evaluateRule(ctx context.Context, r Rule, hosts []ruleHost, channels map[int32]Channel) error {
	for _, h := range hosts {
		values, err := e.queryGroups(ctx, r, h.id)
		if err != nil {
			return err
		}
		for labelKey, group := range values {
			fired := e.compare(r, group.value)
			if err := e.transition(ctx, r, h, labelKey, group.labels, fired, group.value, channels); err != nil {
				return err
			}
		}
		stale, err := e.staleStates(ctx, r.ID, h.id, values)
		if err != nil {
			return err
		}
		for _, s := range stale {
			if err := e.transition(ctx, r, h, s.labelKey, s.labels, false, 0, channels); err != nil {
				return err
			}
		}
	}
	return nil
}

type staleState struct {
	labelKey string
	labels   map[string]string
}

func (e *Engine) sweepOrphans(
	ctx context.Context,
	rulesByID map[int32]Rule,
	matched map[int32]map[int64]ruleHost,
	matchedKnown map[int32]bool,
	channels map[int32]Channel,
) {
	rows, err := e.pool.Query(ctx, `
		SELECT s.rule_id, s.host_id, h.hostname, s.label_key, COALESCE(s.last_value, 0)
		FROM alert_states s
		JOIN hosts h ON h.id = s.host_id
		WHERE s.state IN ('pending', 'firing')
	`)
	if err != nil {
		e.logger.Warn("alerting: sweep orphans", "err", err)
		return
	}
	defer rows.Close()
	type orphan struct {
		ruleID    int32
		host      ruleHost
		labelKey  string
		lastValue float64
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.ruleID, &o.host.id, &o.host.hostname, &o.labelKey, &o.lastValue); err != nil {
			e.logger.Warn("alerting: scan orphan", "err", err)
			continue
		}
		rule, ok := rulesByID[o.ruleID]
		if !ok {
			continue
		}
		if !rule.Enabled {
			orphans = append(orphans, o)
			continue
		}
		if !matchedKnown[o.ruleID] {
			continue
		}
		if _, hit := matched[o.ruleID][o.host.id]; hit {
			continue
		}
		orphans = append(orphans, o)
	}
	if err := rows.Err(); err != nil {
		e.logger.Warn("alerting: sweep orphans rows", "err", err)
	}
	for _, o := range orphans {
		rule := rulesByID[o.ruleID]
		labels := parseLabelKey(o.labelKey)
		if err := e.transition(ctx, rule, o.host, o.labelKey, labels, false, o.lastValue, channels); err != nil {
			e.logger.Warn("alerting: resolve orphan", "rule", rule.Name, "err", err)
		}
	}
}

func (e *Engine) staleStates(ctx context.Context, ruleID int32, hostID int64, present map[string]ruleGroup) ([]staleState, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT label_key FROM alert_states
		WHERE rule_id = $1 AND host_id = $2 AND state IN ('pending', 'firing')
	`, ruleID, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []staleState
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		if _, ok := present[k]; ok {
			continue
		}
		out = append(out, staleState{labelKey: k, labels: parseLabelKey(k)})
	}
	return out, rows.Err()
}

func parseLabelKey(k string) map[string]string {
	if k == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(strings.TrimSuffix(k, ";"), ";") {
		if pair == "" {
			continue
		}
		if i := strings.IndexByte(pair, '='); i >= 0 {
			out[pair[:i]] = pair[i+1:]
		}
	}
	return out
}

type ruleGroup struct {
	value  float64
	labels map[string]string
}

type ruleHost struct {
	id       int64
	hostname string
}

func (e *Engine) matchedHosts(ctx context.Context, r Rule) ([]ruleHost, error) {
	switch {
	case r.HostSelector.All:
		rows, err := e.pool.Query(ctx, `SELECT id, hostname FROM hosts`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanHosts(rows)
	case len(r.HostSelector.IDs) > 0:
		rows, err := e.pool.Query(ctx, `SELECT id, hostname FROM hosts WHERE id = ANY($1)`, r.HostSelector.IDs)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanHosts(rows)
	case len(r.HostSelector.Tags) > 0:
		tags, _ := json.Marshal(r.HostSelector.Tags)
		rows, err := e.pool.Query(ctx, `SELECT id, hostname FROM hosts WHERE tags @> $1::jsonb`, tags)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanHosts(rows)
	default:
		e.logger.Warn("alerting: empty host_selector, rule will not fire", "rule", r.Name, "rule_id", r.ID)
		return nil, nil
	}
}

func scanHosts(rows pgx.Rows) ([]ruleHost, error) {
	out := []ruleHost{}
	for rows.Next() {
		var h ruleHost
		if err := rows.Scan(&h.id, &h.hostname); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (e *Engine) queryGroups(ctx context.Context, r Rule, hostID int64) (map[string]ruleGroup, error) {
	aggSQL := "avg(value)"
	switch strings.ToLower(r.Agg) {
	case "max":
		aggSQL = "max(value)"
	case "min":
		aggSQL = "min(value)"
	case "last":
		aggSQL = "last(value, time)"
	}
	labelArg := []byte("{}")
	if len(r.LabelSelector) > 0 {
		labelArg, _ = json.Marshal(r.LabelSelector)
	}
	query := fmt.Sprintf(`
		SELECT labels, %s
		FROM metric_points
		WHERE host_id = $1 AND metric = $2 AND time >= now() - $3::interval
		  AND ($4::jsonb = '{}'::jsonb OR labels @> $4::jsonb)
		GROUP BY labels
	`, aggSQL)
	rows, err := e.pool.Query(ctx, query, hostID, int16(r.Metric), fmt.Sprintf("%d seconds", r.WindowS), labelArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ruleGroup{}
	for rows.Next() {
		var (
			labels map[string]string
			val    float64
		)
		if err := rows.Scan(&labels, &val); err != nil {
			return nil, err
		}
		out[labelKey(labels)] = ruleGroup{value: val, labels: labels}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Engine) compare(r Rule, value float64) bool {
	switch r.Comparator {
	case ">":
		return value > r.Threshold
	case "<":
		return value < r.Threshold
	case ">=":
		return value >= r.Threshold
	case "<=":
		return value <= r.Threshold
	case "==":
		return value == r.Threshold
	case "!=":
		return value != r.Threshold
	}
	return false
}

func (e *Engine) transition(
	ctx context.Context,
	r Rule, h ruleHost, key string, labels map[string]string,
	predicate bool, value float64,
	channels map[int32]Channel,
) error {
	var (
		state        string
		since        time.Time
		lastNotified *time.Time
		lastValue    *float64
	)
	row := e.pool.QueryRow(ctx, `
		SELECT state, since, last_notified, last_value
		FROM alert_states WHERE rule_id = $1 AND host_id = $2 AND label_key = $3
	`, r.ID, h.id, key)
	err := row.Scan(&state, &since, &lastNotified, &lastValue)
	if errors.Is(err, pgx.ErrNoRows) {
		state = "ok"
		since = time.Now()
	} else if err != nil {
		return err
	}

	now := time.Now()
	switch {
	case predicate && state == "ok":
		_, err := e.pool.Exec(ctx, `
			INSERT INTO alert_states (rule_id, host_id, label_key, state, since, last_value)
			VALUES ($1, $2, $3, 'pending', now(), $4)
			ON CONFLICT (rule_id, host_id, label_key) DO UPDATE
			  SET state='pending', since=now(), last_value=EXCLUDED.last_value
		`, r.ID, h.id, key, value)
		return err

	case predicate && state == "pending":
		if now.Sub(since) < time.Duration(r.ForS)*time.Second {
			_, err := e.pool.Exec(ctx, `UPDATE alert_states SET last_value=$4
				WHERE rule_id=$1 AND host_id=$2 AND label_key=$3`,
				r.ID, h.id, key, value)
			return err
		}
		_, err := e.pool.Exec(ctx, `UPDATE alert_states
			SET state='firing', since=now(), last_notified=now(), last_value=$4
			WHERE rule_id=$1 AND host_id=$2 AND label_key=$3`,
			r.ID, h.id, key, value)
		if err != nil {
			return err
		}
		_, _ = e.pool.Exec(ctx, `
			INSERT INTO alert_history (rule_id, host_id, label_key, fired_at, value, labels, severity)
			VALUES ($1, $2, $3, now(), $4, $5::jsonb, $6)
		`, r.ID, h.id, key, value, mustJSON(labels), r.Severity)
		e.dispatch(ctx, "fire", r, h, labels, value, channels)
		return nil

	case predicate && state == "firing":
		shouldRenotify := lastNotified == nil || now.Sub(*lastNotified) >= time.Duration(r.CooldownS)*time.Second
		_, err := e.pool.Exec(ctx, `UPDATE alert_states SET last_value=$4 WHERE rule_id=$1 AND host_id=$2 AND label_key=$3`,
			r.ID, h.id, key, value)
		if err != nil {
			return err
		}
		if shouldRenotify {
			_, _ = e.pool.Exec(ctx, `UPDATE alert_states SET last_notified=now() WHERE rule_id=$1 AND host_id=$2 AND label_key=$3`,
				r.ID, h.id, key)
			e.dispatch(ctx, "fire", r, h, labels, value, channels)
		}
		return nil

	case !predicate && (state == "firing" || state == "pending"):
		_, err := e.pool.Exec(ctx, `UPDATE alert_states SET state='ok', since=now(), last_value=$4 WHERE rule_id=$1 AND host_id=$2 AND label_key=$3`,
			r.ID, h.id, key, value)
		if err != nil {
			return err
		}
		if state == "firing" {
			_, _ = e.pool.Exec(ctx, `UPDATE alert_history SET resolved_at = now()
				WHERE id = (SELECT id FROM alert_history
				            WHERE rule_id=$1 AND host_id=$2 AND label_key=$3 AND resolved_at IS NULL
				            ORDER BY fired_at DESC LIMIT 1)`,
				r.ID, h.id, key)
			e.dispatch(ctx, "resolve", r, h, labels, value, channels)
		}
		return nil
	}
	return nil
}

func (e *Engine) dispatch(ctx context.Context, kind string, r Rule, h ruleHost, labels map[string]string, value float64, channels map[int32]Channel) {
	now := time.Now()
	msg := notify.Notification{
		Kind:      kind,
		RuleID:    r.ID,
		RuleName:  r.Name,
		HostID:    h.id,
		Hostname:  h.hostname,
		Metric:    r.Metric.Meta().Name,
		Unit:      r.Metric.Meta().Unit,
		Labels:    labels,
		Value:     value,
		Threshold: r.Threshold,
		Cmp:       r.Comparator,
		Severity:  r.Severity,
		FiredAt:   now,
	}
	for _, id := range r.ChannelIDs {
		c, ok := channels[id]
		if !ok || !c.Enabled {
			continue
		}
		e.dispatcher.Send(ctx, c.Kind, c.Name, c.Config, msg)
	}
	if e.broadcaster == nil {
		return
	}
	e.broadcaster.BroadcastAlert(wire.AlertEvent{
		Kind:     kind,
		HostID:   h.id,
		RuleID:   r.ID,
		RuleName: r.Name,
		Severity: r.Severity,
		Metric:   r.Metric.Meta().Name,
		Value:    value,
		Time:     now,
	})
}

func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(labels[k])
		sb.WriteByte(';')
	}
	return sb.String()
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
