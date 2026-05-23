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

	stmts := []string{
		"DROP EXTENSION IF EXISTS timescaledb CASCADE",
		"DROP SCHEMA public CASCADE",
		"CREATE SCHEMA public",
		"GRANT ALL ON SCHEMA public TO public",
	}
	for _, s := range stmts {
		if _, err := pool.Exec(context.Background(), s); err != nil {
			log.Fatalf("%s: %v", s, err)
		}
		fmt.Println("ok:", s)
	}
}
