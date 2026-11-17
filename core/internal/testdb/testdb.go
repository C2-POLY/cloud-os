// Package testdb gives each test its own throwaway Postgres database, created
// next to the one in CLOUDOS_TEST_DATABASE_URL and dropped when the test ends,
// so tests never touch a running stack's data.
package testdb

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
)

// URL creates an empty database and returns its URL. The test is skipped, with
// a message saying how to run it, when CLOUDOS_TEST_DATABASE_URL is not set.
func URL(t testing.TB) string {
	t.Helper()
	admin := os.Getenv("CLOUDOS_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("CLOUDOS_TEST_DATABASE_URL is not set: run `make test`, which starts the database")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Test packages run in parallel processes: the time alone can repeat.
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("cloudos_test_%d_%x", time.Now().UnixNano(), suffix)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database: %v", err)
		}
		conn.Close(ctx)
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Migrated returns a pool on a new database with every migration applied.
func Migrated(t testing.TB) (*pgxpool.Pool, *goose.Provider) {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	p, err := migrate.NewProvider(db.SQL(pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool, p
}
