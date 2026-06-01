package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type RetentionConfig struct {
	Raw           string
	Aggregate5m   string
	Processes     string
	Containers    string
	CompressAfter string

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
}

type retentionResp struct {
	Policies        []retentionEntry `json:"policies"`
	IntervalFormat  string           `json:"interval_format"`
	RequiresRestart bool             `json:"requires_restart"`
}

func retentionHandler(cfg RetentionConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
				},
				{
					Key:         "aggregate_5m",
					Label:       "5-minute aggregate",
					Target:      "metric_points_5m",
					Env:         "RETENTION_AGGREGATE_5M",
					Configured:  cfg.Aggregate5m,
					Default:     "6 months",
					Description: "Continuous avg/min/max/last per (host, metric, labels) in 5-minute buckets. Used for queries past the raw window.",
				},
				{
					Key:         "processes",
					Label:       "Process snapshots",
					Target:      "processes",
					Env:         "RETENTION_PROCESSES",
					Configured:  cfg.Processes,
					Default:     "7 days",
					Description: "Top-N process list per host per tick. Heaviest table per row; keep short unless you need process history.",
				},
				{
					Key:         "containers",
					Label:       "Container snapshots",
					Target:      "containers",
					Env:         "RETENTION_CONTAINERS",
					Configured:  cfg.Containers,
					Default:     "30 days",
					Description: "Docker container CPU / mem / I/O per tick. No-op on hosts without a Docker daemon.",
				},
				{
					Key:         "compression",
					Label:       "Compression delay (raw)",
					Target:      "metric_points",
					Env:         "COMPRESSION_AFTER",
					Configured:  cfg.CompressAfter,
					Default:     "7 days",
					Description: "Chunks older than this get compressed (typically 10-15x smaller, slightly slower to query).",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
