package migrate_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	// Migration 22 fills search_words from the texts already there, then a trigger keeps it.
	{22, []string{"search_words"}, `INSERT INTO document_text (sha256, source, text, chars, truncated, has_text)
		VALUES (sha256('c'), 'text', 'Warhallow', 9, false, true)`},
	{23, []string{"document_categories", "file_categories"}, `INSERT INTO document_categories (sha256, category, score, margin, version)
		VALUES (sha256('a'), 'contrats', 0.45, 0.15, 'v');
		INSERT INTO file_categories (file_id, category) SELECT id, 'autre' FROM files WHERE path = 'a.txt'`},
	// Migration 24 only adds an index on the tasks of a content.
	{24, []string{"tasks_kind_sha"}, `INSERT INTO tasks (kind, payload) VALUES ('ocr', '{"sha256": "00"}');
		SELECT 1 FROM tasks WHERE kind = 'ocr' AND payload ->> 'sha256' = '00'`},
	{25, []string{"smb_passwords"}, `INSERT INTO smb_passwords (account, nt_hash, last_used_at)
		VALUES ('enzo', decode('a4f49c406510bdcab6824ee7c30fd852', 'hex'), now())`},
	// Migration 26 creates the smb service's role and grants it rights, no table (TestSMBRoleMigration).
	{26, []string{}, `SELECT 1`},
	// Migration 27 grants that role storage's SMB writes and journals op 'replace', no table (TestSMBWriteMigration).
	{27, []string{}, `SELECT 1`},
	// Migration 28 creates the backup's run history and its role (TestBackupMigration).
	{28, []string{"backup_runs"}, `INSERT INTO backup_runs (kind, detail) VALUES ('backup', '{"files": 3}');
		UPDATE backup_runs SET finished_at = now(), ok = true`},
	// Migration 29 gives the trash a reason and journals op 'rescue', no table (TestSMBRescueMigration).
	{29, []string{}, `SELECT 1`},
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

// smbRights says what the role cloudos_smb may do in this database: false
// for everything when the role does not exist.
func smbRights(t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	checks := map[string]string{
		"select files":                   `has_table_privilege('cloudos_smb', 'files', 'SELECT')`,
		"select goose_db_version":        `has_table_privilege('cloudos_smb', 'goose_db_version', 'SELECT')`,
		"select accounts.name":           `has_column_privilege('cloudos_smb', 'accounts', 'name', 'SELECT')`,
		"select accounts.disabled_at":    `has_column_privilege('cloudos_smb', 'accounts', 'disabled_at', 'SELECT')`,
		"select accounts.password_hash":  `has_column_privilege('cloudos_smb', 'accounts', 'password_hash', 'SELECT')`,
		"select smb_passwords.nt_hash":   `has_column_privilege('cloudos_smb', 'smb_passwords', 'nt_hash', 'SELECT')`,
		"update smb_passwords.last_used": `has_column_privilege('cloudos_smb', 'smb_passwords', 'last_used_at', 'UPDATE')`,
		"update smb_passwords.nt_hash":   `has_column_privilege('cloudos_smb', 'smb_passwords', 'nt_hash', 'UPDATE')`,
		"write files":                    `has_table_privilege('cloudos_smb', 'files', 'INSERT,UPDATE,DELETE,TRUNCATE')`,
		"write accounts":                 `has_table_privilege('cloudos_smb', 'accounts', 'INSERT,UPDATE,DELETE,TRUNCATE')`,
		"select sessions":                `has_table_privilege('cloudos_smb', 'sessions', 'SELECT')`,
	}
	out := map[string]bool{}
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cloudos_smb')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	for name, expr := range checks {
		if !exists {
			out[name] = false
			continue
		}
		var ok bool
		if err := pool.QueryRow(context.Background(), "SELECT "+expr).Scan(&ok); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = ok
	}
	return out
}

