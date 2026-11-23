package smbfs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/doctext"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/thumbs"
)

// rolePool is a pool on the fixture's database whose connections act as the
// smb service's role, DBRole, as `core smb` does with its own login. SET ROLE
// keeps the role's password alone: a running stack may use it.
func rolePool(t *testing.T, fx *fixture) *pgxpool.Pool {
	t.Helper()
	cfg := fx.store.Pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+DBRole)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var user string
	if err := pool.QueryRow(context.Background(), "SELECT current_user").Scan(&user); err != nil || user != DBRole {
		t.Fatalf("the pool acts as %q (%v), want %s", user, err, DBRole)
	}
	return pool
}

// The smb service under its own role: it signs in, reads, notes the
// password's use, hears revocations and writes through storage, and Postgres
// refuses every write outside its grants.
func TestServerUnderItsRole(t *testing.T) {
	fx := newFixture(t)
	pool := rolePool(t, fx)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	t.Run("check role", func(t *testing.T) {
		if err := CheckRole(ctx, pool); err != nil {
			t.Errorf("CheckRole refused %s: %v", DBRole, err)
		}
		// The core's role is a superuser here, and may write everything
		if err := CheckRole(ctx, fx.store.Pool); err == nil {
			t.Error("CheckRole accepted the core's role")
		}
	})

	t.Run("migrations", func(t *testing.T) {
		p, err := migrate.NewProvider(db.SQL(pool))
		if err != nil {
			t.Fatal(err)
		}
		pending, err := p.HasPending(ctx)
		if err != nil || pending {
			t.Errorf("HasPending = %v, %v; want false, nil", pending, err)
		}
	})

	srv, err := Start(ctx, Config{
		Addr:       "127.0.0.1:0",
		Auth:       auth.NewService(pool),
		Store:      &storage.Store{Root: fx.store.Root, Pool: pool},
		loginDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	addr := srv.Addr().String()
	go srv.WatchRevocations(ctx, pool, time.Hour)
	waitListening(t, fx)

	t.Run("sign in and read", func(t *testing.T) {
		share := mountAs(t, addr, "bob", fx.passwords["bob"])
		f, err := share.Open("secret.txt")
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(f)
		f.Close()
		if err != nil || string(b) != "bob's\n" {
			t.Fatalf("read %q, %v", b, err)
		}
		list, err := share.ReadDir(".")
		if err != nil || len(list) != 1 {
			t.Errorf("list: %d entries, %v", len(list), err)
		}
		var used bool
		if err := fx.store.Pool.QueryRow(ctx, `SELECT last_used_at IS NOT NULL FROM smb_passwords WHERE account = 'bob'`).Scan(&used); err != nil || !used {
			t.Errorf("last_used_at not noted under the role (%v)", err)
		}
	})

	t.Run("revocation", func(t *testing.T) {
		share := mountAs(t, addr, "carol", fx.passwords["carol"])
		if err := fx.core.RevokeSMBPassword(ctx, "carol"); err != nil {
			t.Fatal(err)
		}
		if !gone(t, share) {
			t.Error("the session survived its revoked password")
		}
		if n, err := srv.Sweep(ctx, pool); err != nil {
			t.Errorf("sweep under the role: %d, %v", n, err)
		}
	})

	// Every SMB write works under the role: create, edit (a version), a
	// folder, a date, a rename, a rename that replaces, a delete, with the
	// analysis task and the activity line they queue.
	t.Run("writes", func(t *testing.T) {
		store := &storage.Store{Root: fx.store.Root, Pool: pool}
		store.AfterStore = func(ctx context.Context, f storage.File, _ []byte) {
			if _, err := doctext.Enqueue(ctx, &tasks.Queue{Pool: pool}, f, tasks.PriorityNew); err != nil {
				t.Errorf("queue under the role: %v", err)
			}
			if _, err := thumbs.Enqueue(ctx, &tasks.Queue{Pool: pool}, f, []byte("\xff\xd8\xff"), tasks.PriorityNew); err != nil {
				t.Errorf("queue a thumbnail under the role: %v", err)
			}
		}
		store.Notify = (&activity.Log{Pool: pool}).Record
		wctx, wcancel := context.WithCancel(ctx)
		defer wcancel()
		wsrv, err := Start(wctx, Config{Addr: "127.0.0.1:0", Auth: auth.NewService(pool), Store: store,
			loginDelay: time.Millisecond, freeSpace: func() uint64 { return 1 << 40 }})
		if err != nil {
			t.Fatal(err)
		}
		defer wsrv.Close()
		share := mountAs(t, wsrv.Addr().String(), "bob", fx.passwords["bob"])
		steps := []struct {
			what string
			do   func() error
		}{
			{"create", func() error { return share.WriteFile("notes.txt", []byte("first\n"), 0o644) }},
			{"edit", func() error { return share.WriteFile("notes.txt", []byte("second\n"), 0o644) }},
			{"photo", func() error { return share.WriteFile("photo.jpg", []byte("\xff\xd8\xff not really"), 0o644) }},
			{"folder", func() error { return share.MkdirAll(`a\b`, 0o755) }},
			{"date", func() error { return share.Chtimes("notes.txt", time.Unix(1e9, 0), time.Unix(1e9, 0)) }},
			{"rename", func() error { return share.Rename("notes.txt", `a\b\notes.txt`) }},
			{"rename onto a file", func() error {
				if err := share.WriteFile("draft.txt", []byte("third\n"), 0o644); err != nil {
					return err
				}
				v, err := wsrv.backend.Login("bob")
				if err != nil {
					return err
				}
				return v.Shares()[0].Rename("draft.txt", "a/b/notes.txt")
			}},
			{"delete", func() error { return share.Remove("secret.txt") }},
		}
		for _, step := range steps {
			if err := step.do(); err != nil {
				t.Fatalf("%s under %s: %v", step.what, DBRole, err)
			}
		}
		f, err := fx.store.Lookup(ctx, "bob", "a/b/notes.txt")
		if err != nil || f.Size != int64(len("third\n")) {
			t.Errorf("a/b/notes.txt: %v, %v", f, err)
		}
		if v, err := fx.store.ListVersions(ctx, "bob", "a/b/notes.txt"); err != nil || len(v) != 2 {
			t.Errorf("versions: %d, %v; want 2", len(v), err)
		}
		if items, err := fx.store.ListTrash(ctx, "bob"); err != nil || len(items) != 1 {
			t.Errorf("trash: %v, %v", items, err)
		}
		var queued, journaled, moves int
		if err := fx.store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tasks WHERE kind IN ('text', 'thumb')),
			(SELECT count(*) FROM activity WHERE owner = 'bob'), (SELECT count(*) FROM moves)`).Scan(&queued, &journaled, &moves); err != nil {
			t.Fatal(err)
		}
		if queued < 2 || journaled < 5 || moves != 0 {
			t.Errorf("%d tasks, %d activity lines, %d journal rows left", queued, journaled, moves)
		}

		// A write that is not stored is kept in the trash under the role,
		// at once when the name is taken, and once the database answers
		// when it was away.
		st, err := store.Stage(ctx, "bob", "taken.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.WriteAt([]byte("kept"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := fx.store.Mkdir(ctx, "bob", "taken.txt"); err != nil {
			t.Fatal(err)
		}
		var kept *storage.KeptError
		if _, err := st.Commit(storage.NoExpectations()); !errors.As(err, &kept) || kept.TrashID == 0 {
			t.Errorf("a name taken under %s: %v", DBRole, err)
		}
		away := rolePool(t, fx)
		cut := &storage.Store{Root: fx.store.Root, Pool: away}
		if st, err = cut.Stage(ctx, "bob", "away.txt", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.WriteAt([]byte("kept too"), 0); err != nil {
			t.Fatal(err)
		}
		away.Close()
		if _, err := st.Commit(storage.NoExpectations()); !errors.As(err, &kept) || kept.TrashID != 0 {
			t.Errorf("the database away: %v", err)
		}
		if n, err := store.SettleRescued(ctx); n != 1 || err != nil {
			t.Errorf("SettleRescued under %s = %d, %v", DBRole, n, err)
		}
		reasons := 0
		items, _ := fx.store.ListTrash(ctx, "bob")
		for _, it := range items {
			if it.Reason == storage.RescueReason {
				reasons++
			}
		}
		if reasons != 2 {
			t.Errorf("%d kept writes in bob's trash, want 2: %+v", reasons, items)
		}
	})

	t.Run("writes refused", func(t *testing.T) {
		before := fx.snapshot(t)
		for _, stmt := range []string{
			// The index: no column storage does not write
			`INSERT INTO files (owner, path, size, mime, sha256, modified_at, created_at) VALUES ('alice', 'x.txt', 1, 'text/plain', sha256('x'), now(), now())`,
			`UPDATE files SET owner = 'bob'`,
			`UPDATE files SET created_at = now()`,
			`TRUNCATE files`,
			// The journal, the versions, the trash: rows go in and out, never change
			`UPDATE moves SET to_path = 'elsewhere'`,
			`UPDATE file_versions SET sha256 = sha256('x')`,
			`UPDATE trash SET expires_at = now()`,
			`TRUNCATE moves`,
			// The task queue: only new tasks, never another's state
			`UPDATE tasks SET state = 'done'`,
			`DELETE FROM tasks`,
			`SELECT payload FROM tasks`,
			`DELETE FROM activity`,
			`SELECT path FROM activity`,
			// What the workers write
			`INSERT INTO thumbnails (sha256, size, mime, width, height, bytes) VALUES (sha256('a'), 256, 'image/webp', 1, 1, '\x00')`,
			`DELETE FROM document_text`,
			`SELECT text FROM document_text`,
			// Accounts, sessions and SMB passwords
			`UPDATE smb_passwords SET nt_hash = decode('00000000000000000000000000000001', 'hex')`,
			`INSERT INTO smb_passwords (account, nt_hash) VALUES ('alice', decode('00000000000000000000000000000001', 'hex'))`,
			`DELETE FROM smb_passwords`,
			`SELECT password_hash FROM accounts`,
			`UPDATE accounts SET disabled_at = NULL`,
			`UPDATE accounts SET role = 'admin'`,
			`INSERT INTO accounts (name, display_name, password_hash, role) VALUES ('mallory', 'M', 'x', 'admin')`,
			`DELETE FROM accounts`,
			`SELECT token_hash FROM sessions`,
			`INSERT INTO sessions (token_hash, account, expires_at) VALUES (sha256('t'), 'alice', now() + interval '1 day')`,
			`DELETE FROM sessions`,
			`DELETE FROM shares`,
			`DELETE FROM album_items`,
			`UPDATE goose_db_version SET is_applied = false`,
			`CREATE TABLE smb_owned (x int)`,
			`ALTER TABLE files ADD COLUMN smb_owned int`,
		} {
			_, err := pool.Exec(ctx, stmt)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Errorf("%s: %v, want permission denied (42501)", stmt, err)
			}
		}
		if after := fx.snapshot(t); after != before {
			t.Errorf("a refused write changed the index or the disk:\n%s\n---\n%s", before, after)
		}
	})

	// CheckRole takes exactly migration 27's grants: one more column, or a
	// delete elsewhere, and the smb service does not start.
	t.Run("check role refuses more", func(t *testing.T) {
		for _, grant := range []string{
			`GRANT UPDATE (owner) ON files TO ` + DBRole,
			`GRANT UPDATE (state) ON tasks TO ` + DBRole,
			`GRANT DELETE ON activity TO ` + DBRole,
			`GRANT UPDATE (nt_hash) ON smb_passwords TO ` + DBRole,
			`GRANT INSERT (name) ON accounts TO ` + DBRole,
			`GRANT SELECT (password_hash) ON accounts TO ` + DBRole,
			`GRANT pg_read_all_data TO ` + DBRole,
		} {
			if _, err := fx.store.Pool.Exec(ctx, grant); err != nil {
				t.Fatal(err)
			}
			err := CheckRole(ctx, pool)
			revoke := strings.Replace(strings.Replace(grant, "GRANT", "REVOKE", 1), " TO ", " FROM ", 1)
			if _, rerr := fx.store.Pool.Exec(ctx, revoke); rerr != nil {
				t.Fatal(rerr)
			}
			if err == nil {
				t.Errorf("CheckRole accepted the role after %s", grant)
			}
		}
		if err := CheckRole(ctx, pool); err != nil {
			t.Errorf("CheckRole refused the role once the extra grants were revoked: %v", err)
		}
	})
}
