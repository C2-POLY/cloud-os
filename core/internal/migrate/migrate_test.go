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
	// Migration 7 refuses to go down while versions exist (TestVersionsMigrationKeepsVersions).
	{7, []string{"file_versions"}, `SELECT 1`},
	{8, []string{"tasks"}, `INSERT INTO tasks (kind) VALUES ('selftest')`},
	{9, []string{"shares", "share_grants"}, `INSERT INTO shares (token_hash, owner, file_id, expires_at)
		SELECT sha256('s'), 'enzo', id, now() FROM files WHERE path = 'a.txt';
		INSERT INTO share_grants (token_hash, share_id, expires_at) SELECT sha256('g'), id, now() FROM shares`},
	{10, []string{"thumbnails"}, `INSERT INTO thumbnails (sha256, size, mime, width, height, bytes)
		VALUES (sha256('a'), 256, 'image/webp', 1, 1, '\x00')`},
	// Migration 12 adds an index and a function, no table.
	{12, []string{}, `SELECT cloudos_fold('Été')`},
	{13, []string{"activity"}, `INSERT INTO activity (owner, kind, path) VALUES ('enzo', 'added', 'a.txt')`},
	{14, []string{"media_meta"}, `INSERT INTO media_meta (sha256, taken_at, taken_offset, latitude, longitude, width, height)
		VALUES (sha256('a'), '2024-07-14 18:03:22', 120, 48.85, 2.35, 4032, 3024)`},
	// Migration 15 adds a column to media_meta, no table.
	{15, []string{}, `UPDATE media_meta SET duration_ms = 42000`},
	{16, []string{"albums", "album_items"}, `INSERT INTO albums (owner, name) SELECT name, 'Bretagne' FROM accounts LIMIT 1`},
	{17, []string{"document_text"}, `INSERT INTO document_text (sha256, source, text, chars, truncated, pages, has_text)
		VALUES (sha256('a'), 'pdf', 'Contrat de location', 19, false, 1, false)`},
	// Migration 18 adds a generated column and its index to document_text, no table.
	{18, []string{}, `SELECT 1 FROM document_text WHERE tsv @@ websearch_to_tsquery('public.cloudos_fr', 'contrats')`},
	// Migration 19 lets document_text hold the text read by OCR, no table.
	{19, []string{}, `UPDATE document_text SET text = 'Facture', chars = 7, has_text = true, ocr_model = 'pp-ocrv5-latin';
		INSERT INTO document_text (sha256, source, text, chars, truncated, has_text, ocr_model)
		VALUES (sha256('b'), 'image', '', 0, false, false, 'pp-ocrv5-latin')`},
	{20, []string{"embedding_models", "photo_vectors_siglip2_b16_224"}, `INSERT INTO embedding_models (name, version, dims)
		VALUES ('siglip2-base-patch16-224', 'v1', 768);
		INSERT INTO photo_vectors_siglip2_b16_224 (sha256, model, version, source, embedding)
		SELECT sha256('a'), 'siglip2-base-patch16-224', 'v1', 'image', array_fill(0.036::real, ARRAY[768])::vector`},
	{21, []string{"text_vectors_arctic_m_v2"}, `INSERT INTO text_vectors_arctic_m_v2 (sha256, chunk, start_char, end_char, model, version, embedding)
		VALUES (sha256('a'), 0, 0, 19, 'arctic-embed-m-v2.0', 'v', array_fill(0.036, ARRAY[768])::halfvec)`},
	{22, []string{"document_categories", "file_categories"}, `INSERT INTO document_categories (sha256, category, score, margin, version)
		VALUES (sha256('a'), 'contrats', 0.45, 0.15, 'v');
		INSERT INTO file_categories (file_id, category) SELECT id, 'autre' FROM files WHERE path = 'a.txt'`},
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
	latest := tables[len(tables)-1].version
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
	for i := len(tables) - 1; i >= 0; i-- {
		if _, err := p.Down(ctx); err != nil {
			t.Fatalf("down %d: %v", tables[i].version, err)
		}
		prev := int64(0)
		if i > 0 {
			prev = tables[i-1].version
		}
		check(fmt.Sprintf("down %d", tables[i].version), prev)
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

// TestDocumentSearchMigration checks that migration 18 indexes the text
// already extracted, and that undoing it keeps every text.
func TestDocumentSearchMigration(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 17); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO document_text (sha256, source, text, chars, truncated, pages, has_text)
		VALUES (sha256('a'), 'text', 'Facture d''ÉLECTRICITÉ', 21, false, NULL, true)`); err != nil {
		t.Fatal(err)
	}
	found := func() bool {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_text
			WHERE tsv @@ websearch_to_tsquery('public.cloudos_fr', 'factures electricite')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	for _, step := range []string{"up", "up again"} {
		if _, err := p.UpTo(ctx, 18); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if !found() {
			t.Errorf("%s: the text already there is not indexed", step)
		}
		if _, err := p.DownTo(ctx, 17); err != nil {
			t.Fatalf("down: %v", err)
		}
		var text string
		if err := pool.QueryRow(ctx, `SELECT text FROM document_text`).Scan(&text); err != nil || text != "Facture d'ÉLECTRICITÉ" {
			t.Errorf("after down: %q (%v)", text, err)
		}
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
	if _, err := p.DownTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 5); err == nil {
		t.Fatal("migration 6 went down with an item in the trash")
	}
	if v, _ := p.GetDBVersion(ctx); v != 6 {
		t.Errorf("version after the refused down: %d", v)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM trash`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 5); err != nil {
		t.Errorf("down with an empty trash: %v", err)
	}
}

// TestVersionsMigrationKeepsVersions checks that undoing migration 7 is
// refused while a version exists.
func TestVersionsMigrationKeepsVersions(t *testing.T) {
	ctx := context.Background()
	pool, p := testdb.Migrated(t)
	if _, err := pool.Exec(ctx, `INSERT INTO files (owner, path, size, mime, sha256, modified_at) VALUES ('enzo', 'a.txt', 1, 'text/plain', sha256('a'), now());
		INSERT INTO file_versions (file_id, owner, size, mime, sha256, modified_at) SELECT id, owner, size, mime, sha256, modified_at FROM files`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 6); err == nil {
		t.Fatal("migration 7 went down with a version")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM file_versions`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 6); err != nil {
		t.Errorf("down without versions: %v", err)
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
