package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RetentionPolicies struct {
	Raw           string
	Aggregate5m   string
	Processes     string
	Containers    string
	Ports         string
	CompressAfter string
}

type AppliedPolicy struct {
	Target string
	Kind   string
	Value  string
}

func ApplyRetentionPolicies(ctx context.Context, pool *pgxpool.Pool, p RetentionPolicies) ([]AppliedPolicy, error) {
	specs := []struct {
		target string
		kind   string
		value  string
	}{
		{"metric_points", "compression", p.CompressAfter},
		{"metric_points", "retention", p.Raw},
		{"metric_points_5m", "retention", p.Aggregate5m},
		{"processes", "retention", p.Processes},
		{"containers", "retention", p.Containers},
		{"ports", "retention", p.Ports},
	}

	applied := make([]AppliedPolicy, 0, len(specs))
	for _, s := range specs {
		removeFn := "remove_retention_policy"
		addFn := "add_retention_policy"
		if s.kind == "compression" {
			removeFn = "remove_compression_policy"
			addFn = "add_compression_policy"
		}

		removeSQL := fmt.Sprintf("SELECT %s($1, if_exists => true)", removeFn)
		if _, err := pool.Exec(ctx, removeSQL, s.target); err != nil {
			return applied, fmt.Errorf("remove %s on %s: %w", s.kind, s.target, err)
		}

		if isForever(s.value) {
			applied = append(applied, AppliedPolicy{Target: s.target, Kind: s.kind, Value: "forever"})
			continue
		}

		addSQL := fmt.Sprintf("SELECT %s($1, INTERVAL %s, if_not_exists => true)", addFn, quoteInterval(s.value))
		if _, err := pool.Exec(ctx, addSQL, s.target); err != nil {
			return applied, fmt.Errorf("add %s on %s: %w", s.kind, s.target, err)
		}
		applied = append(applied, AppliedPolicy{Target: s.target, Kind: s.kind, Value: s.value})
	}
	return applied, nil
}

func isForever(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || strings.EqualFold(v, "forever")
}

func quoteInterval(v string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(v), "'", "''") + "'"
}
