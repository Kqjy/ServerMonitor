package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL required")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	fmt.Println("=== TABLES ===")
	rows, err := pool.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema='public' AND table_type='BASE TABLE'
		ORDER BY table_name`)
	if err != nil {
		log.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		tables = append(tables, t)
	}
	rows.Close()
	for _, t := range tables {
		fmt.Println(t)
	}

	fmt.Println("\n=== COLUMNS ===")
	for _, t := range tables {
		fmt.Printf("\n[%s]\n", t)
		cols, err := pool.Query(ctx, `
			SELECT column_name, data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema='public' AND table_name=$1
			ORDER BY ordinal_position`, t)
		if err != nil {
			log.Fatal(err)
		}
		for cols.Next() {
			var name, dt, nullable string
			var def *string
			cols.Scan(&name, &dt, &nullable, &def)
			d := ""
			if def != nil {
				d = " default=" + *def
			}
			fmt.Printf("  %s %s nullable=%s%s\n", name, dt, nullable, d)
		}
		cols.Close()
	}

	fmt.Println("\n=== INDEXES ===")
	idxRows, err := pool.Query(ctx, `
		SELECT tablename, indexname, indexdef
		FROM pg_indexes
		WHERE schemaname='public'
		ORDER BY tablename, indexname`)
	if err != nil {
		log.Fatal(err)
	}
	for idxRows.Next() {
		var t, n, d string
		idxRows.Scan(&t, &n, &d)
		fmt.Printf("%-30s %s\n  %s\n", t, n, d)
	}
	idxRows.Close()

	fmt.Println("\n=== TIMESCALE HYPERTABLES ===")
	htRows, _ := pool.Query(ctx, `SELECT hypertable_name, compression_enabled FROM timescaledb_information.hypertables ORDER BY hypertable_name`)
	for htRows.Next() {
		var n string
		var c bool
		htRows.Scan(&n, &c)
		fmt.Printf("%s compression=%v\n", n, c)
	}
	htRows.Close()

	fmt.Println("\n=== TIMESCALE JOBS (all) ===")
	jobRows, _ := pool.Query(ctx, `SELECT job_id, application_name, proc_name, hypertable_name FROM timescaledb_information.jobs ORDER BY job_id`)
	for jobRows.Next() {
		var id int
		var app, proc, ht *string
		jobRows.Scan(&id, &app, &proc, &ht)
		a, p, h := "", "", ""
		if app != nil {
			a = *app
		}
		if proc != nil {
			p = *proc
		}
		if ht != nil {
			h = *ht
		}
		fmt.Printf("%-6d %-40s %-30s %s\n", id, a, p, h)
	}
	jobRows.Close()
}
