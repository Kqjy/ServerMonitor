package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"servermonitor/internal/server/storage"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpdateChannelPreservesPassword(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed channel update test")
	}
	if err := storage.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	name := fmt.Sprintf("_smtest_chan_%d", os.Getpid())
	_, _ = pool.Exec(ctx, `DELETE FROM notification_channels WHERE name=$1`, name)
	var id int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO notification_channels (name, kind, config, enabled)
		VALUES ($1, 'smtp', $2, true) RETURNING id
	`, name, []byte(`{"host":"mail","password":"orig-secret","from":"a@b.c"}`)).Scan(&id); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM notification_channels WHERE id=$1`, id) })

	readConfig := func() map[string]any {
		var cfg []byte
		if err := pool.QueryRow(ctx, `SELECT config FROM notification_channels WHERE id=$1`, id).Scan(&cfg); err != nil {
			t.Fatalf("read config: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(cfg, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	blank := []byte(`{"host":"mail2","password":"","from":"a@b.c"}`)
	if _, err := pool.Exec(ctx, updateChannelSQL, id, name, "smtp", blank, true); err != nil {
		t.Fatalf("update blank: %v", err)
	}
	m := readConfig()
	if m["password"] != "orig-secret" {
		t.Fatalf("blank password must preserve stored secret; got %v", m["password"])
	}
	if m["host"] != "mail2" {
		t.Fatalf("non-secret fields must still update in preserve mode; host = %v", m["host"])
	}

	withNew := []byte(`{"host":"mail2","password":"new-secret","from":"a@b.c"}`)
	if _, err := pool.Exec(ctx, updateChannelSQL, id, name, "smtp", withNew, true); err != nil {
		t.Fatalf("update new: %v", err)
	}
	if m := readConfig(); m["password"] != "new-secret" {
		t.Fatalf("non-blank password must replace; got %v", m["password"])
	}
}
