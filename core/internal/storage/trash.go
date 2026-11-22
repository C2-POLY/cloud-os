package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultTrashTTL is how long a trashed item is kept before it is purged.
const DefaultTrashTTL = 30 * 24 * time.Hour

// TrashItem is one file or folder in the trash, as it was when deleted.
type TrashItem struct {
	ID        int64
	Owner     string
	Path      string // where it was
	Kind      string
	Size      int64 // bytes, children included
	DeletedAt time.Time
	ExpiresAt time.Time
	// Reason is why it is in the trash when nobody deleted it: RescueReason
	// for an SMB write the box could not store. Empty for a deletion.
	Reason string
}

func (s *Store) trashTTL() time.Duration {
	if s.TrashTTL > 0 {
		return s.TrashTTL
	}
	return DefaultTrashTTL
}

// trashRoot is the only place the trash ever removes bytes from.
func (s *Store) trashRoot(owner string) string {
	return filepath.Join(s.Root, ".cloudos", "trash", owner)
}

func (s *Store) trashDir(owner string, id int64) string {
	return filepath.Join(s.trashRoot(owner), strconv.FormatInt(id, 10))
}

// trashDisk is where the item that was at original lies in the trash.
func (s *Store) trashDisk(owner string, id int64, original string) string {
	return filepath.Join(s.trashDir(owner, id), path.Base(original))
}

// withOwnerLock runs fn on one connection holding the owner's lock for its
// whole length, as Move does, so no write of this owner can interleave.
func (s *Store) withOwnerLock(ctx context.Context, owner string, fn func(conn *pgxpool.Conn) error) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(`+ownerLockKey+`)`, owner); err != nil {
		return err
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+ownerLockKey+`)`, owner); err != nil {
			slog.Error("release owner lock", "owner", owner, "err", err)
			conn.Conn().Close(context.WithoutCancel(ctx)) //nolint:errcheck // dropping the session releases the lock
		}
	}()
	return fn(conn)
}

const trashColumns = `id, owner, path, kind, size, deleted_at, expires_at, coalesce(reason, '')`

func scanTrash(row pgx.Row) (TrashItem, error) {
	var t TrashItem
	err := row.Scan(&t.ID, &t.Owner, &t.Path, &t.Kind, &t.Size, &t.DeletedAt, &t.ExpiresAt, &t.Reason)
	return t, err
}

// Trash moves the file or folder owner/p, children included, to the trash.
// Nothing is erased: the bytes move under .cloudos/trash, the rows are marked,
// and Restore brings them back until the item expires.
func (s *Store) Trash(ctx context.Context, owner, p string) (TrashItem, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return TrashItem{}, err
	}
	if p, err = CleanPath(p); err != nil {
		return TrashItem{}, err
	}
	var item TrashItem
	err = s.withOwnerLock(ctx, owner, func(conn *pgxpool.Conn) error {
		// 1. Journal the intention, with the trash row it creates.
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
		f, err := scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE owner = $1 AND path = $2 AND trash_id IS NULL`, owner, p))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s/%s", ErrNotFound, owner, p)
		}
		if err != nil {
			return err
		}
		item, err = scanTrash(tx.QueryRow(ctx,
			`INSERT INTO trash (owner, path, kind, size, expires_at)
			 SELECT $1, $2, $3, coalesce(sum(size), 0), now() + make_interval(secs => $5)
			 FROM files WHERE owner = $1 AND trash_id IS NULL AND (path = $2 OR path LIKE $4 || '/%' ESCAPE '\')
			 RETURNING `+trashColumns, owner, p, f.Kind, escapeLike(p), s.trashTTL().Seconds()))
		if err != nil {
			return err
		}
		var moveID int64
		if err := tx.QueryRow(ctx, `INSERT INTO moves (owner, from_path, to_path, op, trash_id) VALUES ($1, $2, '', 'trash', $3) RETURNING id`,
			owner, p, item.ID).Scan(&moveID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}

		// 2. Move the bytes into the trash.
		if err := os.MkdirAll(s.trashDir(owner, item.ID), 0o750); err != nil {
			return s.dropIntention(ctx, conn, moveID, item.ID, err)
		}
		if err := renameNoReplace(s.diskPath(owner, p), s.trashDisk(owner, item.ID, p)); err != nil {
			return s.dropIntention(ctx, conn, moveID, item.ID, err)
		}

		// 3. Mark the rows and clear the intention, together.
		return applyTrash(ctx, conn, moveID, owner, p, item.ID)
	})
	if err == nil {
		s.notify(ctx, owner, "trashed", p)
	}
	return item, err
}