// TestSMBRoleMigration checks that migration 26 gives the smb service's role
// exactly what it reads and the one column it writes, and that undoing it
// takes the rights back without touching a row.
func TestSMBRoleMigration(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (name, display_name, password_hash, role) VALUES ('enzo', 'Enzo', 'x', 'admin');
		INSERT INTO smb_passwords (account, nt_hash) VALUES ('enzo', decode('a4f49c406510bdcab6824ee7c30fd852', 'hex'))`); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"select files": true, "select goose_db_version": true,
		"select accounts.name": true, "select accounts.disabled_at": true, "select accounts.password_hash": false,
		"select smb_passwords.nt_hash": true, "update smb_passwords.last_used": true, "update smb_passwords.nt_hash": false,
		"write files": false, "write accounts": false, "select sessions": false,
	}
	none := map[string]bool{}
	for k := range want {
		none[k] = false
	}
	expect := func(step string, want map[string]bool) {
		t.Helper()
		got := smbRights(t, pool)
		for k, w := range want {
			if got[k] != w {
				t.Errorf("%s: %s = %v, want %v", step, k, got[k], w)
			}
		}
	}

	if _, err := p.UpTo(ctx, 26); err != nil {
		t.Fatalf("up 26: %v", err)
	}
	expect("up", want)
	var attrs string
	if err := pool.QueryRow(ctx, `SELECT format('%s %s %s %s %s %s', rolsuper, rolcreaterole, rolcreatedb, rolbypassrls, rolinherit, rolconnlimit)
		FROM pg_roles WHERE rolname = 'cloudos_smb'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if attrs != "f f f f f 8" {
		t.Errorf("role attributes (super, createrole, createdb, bypassrls, inherit, connection limit) = %s", attrs)
	}

	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("down 26: %v", err)
	}
	expect("down", none)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM smb_passwords`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("smb_passwords after down: %d rows, %v", n, err)
	}

	if _, err := p.UpTo(ctx, 26); err != nil {
		t.Fatalf("up 26 again: %v", err)
	}
	expect("up again", want)
}

// smbWrites lists every write cloudos_smb may make: "INSERT table.column",
// "UPDATE table.column", "DELETE table", "TRUNCATE table".
func smbWrites(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT p.priv || ' ' || c.relname || '.' || a.attname
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		   CROSS JOIN (VALUES ('INSERT'), ('UPDATE')) p(priv)
		  WHERE c.relkind = 'r' AND has_column_privilege('cloudos_smb', c.oid, a.attnum, p.priv)
		 UNION ALL
		 SELECT p.priv || ' ' || c.relname
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		   CROSS JOIN (VALUES ('DELETE'), ('TRUNCATE')) p(priv)
		  WHERE c.relkind = 'r' AND has_table_privilege('cloudos_smb', c.oid, p.priv)
		 ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSMBWriteMigration checks that migration 27 grants the smb service's
// role exactly storage's SMB writes, and the reads of the tasks it queues;
// that undoing it is refused while a replace is journaled, then takes the
// rights back without touching a row; and that it applies again.
func TestSMBWriteMigration(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (name, display_name, password_hash, role) VALUES ('enzo', 'Enzo', 'x', 'admin');
		INSERT INTO smb_passwords (account, nt_hash) VALUES ('enzo', decode('a4f49c406510bdcab6824ee7c30fd852', 'hex'));
		INSERT INTO files (owner, path, size, mime, sha256, modified_at) VALUES ('enzo', 'a.txt', 1, 'text/plain', sha256('a'), now())`); err != nil {
		t.Fatal(err)
	}
	before := smbWrites(t, pool)
	if fmt.Sprint(before) != "[UPDATE smb_passwords.last_used_at]" {
		t.Fatalf("before 27, cloudos_smb writes %v", before)
	}

	want := []string{
		"DELETE file_versions", "DELETE files", "DELETE moves", "DELETE trash",
		"INSERT activity.kind", "INSERT activity.owner", "INSERT activity.path",
		"INSERT file_versions.file_id", "INSERT file_versions.mime", "INSERT file_versions.modified_at",
		"INSERT file_versions.owner", "INSERT file_versions.sha256", "INSERT file_versions.size",
		"INSERT files.kind", "INSERT files.mime", "INSERT files.modified_at", "INSERT files.owner",
		"INSERT files.path", "INSERT files.sha256", "INSERT files.size",
		"INSERT moves.from_path", "INSERT moves.op", "INSERT moves.owner", "INSERT moves.to_path",
		"INSERT moves.trash_id", "INSERT moves.version_id",
		"INSERT tasks.dedupe_key", "INSERT tasks.kind", "INSERT tasks.max_attempts", "INSERT tasks.payload", "INSERT tasks.priority",
		"INSERT trash.expires_at", "INSERT trash.kind", "INSERT trash.owner", "INSERT trash.path", "INSERT trash.size",
		"UPDATE files.indexed_at", "UPDATE files.mime", "UPDATE files.modified_at", "UPDATE files.path",
		"UPDATE files.sha256", "UPDATE files.size", "UPDATE files.trash_id",
		"UPDATE smb_passwords.last_used_at",
	}
	reads := map[string]bool{
		"thumbnails.sha256": true, "thumbnails.bytes": false, "media_meta.sha256": true, "media_meta.latitude": false,
		"document_text.sha256": true, "document_text.text": false, "embedding_models.version": true,
		"photo_vectors_siglip2_b16_224.sha256": true, "photo_vectors_siglip2_b16_224.embedding": false,
		"tasks.id": true, "tasks.state": true, "tasks.dedupe_key": true, "tasks.payload": false, "tasks.last_error": false, "accounts.password_hash": false, "sessions.token_hash": false,
		"activity.path": false,
	}
	check := func(step string, writes []string, granted bool) {
		t.Helper()
		if got := smbWrites(t, pool); fmt.Sprint(got) != fmt.Sprint(writes) {
			t.Errorf("%s: cloudos_smb writes\n%v\nwant\n%v", step, got, writes)
		}
		for col, w := range reads {
			table, column, _ := strings.Cut(col, ".")
			var ok bool
			if err := pool.QueryRow(ctx, `SELECT has_column_privilege('cloudos_smb', $1, $2, 'SELECT')`, table, column).Scan(&ok); err != nil {
				t.Fatal(err)
			}
			if ok != (w && granted) {
				t.Errorf("%s: SELECT %s = %v", step, col, ok)
			}
		}
	}

	if _, err := p.UpTo(ctx, 27); err != nil {
		t.Fatalf("up 27: %v", err)
	}
	check("up", want, true)

	// A replace in the journal holds the migration up: the core settles it first
	if _, err := pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op) VALUES ('enzo', 'b.txt', 'a.txt', 'replace')`); err != nil {
		t.Fatalf("journal a replace: %v", err)
	}
	if _, err := p.Down(ctx); err == nil {
		t.Fatal("down 27 with a replace in the journal was not refused")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM moves WHERE op = 'replace'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("down 27: %v", err)
	}
	check("down", before, false)
	var files, passwords int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM files), (SELECT count(*) FROM smb_passwords)`).Scan(&files, &passwords); err != nil || files != 1 || passwords != 1 {
		t.Fatalf("after down: %d files, %d SMB passwords (%v)", files, passwords, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op) VALUES ('enzo', 'b.txt', 'a.txt', 'replace')`); err == nil {
		t.Error("after down, the journal still takes op 'replace'")
	}

	if _, err := p.UpTo(ctx, 27); err != nil {
		t.Fatalf("up 27 again: %v", err)
	}
	check("up again", want, true)
}

// roleWrites lists every write role may make in this database: "INSERT
// table.column", "UPDATE table.column", "DELETE table", "TRUNCATE table".
func roleWrites(t *testing.T, pool *pgxpool.Pool, role string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT p.priv || ' ' || c.relname || '.' || a.attname
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		   CROSS JOIN (VALUES ('INSERT'), ('UPDATE')) p(priv)
		  WHERE c.relkind = 'r' AND has_column_privilege($1, c.oid, a.attnum, p.priv)
		 UNION ALL
		 SELECT p.priv || ' ' || c.relname
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		   CROSS JOIN (VALUES ('DELETE'), ('TRUNCATE')) p(priv)
		  WHERE c.relkind = 'r' AND has_table_privilege($1, c.oid, p.priv)
		 ORDER BY 1`, role)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestBackupMigration checks that migration 28 creates backup_runs and the
