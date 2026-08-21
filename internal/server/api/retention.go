package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type RetentionConfig struct {
	Raw                string
	Aggregate5m        string
	Processes          string
	Containers         string
	Ports              string
	IPBanEvents        string
	CompressAfter      string
	ArchiveAggregate5m bool

	RawCutoff         time.Duration
	Aggregate5mCutoff time.Duration
}

type retentionEntry struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Target      string `json:"target"`
	Env         string `json:"env"`
	Configured  string `json:"configured"`
	Default     string `json:"default"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
}

type retentionResp struct {
	Policies        []retentionEntry `json:"policies"`
	IntervalFormat  string           `json:"interval_format"`
	RequiresRestart bool             `json:"requires_restart"`
}

func retentionHandler(cfg RetentionConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		aggregateOwner := "TimescaleDB retention policy"
		aggregateDescription := "Continuous avg/min/max/last per (host, metric, labels) in 5-minute buckets. Used for queries past the raw window."
		if cfg.ArchiveAggregate5m {
			aggregateOwner = "Cold archive job"
			aggregateDescription += " The archive job owns expiry: it disables the independent Timescale policy and drops chunks only after every eligible host upload and manifest succeeds."
		}
		resp := retentionResp{
			IntervalFormat:  `N second|minute|hour|day|week|month|year[s], or "forever" / empty`,
			RequiresRestart: true,
			Policies: []retentionEntry{
				{
					Key:         "raw",
					Label:       "Raw points (full resolution)",
					Target:      "metric_points",
					Env:         "RETENTION_RAW",
					Configured:  cfg.Raw,
					Default:     "30 days",
					Description: "Every sample at the agent's interval. Compressed after the delay below; dropped after this window.",
					Owner:       "TimescaleDB retention policy",
				},
				{
					Key:         "aggregate_5m",
					Label:       "5-minute aggregate",
					Target:      "metric_points_5m",
					Env:         "RETENTION_AGGREGATE_5M",
					Configured:  cfg.Aggregate5m,
					Default:     "6 months",
					Description: aggregateDescription,
					Owner:       aggregateOwner,
				},
				{
					Key:         "processes",
					Label:       "Process snapshots",
					Target:      "processes",
					Env:         "RETENTION_PROCESSES",
					Configured:  cfg.Processes,
					Default:     "7 days",
					Description: "Top-N process list per host per tick. Heaviest table per row; keep short unless you need process history.",
					Owner:       "TimescaleDB retention policy",
				},
				{
					Key:         "containers",
					Label:       "Container snapshots",
					Target:      "containers",
					Env:         "RETENTION_CONTAINERS",
					Configured:  cfg.Containers,
					Default:     "30 days",
					Description: "Docker container CPU / mem / I/O per tick. No-op on hosts without a Docker daemon.",
					Owner:       "TimescaleDB retention policy",
				},
				{
					Key:         "ports",
					Label:       "Listening port snapshots",
					Target:      "ports",
					Env:         "RETENTION_PORTS",
					Configured:  cfg.Ports,
					Default:     "30 days",
					Description: "Open listening sockets per host per tick. Uncompressed and one row per port, so this table grows fast on busy hosts.",
					Owner:       "TimescaleDB retention policy",
				},
				{
					Key:         "ipban_events",
					Label:       "IP ban history",
					Target:      "ipban_events",
					Env:         "RETENTION_IPBAN_EVENTS",
					Configured:  cfg.IPBanEvents,
					Default:     "90 days",
					Description: "Ban, unban and fleet-propagation events shown on the Security page. Swept by the server every minute; a plain table, not a hypertable.",
					Owner:       "Server sweeper",
				},
				{
					Key:         "compression",
					Label:       "Compression delay (raw)",
					Target:      "metric_points",
					Env:         "COMPRESSION_AFTER",
					Configured:  cfg.CompressAfter,
					Default:     "7 days",
					Description: "Chunks older than this get compressed (typically 10-15x smaller, slightly slower to query).",
					Owner:       "TimescaleDB compression policy",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