// dropIntention undoes a journaled trash that never reached the disk.
func (s *Store) dropIntention(ctx context.Context, conn *pgxpool.Conn, moveID, trashID int64, cause error) error {
	if _, err := conn.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
		slog.Error("drop trash intention", "id", moveID, "err", err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM trash WHERE id = $1`, trashID); err != nil {
		slog.Error("drop trash row", "id", trashID, "err", err)
	}
	return cause
}

func applyTrash(ctx context.Context, db beginner, moveID int64, owner, p string, trashID int64) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if _, err := tx.Exec(ctx,
		`UPDATE files SET trash_id = $3
		 WHERE owner = $1 AND trash_id IS NULL AND (path = $2 OR path LIKE $4 || '/%' ESCAPE '\')`,
		owner, p, trashID, escapeLike(p)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListTrash returns the owner's trash, most recently deleted first.
func (s *Store) ListTrash(ctx context.Context, owner string) ([]TrashItem, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+trashColumns+` FROM trash WHERE owner = $1 ORDER BY deleted_at DESC, id DESC`, owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrashItem, error) { return scanTrash(r) })
}

// restoredName is the n-th name to try when the original path is taken:
// "a/rapport.pdf" gives "a/rapport (restauré).pdf", then "a/rapport (restauré 2).pdf".
func restoredName(p string, n int) string {
	dir, base := path.Split(p)
	ext := path.Ext(base)
	if ext == base { // ".bashrc": no extension to keep apart
		ext = ""
	}
	stem := strings.TrimSuffix(base, ext)
	suffix := " (restauré)"
	if n > 1 {
		suffix = fmt.Sprintf(" (restauré %d)", n)
	}
	return dir + stem + suffix + ext
}

// Restore brings trash item id back to its original path, or beside it under a
// "(restauré)" name when the path is taken, and at the root when its folder
// has become a file. Missing parent folders are created.
func (s *Store) Restore(ctx context.Context, owner string, id int64) (File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return File{}, err
	}
	var target string
	err = s.withOwnerLock(ctx, owner, func(conn *pgxpool.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
		item, err := scanTrash(tx.QueryRow(ctx, `SELECT `+trashColumns+` FROM trash WHERE id = $1 AND owner = $2`, id, owner))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: trash item %d", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		if target, err = freeRestorePath(ctx, tx, owner, item.Path); err != nil {
			return err
		}
		var moveID int64
		if err := tx.QueryRow(ctx, `INSERT INTO moves (owner, from_path, to_path, op, trash_id) VALUES ($1, $2, $3, 'restore', $4) RETURNING id`,
			owner, item.Path, target, id).Scan(&moveID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}

		dst := s.diskPath(owner, target)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return s.dropRestore(ctx, conn, moveID, err)
		}
		if err := renameNoReplace(s.trashDisk(owner, id, item.Path), dst); err != nil {
			return s.dropRestore(ctx, conn, moveID, err)
		}
		removeEmptyTrashDir(s.trashDir(owner, id))
		return applyRestore(ctx, conn, moveID, owner, item.Path, target, id)
	})
	if err != nil {
		return File{}, err
	}
	s.notify(ctx, owner, "restored", target)
	return s.Lookup(ctx, owner, target)
}