// backup's role, which reads every table (pg_dump) and writes backup_runs
// only, the columns of a run's row; that the role logs in as that, writes
// nothing else; and that undoing it drops the history, the files untouched.
func TestBackupMigration(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 27); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (name, display_name, password_hash, role) VALUES ('enzo', 'Enzo', 'x', 'admin');
		INSERT INTO files (owner, path, size, mime, sha256, modified_at) VALUES ('enzo', 'a.txt', 1, 'text/plain', sha256('a'), now())`); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"INSERT backup_runs.detail", "INSERT backup_runs.kind",
		"UPDATE backup_runs.detail", "UPDATE backup_runs.error", "UPDATE backup_runs.finished_at", "UPDATE backup_runs.ok",
	}
	check := func(step string) {
		t.Helper()
		if got := roleWrites(t, pool, "cloudos_backup"); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: cloudos_backup writes\n%v\nwant\n%v", step, got, want)
		}
		var member, reads, readsHash, super, login bool
		if err := pool.QueryRow(ctx, `SELECT pg_has_role('cloudos_backup', 'pg_read_all_data', 'USAGE'),
			has_table_privilege('cloudos_backup', 'files', 'SELECT'),
			has_column_privilege('cloudos_backup', 'accounts', 'password_hash', 'SELECT'),
			rolsuper, rolcanlogin FROM pg_roles WHERE rolname = 'cloudos_backup'`).Scan(&member, &reads, &readsHash, &super, &login); err != nil {
			t.Fatal(err)
		}
		if !member || !reads || !readsHash || super {
			t.Errorf("%s: member of pg_read_all_data %v, reads files %v, reads accounts %v, superuser %v", step, member, reads, readsHash, super)
		}
	}

	if _, err := p.UpTo(ctx, 28); err != nil {
		t.Fatalf("up 28: %v", err)
	}
	check("up")

	// Logged in as the role: a run's row, and nothing else.
	if err := db.SetLoginPassword(ctx, pool, "cloudos_backup", "backup-test-password"); err != nil {
		t.Fatal(err)
	}
	defer db.SetLoginPassword(ctx, pool, "cloudos_backup", "") //nolint:errcheck
	cfg, err := pgx.ParseConfig(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = "cloudos_backup", "backup-test-password"
	as, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("log in as cloudos_backup: %v", err)
	}
	defer as.Close(ctx)
	var id int64
	if err := as.QueryRow(ctx, `INSERT INTO backup_runs (kind) VALUES ('check') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("the role cannot start a run: %v", err)
	}
	if _, err := as.Exec(ctx, `UPDATE backup_runs SET finished_at = now(), ok = false, error = 'disk absent' WHERE id = $1`, id); err != nil {
		t.Fatalf("the role cannot end a run: %v", err)
	}
	for _, q := range []string{
		`UPDATE files SET size = 2`, `DELETE FROM files`, `INSERT INTO tasks (kind) VALUES ('selftest')`,
		`DELETE FROM backup_runs`, `UPDATE backup_runs SET kind = 'copy'`, `TRUNCATE backup_runs`,
		`UPDATE accounts SET role = 'member'`, `CREATE TABLE x (y int)`,
	} {
		if _, err := as.Exec(ctx, q); err == nil {
			t.Errorf("cloudos_backup may run %s", q)
		}
	}
	var n int
	if err := as.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE password_hash IS NOT NULL`).Scan(&n); err != nil || n != 1 {
		t.Errorf("cloudos_backup reads the whole database (pg_dump): %d, %v", n, err)
	}
	as.Close(ctx)

	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("down 28: %v", err)
	}
	if tableExists(t, pool, "backup_runs") {
		t.Error("backup_runs is still there after down")
	}
	var files int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files`).Scan(&files); err != nil || files != 1 {
		t.Fatalf("after down: %d files (%v)", files, err)
	}

	if _, err := p.UpTo(ctx, 28); err != nil {
		t.Fatalf("up 28 again: %v", err)
	}
	check("up again")
}

