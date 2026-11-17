package migrate

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/db"
)

// tempDatabase creates an empty database next to the one in
// CLOUDOS_TEST_DATABASE_URL and drops it when the test ends, so the test never
// touches the running stack's data.
func tempDatabase(t *testing.T) string {
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
	name := fmt.Sprintf("cloudos_test_%d", time.Now().UnixNano())
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

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func extensionExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, tempDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	p, err := NewProvider(db.SQL(pool))
	if err != nil {
		t.Fatal(err)
	}

	check := func(step string, wantVersion int64, wantSchema bool) {
		t.Helper()
		v, err := p.GetDBVersion(ctx)
		if err != nil {
			t.Fatalf("%s: version: %v", step, err)
		}
		if v != wantVersion {
			t.Errorf("%s: version = %d, want %d", step, v, wantVersion)
		}
		if got := tableExists(t, pool, "worker_heartbeat"); got != wantSchema {
			t.Errorf("%s: worker_heartbeat exists = %v, want %v", step, got, wantSchema)
		}
		if got := extensionExists(t, pool, "vector"); got != wantSchema {
			t.Errorf("%s: vector extension exists = %v, want %v", step, got, wantSchema)
		}
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	check("up", 1, true)

	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	check("down", 0, false)

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	check("up again", 1, true)
}

func TestHeartbeatQuery(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, tempDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	p, err := NewProvider(db.SQL(pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO worker_heartbeat VALUES ('fresh', now()), ('stale', now() - interval '1 minute')`); err != nil {
		t.Fatal(err)
	}

	store := &db.Store{Pool: pool, Migrations: p}
	workers, err := store.RecentWorkers(ctx, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].ID != "fresh" {
		t.Errorf("recent workers = %+v, want only fresh", workers)
	}
}