// freeRestorePath finds where to restore original: itself, then "(restauré n)"
// beside it, then the same names at the root if its folder became a file.
func freeRestorePath(ctx context.Context, q queryer, owner, original string) (string, error) {
	for _, base := range []string{original, path.Base(original)} {
		for n := 0; n <= 50; n++ {
			candidate := base
			if n > 0 {
				candidate = restoredName(base, n)
			}
			why, err := conflict(ctx, q, owner, candidate, "")
			if err != nil {
				return "", err
			}
			if why == "" {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("%w: no free name to restore %s", ErrExists, original)
}

func (s *Store) dropRestore(ctx context.Context, conn *pgxpool.Conn, moveID int64, cause error) error {
	if _, err := conn.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
		slog.Error("drop restore intention", "id", moveID, "err", err)
	}
	return cause
}

func applyRestore(ctx context.Context, db beginner, moveID int64, owner, original, target string, trashID int64) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if err := ensureDirs(ctx, tx, owner, target); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE files SET trash_id = NULL,
		     path = CASE WHEN path = $2 THEN $3 ELSE $3 || substr(path, length($2) + 1) END
		 WHERE owner = $1 AND trash_id = $4`, owner, original, target, trashID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM trash WHERE id = $1`, trashID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Purge erases trash item id for good. It is one of the only two ways bytes
// leave the box (with PurgeExpired), and only ever under .cloudos/trash/<owner>.
func (s *Store) Purge(ctx context.Context, owner string, id int64) error {
	owner, err := CleanOwner(owner)
	if err != nil {
		return err
	}
	return s.withOwnerLock(ctx, owner, func(conn *pgxpool.Conn) error { return s.purgeItem(ctx, conn, owner, id) })
}

// EmptyTrash purges every item of the owner's trash and returns how many.
func (s *Store) EmptyTrash(ctx context.Context, owner string) (int, error) {
	items, err := s.ListTrash(ctx, owner)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		if err := s.Purge(ctx, owner, it.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
		n++
	}
	return n, nil
}

// PurgeExpired purges the items whose time in the trash is over.
func (s *Store) PurgeExpired(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT owner, id FROM trash WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	type key struct {
		owner string
		id    int64
	}
	expired, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (key, error) {
		var k key
		return k, r.Scan(&k.owner, &k.id)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range expired {
		if err := s.Purge(ctx, k.owner, k.id); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
		n++
	}
	return n, nil
}

// purgeItem removes the bytes first, then the rows: a crash in between leaves
// rows whose bytes are gone, which the next purge finishes.
func (s *Store) purgeItem(ctx context.Context, conn *pgxpool.Conn, owner string, id int64) error {
	var exists, busy bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM trash WHERE id = $1 AND owner = $2),
	                                     EXISTS (SELECT 1 FROM moves WHERE trash_id = $1)`, id, owner).Scan(&exists, &busy); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: trash item %d", ErrNotFound, id)
	}
	if busy {
		return fmt.Errorf("trash item %d has an unfinished operation; retry after restart", id)
	}
	if err := s.removeTrashDir(owner, id); err != nil {
		return err
	}
	// The versions of the purged files go too (their rows follow by cascade).
	rows, err := conn.Query(ctx, `SELECT id FROM files WHERE trash_id = $1 AND kind = 'file'`, id)
	if err != nil {
		return err
	}
	fileIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, fid := range fileIDs {
		if err := s.removeVersionsDir(owner, fid); err != nil {
			return err
		}
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if _, err := tx.Exec(ctx, `DELETE FROM files WHERE trash_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM trash WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// removeTrashDir erases one trash item's folder. It refuses any path that is
// not strictly inside .cloudos/trash/<owner>.
func (s *Store) removeTrashDir(owner string, id int64) error {
	if _, err := CleanOwner(owner); err != nil || id <= 0 {
		return fmt.Errorf("refusing to erase the trash item %q/%d: invalid owner or id", owner, id)
	}
	trash := filepath.Join(s.Root, ".cloudos", "trash")
	dir := s.trashDir(owner, id)
	rel, err := filepath.Rel(trash, dir)
	if err != nil || rel != filepath.Join(owner, strconv.FormatInt(id, 10)) {
		return fmt.Errorf("refusing to erase %s: not a trash item folder", dir)
	}
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// removeEmptyTrashDir removes a trash item's folder once restored. os.Remove
// fails on a folder that is not empty, so it cannot erase anything.
func removeEmptyTrashDir(dir string) {
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("trash folder left behind", "dir", dir, "err", err)
	}
}

// renameNoReplace renames src to dst, refusing to replace anything at dst.
func renameNoReplace(src, dst string) error {
	if _, err := os.Lstat(src); err != nil {
		return fmt.Errorf("source on disk: %w", err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%w: %s already exists on disk", ErrExists, dst)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	return syncDir(filepath.Dir(src))
}
