package smbfs

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/migrate"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
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
// password's use and hears revocations, and Postgres refuses every write
// outside its grants.
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

	t.Run("writes refused", func(t *testing.T) {
		before := fx.snapshot(t)
		for _, stmt := range []string{
			`INSERT INTO files (owner, path, size, mime, sha256, modified_at) VALUES ('alice', 'x.txt', 1, 'text/plain', sha256('x'), now())`,
			`UPDATE files SET size = 0`,
			`DELETE FROM files`,
			`TRUNCATE files`,
			`INSERT INTO moves (owner, from_path, to_path) VALUES ('alice', 'docs', 'gone')`,
			`UPDATE smb_passwords SET nt_hash = decode('00000000000000000000000000000001', 'hex')`,
			`INSERT INTO smb_passwords (account, nt_hash) VALUES ('alice', decode('00000000000000000000000000000001', 'hex'))`,
			`DELETE FROM smb_passwords`,
			`SELECT password_hash FROM accounts`,
			`UPDATE accounts SET disabled_at = NULL`,
			`UPDATE accounts SET role = 'admin'`,
			`INSERT INTO accounts (name, display_name, password_hash, role) VALUES ('mallory', 'M', 'x', 'admin')`,
			`SELECT token_hash FROM sessions`,
			`INSERT INTO sessions (token_hash, account, expires_at) VALUES (sha256('t'), 'alice', now() + interval '1 day')`,
			`INSERT INTO tasks (kind) VALUES ('selftest')`,
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
}
