package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Println("DATABASE_URL not set")
		os.Exit(1)
	}
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(1)
	}
	defer c.Close(ctx)

	queries := []struct {
		label string
		sql   string
	}{
		{"hosts", `SELECT id, hostname, sample_interval_s, last_seen FROM hosts ORDER BY id`},
		{"db_total_size", `SELECT pg_size_pretty(pg_database_size(current_database()))`},
		{"hypertable_total_sizes", `
			SELECT hypertable_name,
			       pg_size_pretty(hypertable_size(format('%I.%I', hypertable_schema, hypertable_name)::regclass)) AS total
			FROM timescaledb_information.hypertables
			ORDER BY hypertable_name`},
		{"compression_ratio_per_table", `
			SELECT hypertable_name,
			       pg_size_pretty(COALESCE(sum(before_compression_total_bytes), 0)) AS before_compress,
			       pg_size_pretty(COALESCE(sum(after_compression_total_bytes), 0)) AS after_compress,
			       COALESCE(round(sum(before_compression_total_bytes)::numeric / NULLIF(sum(after_compression_total_bytes), 0), 2), 0) AS ratio_x
			FROM timescaledb_information.hypertables h
			LEFT JOIN LATERAL (
			    SELECT * FROM hypertable_compression_stats(format('%I.%I', h.hypertable_schema, h.hypertable_name)::regclass)
			) s ON true
			GROUP BY hypertable_name
			ORDER BY hypertable_name`},
		{"chunks_per_table", `
			SELECT hypertable_name,
			       count(*) FILTER (WHERE NOT is_compressed) AS uncompressed_chunks,
			       count(*) FILTER (WHERE is_compressed) AS compressed_chunks,
			       count(*) AS total_chunks
			FROM timescaledb_information.chunks
			GROUP BY hypertable_name
			ORDER BY hypertable_name`},
		{"metric_points_count_and_span", `
			SELECT count(*) AS rows,
			       min(time) AS first,
			       max(time) AS last,
			       max(time) - min(time) AS span
			FROM metric_points`},
		{"points_per_metric_top10", `
			SELECT metric, count(*) AS rows
			FROM metric_points
			GROUP BY metric
			ORDER BY rows DESC
			LIMIT 10`},
		{"ingest_rate_last_5min", `
			SELECT count(*) AS rows_last_5min,
			       count(DISTINCT host_id) AS hosts,
			       round((count(*) / 300.0)::numeric, 2) AS points_per_sec
			FROM metric_points
			WHERE time > now() - INTERVAL '5 minutes'`},
		{"ingest_rate_last_hour", `
			SELECT count(*) AS rows_last_hour,
			       count(DISTINCT host_id) AS hosts,
			       round((count(*) / 3600.0)::numeric, 2) AS points_per_sec
			FROM metric_points
			WHERE time > now() - INTERVAL '1 hour'`},
		{"avg_bytes_per_point_uncompressed", `
			SELECT round((pg_relation_size(c.oid) / NULLIF(count(*), 0))::numeric, 1) AS bytes_per_point
			FROM metric_points mp
			JOIN pg_class c ON c.relname = '_hyper_1_1_chunk'
			WHERE mp.time > now() - INTERVAL '1 day'`},
		{"retention_compression_jobs", `
			SELECT j.application_name, j.proc_name, j.schedule_interval, j.config
			FROM timescaledb_information.jobs j
			WHERE j.proc_name IN ('policy_retention', 'policy_compression')
			ORDER BY j.application_name`},
	}

	for _, q := range queries {
		fmt.Printf("\n=== %s ===\n", q.label)
		rows, err := c.Query(ctx, q.sql)
		if err != nil {
			fmt.Println("ERR:", err)
			continue
		}
		fields := rows.FieldDescriptions()
		for _, f := range fields {
			fmt.Printf("%s\t", f.Name)
		}
		fmt.Println()
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				fmt.Println("row err:", err)
				continue
			}
			for _, v := range vals {
				fmt.Printf("%v\t", v)
			}
			fmt.Println()
		}
		rows.Close()
	}
}
