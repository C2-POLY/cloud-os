package migrate_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// tables lists, for each migration, the tables it creates. Add a line with
// every new migration: the test undoes them one by one, with data in place.
var tables = []struct {
	version int64
	names   []string
	seed    string
}{
	{1, []string{"worker_heartbeat"}, `INSERT INTO worker_heartbeat VALUES ('w', now())`},
	{2, []string{"files"}, `INSERT INTO files (owner, path, size, mime, sha256, modified_at)
		VALUES ('enzo', 'a.txt', 1, 'text/plain', sha256('a'), now())`},
	{3, []string{"accounts", "sessions"}, `INSERT INTO accounts (name, display_name, password_hash, role)
		VALUES ('enzo', 'Enzo', 'x', 'admin');
		INSERT INTO sessions (token_hash, account, expires_at) VALUES (sha256('t'), 'enzo', now())`},
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
	sources := p.ListSources()
	latest := int64(len(tables))
	if len(sources) != len(tables) {
		t.Fatalf("%d migrations, but the test knows %d: add the new one to tables", len(sources), len(tables))
	}

	// check verifies the schema at version v: the tables of migrations up to v exist, the others do not.
	check := func(step string, v int64) {
		t.Helper()
		got, err := p.GetDBVersion(ctx)
		if err != nil || got != v {
			t.Fatalf("%s: version %d (%v), want %d", step, got, err, v)
		}
		for _, m := range tables {
			for _, name := range m.names {
				if exists := tableExists(t, pool, name); exists != (m.version <= v) {
					t.Errorf("%s: table %s exists = %v at version %d", step, name, exists, v)
				}
			}
		}
		if exists := extensionExists(t, pool, "vector"); exists != (v >= 1) {
			t.Errorf("%s: vector extension exists = %v at version %d", step, exists, v)
		}
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	check("up", latest)
	for _, m := range tables {
		if _, err := pool.Exec(ctx, m.seed); err != nil {
			t.Fatalf("seed %d: %v", m.version, err)
		}
	}
	for v := latest; v > 0; v-- {
		if _, err := p.Down(ctx); err != nil {
			t.Fatalf("down %d: %v", v, err)
		}
		check(fmt.Sprintf("down %d", v), v-1)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	check("up again", latest)
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
