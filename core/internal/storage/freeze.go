package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrFrozen is returned by Freeze while another picture of the data root is
// being taken or read (a backup already running).
var ErrFrozen = errors.New("a snapshot of the data root is already in use")

// snapshotLockKey serialises Freeze: one picture of the data root at a time,
// held from Freeze to Release, so a leftover is only ever cleared by the next
// Freeze, never under a backup still reading it.
const snapshotLockKey = `hashtextextended('cloudos.snapshot', 0)`

// accountsLockKey is auth's lock on account changes: held while the owners'
// locks are taken, no account appears that the list would miss.
const accountsLockKey = `hashtextextended('cloudos.accounts', 0)`

// freezeLinked, set by tests only, runs once the links are made, the locks
// still held.
var freezeLinked func()

// Frozen is a consistent picture of the data root and of the database, taken
// by Freeze: the files as hard links under .cloudos/snapshots/current, and an
// exported Postgres snapshot of the same instant. It holds a connection until
// Release.
type Frozen struct {
	// SnapshotID is the exported database snapshot, for pg_dump --snapshot.
	// It stays valid until EndSnapshot (or Release).
	SnapshotID string
	// Dir is the picture of the data root: <root>/.cloudos/snapshots/current.
	Dir string
	// Files and Bytes count the files linked into Dir.
	Files, Bytes int64
	// Indexed counts the index's file rows in the database snapshot, trashed
	// ones included.
	Indexed int64
	// Owners counts the owners whose locks were held.
	Owners int
	// LockHeld is how long the writes waited: from the first owner's lock to
	// the last one released.
	LockHeld time.Duration

	store *Store
	conn  *pgxpool.Conn
	tx    pgx.Tx
}

func (s *Store) snapshotsDir() string { return filepath.Join(s.Root, ".cloudos", "snapshots") }

// SnapshotDir is where Freeze puts its picture of the data root.
func (s *Store) SnapshotDir() string { return filepath.Join(s.snapshotsDir(), "current") }

// Freeze takes a consistent picture of the data root without stopping the
// box. It takes every owner's lock, the one each storage write holds, so no
// write is half done; exports a REPEATABLE READ snapshot of the database;
// hard-links every file of the data root, versions and trash included, into
// .cloudos/snapshots/current (not .cloudos/tmp nor .cloudos/snapshots); then
// releases the locks, and the writes go on. Storage never modifies a stored
// file in place, so the links keep the content of that instant. A picture
// left by an interrupted run is cleared first.
//
// The caller reads Dir and dumps the database with SnapshotID, calls
// EndSnapshot once pg_dump has started, then Release.
func (s *Store) Freeze(ctx context.Context) (_ *Frozen, err error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	fz := &Frozen{store: s, conn: conn, Dir: s.SnapshotDir()}
	defer func() {
		if err != nil {
			fz.abort()
		}
	}()

	var mine bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+snapshotLockKey+`)`).Scan(&mine); err != nil {
		return nil, err
	}
	if !mine {
		conn.Release()
		fz.conn = nil
		return nil, ErrFrozen
	}
	// Left by a run that stopped before Release: nothing reads it any more.
	if err := s.removeSnapshot(); err != nil {
		return nil, fmt.Errorf("clear the previous snapshot: %w", err)
	}

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(`+accountsLockKey+`)`); err != nil {
		return nil, err
	}
	owners, err := s.owners(ctx, conn)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	locked := 0
	unlock := func() error {
		for i := locked - 1; i >= 0; i-- {
			if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+ownerLockKey+`)`, owners[i]); err != nil {
				return err
			}
		}
		locked = 0
		_, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+accountsLockKey+`)`)
		return err
	}
	for _, o := range owners {
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(`+ownerLockKey+`)`, o); err != nil {
			return nil, fmt.Errorf("lock %s: %w", o, err)
		}
		locked++
	}
	fz.Owners = len(owners)

	// Every write is either committed or not begun: the database snapshot
	// and the links below show the same instant.
	fz.tx, err = conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	if err := fz.tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&fz.SnapshotID); err != nil {
		return nil, fmt.Errorf("export the database snapshot: %w", err)
	}
	if err := fz.tx.QueryRow(ctx, `SELECT count(*) FROM files WHERE kind = 'file'`).Scan(&fz.Indexed); err != nil {
		return nil, err
	}
	if err := s.linkTree(ctx, fz); err != nil {
		return nil, fmt.Errorf("link the data root: %w", err)
	}
	if freezeLinked != nil {
		freezeLinked()
	}
	if err := unlock(); err != nil {
		return nil, fmt.Errorf("release the locks: %w", err)
	}
	fz.LockHeld = time.Since(start)
	return fz, nil
}

