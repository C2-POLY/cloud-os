package migrate_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// latest is the number of the last migration in core/migrations.
const latest = 2

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

// TestMigrationsUpDownUp applies every migration, undoes them one by one with
// data in the tables, and applies them again.
func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	p, err := migrate.NewProvider(db.SQL(pool))
	if err != nil {
		t.Fatal(err)
	}

	type schema struct{ vector, heartbeat, files bool }
	check := func(step string, wantVersion int64, want schema) {
		t.Helper()
		v, err := p.GetDBVersion(ctx)
		if err != nil {
			t.Fatalf("%s: version: %v", step, err)
		}
		if v != wantVersion {
			t.Errorf("%s: version = %d, want %d", step, v, wantVersion)
		}
		got := schema{extensionExists(t, pool, "vector"), tableExists(t, pool, "worker_heartbeat"), tableExists(t, pool, "files")}
		if got != want {
			t.Errorf("%s: schema = %+v, want %+v", step, got, want)
		}
	}
	all := schema{true, true, true}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	check("up", latest, all)

	if _, err := pool.Exec(ctx, `INSERT INTO worker_heartbeat VALUES ('w', now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO files (owner, path, size, mime, sha256, modified_at)
		VALUES ('enzo', 'a.txt', 1, 'text/plain', sha256('a'), now())`); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("down 0002: %v", err)
	}
	check("down 0002", 1, schema{true, true, false})

	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down 0001: %v", err)
	}
	check("down 0001", 0, schema{})

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	check("up again", latest, all)
}

func TestHeartbeatQuery(t *testing.T) {
	ctx := context.Background()
	pool, p := testdb.Migrated(t)
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