// TestSMBRescueMigration checks that migration 29 gives the trash its
// reason, journals op 'rescue' and grants the smb service's role the two
// columns of a kept SMB write; that undoing it is refused while a rescue is
// journaled, then keeps every trash item; and that it applies again.
func TestSMBRescueMigration(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trash (owner, path, kind, size, expires_at) VALUES ('enzo', 'deleted.txt', 'file', 1, now() + interval '30 days')`); err != nil {
		t.Fatal(err)
	}
	before := smbWrites(t, pool)
	with := func(extra ...string) string {
		w := append(append([]string{}, before...), extra...)
		slices.Sort(w)
		return fmt.Sprint(w)
	}

	if _, err := p.UpTo(ctx, 29); err != nil {
		t.Fatalf("up 29: %v", err)
	}
	if got := fmt.Sprint(smbWrites(t, pool)); got != with("INSERT files.trash_id", "INSERT trash.reason") {
		t.Errorf("up: cloudos_smb writes\n%v\nwant\n%v", got, with("INSERT files.trash_id", "INSERT trash.reason"))
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trash (owner, path, kind, size, expires_at, reason) VALUES ('enzo', 'kept.txt', 'file', 1, now() + interval '30 days', 'smb_not_saved')`); err != nil {
		t.Fatalf("a kept SMB write: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trash (owner, path, kind, size, expires_at, reason) VALUES ('enzo', 'x.txt', 'file', 1, now(), 'whatever')`); err == nil {
		t.Error("the trash took an unknown reason")
	}

	// A rescue in the journal holds the migration up: the core settles it first
	if _, err := pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op, trash_id) SELECT 'enzo', 'id', 'kept.txt', 'rescue', id FROM trash WHERE path = 'kept.txt'`); err != nil {
		t.Fatalf("journal a rescue: %v", err)
	}
	if _, err := p.Down(ctx); err == nil {
		t.Fatal("down 29 with a rescue in the journal was not refused")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM moves WHERE op = 'rescue'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("down 29: %v", err)
	}
	if got := fmt.Sprint(smbWrites(t, pool)); got != with() {
		t.Errorf("down: cloudos_smb writes %v, want %v", got, with())
	}
	var items int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM trash`).Scan(&items); err != nil || items != 2 {
		t.Errorf("after down: %d trash items (%v), want 2", items, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op) VALUES ('enzo', 'id', 'kept.txt', 'rescue')`); err == nil {
		t.Error("after down, the journal still takes op 'rescue'")
	}

	if _, err := p.UpTo(ctx, 29); err != nil {
		t.Fatalf("up 29 again: %v", err)
	}
	var reasons int
	if err := pool.QueryRow(ctx, `SELECT count(reason) FROM trash`).Scan(&reasons); err != nil || reasons != 0 {
		t.Errorf("up again: %d reasons (%v), want 0", reasons, err)
	}
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

// TestSearchWordsMigration checks that migration 22 builds the vocabulary of
// the texts already there, that its trigger follows every write of a text, and
// that undoing it keeps every text.
func TestSearchWordsMigration(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 21); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	// Two texts with « facture », one with « Warhallow » and numbers.
	exec(`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES
		(sha256('a'), 'text', 'Facture d''électricité', 21, false, true),
		(sha256('b'), 'text', 'Facture Warhallow 2025 42', 25, false, true)`)
	words := func() map[string]int {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT word, docs FROM search_words`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for rows.Next() {
			var w string
			var n int
			if err := rows.Scan(&w, &n); err != nil {
				t.Fatal(err)
			}
			out[w] = n
		}
		return out
	}
	for _, step := range []string{"up", "up again"} {
		if _, err := p.UpTo(ctx, 22); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if got := fmt.Sprint(words()); got != "map[electricit:1 factur:2 warhallow:1]" {
			t.Errorf("%s: vocabulary %s", step, got)
		}
		if _, err := p.DownTo(ctx, 21); err != nil {
			t.Fatalf("down: %v", err)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_text`).Scan(&n); err != nil || n != 2 {
			t.Errorf("after down: %d texts (%v)", n, err)
		}
	}

	if _, err := p.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	// A scan read by OCR (UPDATE), a text pruned (DELETE), a new one (INSERT).
	// « loyer » stems to loi.
	exec(`INSERT INTO document_text (sha256, source, text, chars, truncated, has_text) VALUES (sha256('c'), 'pdf', '', 0, false, false)`)
	exec(`UPDATE document_text SET text = 'Quittance de loyer', chars = 18, has_text = true WHERE sha256 = sha256('c')`)
	exec(`DELETE FROM document_text WHERE sha256 = sha256('b')`)
	if got := fmt.Sprint(words()); got != "map[electricit:1 factur:1 loi:1 quittanc:1]" {
		t.Errorf("after the writes: %s", got)
	}
	var sound string
	if err := pool.QueryRow(ctx, `SELECT cloudos_sound('warhallow') || ' ' || cloudos_sound('warolo') || ' ' || sound FROM search_words WHERE word = 'quittanc'`).Scan(&sound); err != nil || sound != "wrl wrl qtnc" {
		t.Errorf("sounds %q (%v)", sound, err)
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