// owners lists every owner a write could be for: the accounts, the owners in
// the index, and the owners' folders on disk.
func (s *Store) owners(ctx context.Context, conn *pgxpool.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `SELECT name FROM accounts UNION SELECT DISTINCT owner FROM files`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if _, err := CleanOwner(e.Name()); err == nil && e.IsDir() {
			names = append(names, e.Name())
		}
	}
	// One order for everyone who takes several owners' locks.
	slices.Sort(names)
	return slices.Compact(names), nil
}

// linkTree hard-links every file of the data root into fz.Dir, recreating
// folders and symbolic links, without .cloudos/tmp and .cloudos/snapshots.
func (s *Store) linkTree(ctx context.Context, fz *Frozen) error {
	skip := map[string]bool{s.tmpDir(): true, s.snapshotsDir(): true}
	if err := os.MkdirAll(fz.Dir, 0o750); err != nil {
		return err
	}
	type dirTime struct {
		path string
		mod  time.Time
	}
	var dirs []dirTime
	err := filepath.WalkDir(s.Root, func(src string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if skip[src] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(s.Root, src)
		if err != nil {
			return err
		}
		dst := filepath.Join(fz.Dir, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if rel != "." {
				if err := os.Mkdir(dst, info.Mode().Perm()); err != nil {
					return err
				}
			}
			dirs = append(dirs, dirTime{dst, info.ModTime()})
		case info.Mode().IsRegular():
			if err := os.Link(src, dst); err != nil {
				return err
			}
			fz.Files++
			fz.Bytes += info.Size()
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dst); err != nil {
				return err
			}
		default:
			slog.Warn("snapshot: skipped a file that is neither a folder, a file nor a link", "path", rel, "mode", info.Mode().String())
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The folders' dates, once their content is in: the deepest first.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chtimes(dirs[i].path, dirs[i].mod, dirs[i].mod); err != nil {
			return err
		}
	}
	return nil
}

// EndSnapshot ends the database snapshot's transaction, once pg_dump has
// imported it: an open transaction would hold back the database's cleaning
// while the files are read. Release calls it too.
func (fz *Frozen) EndSnapshot(ctx context.Context) error {
	if fz.tx == nil {
		return nil
	}
	err := fz.tx.Rollback(context.WithoutCancel(ctx))
	fz.tx = nil
	return err
}

// Release removes the picture's links, under .cloudos/snapshots/current and
// nowhere else: the files themselves stay where they are. It ends the
// database snapshot and lets the next Freeze run.
func (fz *Frozen) Release(ctx context.Context) error {
	if fz.conn == nil {
		return nil
	}
	err := errors.Join(fz.EndSnapshot(ctx), fz.store.removeSnapshot())
	if err != nil {
		// Keep the lock until the connection goes: nothing must reuse a
		// picture that could not be cleared. The next Freeze clears it.
		fz.abort()
		return err
	}
	if _, err := fz.conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+snapshotLockKey+`)`); err != nil {
		fz.abort()
		return err
	}
	fz.conn.Release()
	fz.conn = nil
	return nil
}

// abort drops the connection, which releases every lock it holds. A picture
// half made stays until the next Freeze clears it.
func (fz *Frozen) abort() {
	if fz.conn == nil {
		return
	}
	ctx := context.Background()
	if fz.tx != nil {
		fz.tx.Rollback(ctx) //nolint:errcheck // the connection goes anyway
		fz.tx = nil
	}
	fz.conn.Conn().Close(ctx) //nolint:errcheck // dropping the session releases its locks
	fz.conn.Release()
	fz.conn = nil
}

// removeSnapshot erases the picture of the data root, strictly
// <root>/.cloudos/snapshots/current. It removes links only: every file there
// is a second name of a file kept elsewhere under the data root. It refuses
// when .cloudos, .cloudos/snapshots or current is not a real folder (a
// symbolic link could point it at the users' files).
func (s *Store) removeSnapshot() error {
	dir := s.SnapshotDir()
	if rel, err := filepath.Rel(s.Root, dir); err != nil || rel != filepath.Join(".cloudos", "snapshots", "current") {
		return fmt.Errorf("refusing to erase %s: not the snapshot folder", dir)
	}
	for _, p := range []string{filepath.Join(s.Root, ".cloudos"), s.snapshotsDir(), dir} {
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("refusing to erase %s: %s is not a folder", dir, p)
		}
	}
	return os.RemoveAll(dir)
}
