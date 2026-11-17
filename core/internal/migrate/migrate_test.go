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
	{4, []string{"moves"}, `INSERT INTO files (owner, path, kind, size, mime, modified_at)
		VALUES ('enzo', 'Photos', 'dir', 0, 'inode/directory', now());
		INSERT INTO moves (owner, from_path, to_path) VALUES ('enzo', 'a', 'b')`},
	{5, []string{"uploads"}, `INSERT INTO uploads (id, owner, path, length, expires_at)
		VALUES ('0123456789abcdef0123456789abcdef', 'enzo', 'video.mp4', 10, now())`},
	// Migration 6 refuses to go down while the trash holds anything (TestTrashMigrationKeepsItems).
	{6, []string{"trash"}, `SELECT 1`},
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

// TestFolderBackfill checks that migration 4 gives every existing file's
// parent folders their own rows, and that undoing it keeps every file row.
func TestFolderBackfill(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO files (owner, path, size, mime, sha256, modified_at) VALUES
		('enzo', 'Photos/2024/plage.jpg', 1, 'image/jpeg', sha256('a'), now()),
		('enzo', 'Photos/chien.jpg', 1, 'image/jpeg', sha256('b'), now()),
		('enzo', 'racine.txt', 1, 'text/plain', sha256('c'), now()),
		('johan', 'Photos/x.jpg', 1, 'image/jpeg', sha256('d'), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 4); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT owner || ':' || path FROM files WHERE kind = 'dir' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	want := []string{"enzo:Photos", "enzo:Photos/2024", "johan:Photos"}
	if fmt.Sprint(dirs) != fmt.Sprint(want) {
		t.Errorf("folders %v, want %v", dirs, want)
	}
	if _, err := p.DownTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	var files int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files`).Scan(&files); err != nil || files != 4 {
		t.Errorf("after down: %d file rows (%v), want the 4 files", files, err)
	}
}

// TestTrashMigrationKeepsItems checks that undoing migration 6 is refused
// while the trash holds an item, and allowed once it is empty.
func TestTrashMigrationKeepsItems(t *testing.T) {
	ctx := context.Background()
	pool, p := testdb.Migrated(t)
	if _, err := pool.Exec(ctx, `INSERT INTO trash (owner, path, kind, size, expires_at) VALUES ('enzo', 'a.txt', 'file', 1, now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 5); err == nil {
		t.Fatal("migration 6 went down with an item in the trash")
	}
	if v, _ := p.GetDBVersion(ctx); v != int64(len(tables)) {
		t.Errorf("version after the refused down: %d", v)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM trash`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 5); err != nil {
		t.Errorf("down with an empty trash: %v", err)
	}
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
