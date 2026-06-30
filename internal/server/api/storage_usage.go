package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"servermonitor/internal/server/storage"
)

type storageTable struct {
	Schema            string  `json:"schema"`
	Name              string  `json:"name"`
	Label             string  `json:"label"`
	Kind              string  `json:"kind"`
	TotalBytes        int64   `json:"total_bytes"`
	TableBytes        int64   `json:"table_bytes"`
	IndexBytes        int64   `json:"index_bytes"`
	ApproxRows        int64   `json:"approx_rows"`
	Chunks            int64   `json:"chunks"`
	CompressedChunks  int64   `json:"compressed_chunks"`
	UncompressedBytes int64   `json:"uncompressed_bytes"`
	CompressionRatio  float64 `json:"compression_ratio"`
}

type storageArchive struct {
	Configured bool       `json:"configured"`
	Objects    int64      `json:"objects"`
	TotalBytes int64      `json:"total_bytes"`
	RowCount   int64      `json:"row_count"`
	Oldest     *time.Time `json:"oldest"`
	Newest     *time.Time `json:"newest"`
}

type storageResp struct {
	CapturedAt         time.Time      `json:"captured_at"`
	DatabaseBytes      int64          `json:"database_bytes"`
	TablesTotalBytes   int64          `json:"tables_total_bytes"`
	OtherDatabaseBytes int64          `json:"other_database_bytes"`
	Tables             []storageTable `json:"tables"`
	Archive            storageArchive `json:"archive"`
	Warnings           []string       `json:"warnings,omitempty"`
}

type storageObject struct {
	schema string
	name   string
	ca     bool
}

func (o storageObject) qualified() string { return o.schema + "." + o.name }

var storageTableMeta = map[string]struct{ label, kind string }{
	"metric_points":    {"Raw metrics", "Raw points"},
	"metric_points_5m": {"5-minute aggregate", "Continuous aggregate"},
	"processes":        {"Process snapshots", "Snapshot"},
	"containers":       {"Container snapshots", "Snapshot"},
	"ports":            {"Open-port snapshots", "Snapshot"},
}

func storageUsageHandler(db *storage.DB, archiveConfigured bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		pool := db.Pool

		resp := storageResp{CapturedAt: time.Now().UTC()}
		warn := func(format string, args ...any) {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf(format, args...))
		}

		if err := pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&resp.DatabaseBytes); err != nil {
			warn("database size unavailable: %v", err)
		}

		objects, err := collectStorageObjects(ctx, pool)
		if err != nil {
			warn("could not enumerate Timescale tables: %v", err)
		}

		for _, obj := range objects {
			t := storageTable{Schema: obj.schema, Name: obj.name}
			if m, ok := storageTableMeta[obj.name]; ok {
				t.Label, t.Kind = m.label, m.kind
			} else {
				t.Label = obj.name
				if obj.ca {
					t.Kind = "Continuous aggregate"
				} else {
					t.Kind = "Hypertable"
				}
			}

			var toast int64
			if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(table_bytes),0), COALESCE(sum(index_bytes),0), COALESCE(sum(toast_bytes),0), COALESCE(sum(total_bytes),0) FROM hypertable_detailed_size($1)`, obj.qualified()).
				Scan(&t.TableBytes, &t.IndexBytes, &toast, &t.TotalBytes); err != nil {
				warn("size for %s unavailable: %v", obj.name, err)
			}
			t.TableBytes += toast
			if err := pool.QueryRow(ctx, `SELECT approximate_row_count($1)`, obj.qualified()).Scan(&t.ApproxRows); err != nil {
				warn("row count for %s unavailable: %v", obj.name, err)
			}

			if !obj.ca {
				if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE is_compressed) FROM timescaledb_information.chunks WHERE hypertable_schema=$1 AND hypertable_name=$2`, obj.schema, obj.name).
					Scan(&t.Chunks, &t.CompressedChunks); err != nil {
					warn("chunk stats for %s unavailable: %v", obj.name, err)
				}
				if t.CompressedChunks > 0 {
					var before, after int64
					if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(before_compression_total_bytes),0), COALESCE(sum(after_compression_total_bytes),0) FROM hypertable_compression_stats($1)`, obj.qualified()).
						Scan(&before, &after); err != nil {
						warn("compression stats for %s unavailable: %v", obj.name, err)
					} else if after > 0 {
						t.UncompressedBytes = before
						t.CompressionRatio = float64(before) / float64(after)
					}
				}
			}

			resp.Tables = append(resp.Tables, t)
			resp.TablesTotalBytes += t.TotalBytes
		}
		sort.SliceStable(resp.Tables, func(i, j int) bool {
			return resp.Tables[i].TotalBytes > resp.Tables[j].TotalBytes
		})

		if resp.DatabaseBytes > resp.TablesTotalBytes {
			resp.OtherDatabaseBytes = resp.DatabaseBytes - resp.TablesTotalBytes
		}

		resp.Archive.Configured = archiveConfigured
		if err := pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(byte_size),0), COALESCE(sum(row_count),0), min(bucket_from), max(bucket_to) FROM archive_manifests`).
			Scan(&resp.Archive.Objects, &resp.Archive.TotalBytes, &resp.Archive.RowCount, &resp.Archive.Oldest, &resp.Archive.Newest); err != nil {
			warn("archive stats unavailable: %v", err)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

func collectStorageObjects(ctx context.Context, pool *pgxpool.Pool) ([]storageObject, error) {
	var out []storageObject
	gather := func(query string, ca bool) error {
		rows, err := pool.Query(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var schema, name string
			if err := rows.Scan(&schema, &name); err != nil {
				return err
			}
			out = append(out, storageObject{schema: schema, name: name, ca: ca})
		}
		return rows.Err()
	}
	if err := gather(`SELECT hypertable_schema, hypertable_name FROM timescaledb_information.hypertables WHERE hypertable_schema = 'public' ORDER BY hypertable_name`, false); err != nil {
		return out, err
	}
	if err := gather(`SELECT view_schema, view_name FROM timescaledb_information.continuous_aggregates WHERE view_schema = 'public' ORDER BY view_name`, true); err != nil {
		return out, err
	}
	return out, nil
}
