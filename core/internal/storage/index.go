package storage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kinds of index rows.
const (
	KindFile = "file"
	KindDir  = "dir"
)

// File is one indexed file or folder. A folder has no hash and a size of 0.
type File struct {
	ID         int64
	Owner      string
	Path       string
	Kind       string
	Size       int64
	MIME       string
	SHA256     []byte
	CreatedAt  time.Time
	ModifiedAt time.Time
	IndexedAt  time.Time
	TrashID    *int64 // set while the row is in the trash
}

// IsDir reports whether the row is a folder.
func (f File) IsDir() bool { return f.Kind == KindDir }

const fileColumns = `id, owner, path, kind, size, mime, sha256, created_at, modified_at, indexed_at, trash_id`

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func scanFile(row pgx.Row) (File, error) {
	var f File
	err := row.Scan(&f.ID, &f.Owner, &f.Path, &f.Kind, &f.Size, &f.MIME, &f.SHA256, &f.CreatedAt, &f.ModifiedAt, &f.IndexedAt, &f.TrashID)
	return f, err
}

// lockOwner serialises every write of one owner for the rest of the
// transaction, so the conflict checks below cannot race.
func lockOwner(ctx context.Context, tx pgx.Tx, owner string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(`+ownerLockKey+`)`, owner)
	return err
}

// ownerLockKey is the advisory lock key of an owner ($1). Move holds it as a
// session lock; the same session may still take it per transaction.
const ownerLockKey = `hashtextextended('cloudos.files:' || $1, 0)`

// conflict reports why path cannot be added for owner, or "" if it can: the
// same path in another case, a file where a folder is needed, or a folder that
// exists in another case. self, when not empty, is the row being renamed: it
// does not conflict with itself, so a rename that only changes case is allowed.
func conflict(ctx context.Context, q queryer, owner, path, self string) (string, error) {
	var existing, kind string
	err := q.QueryRow(ctx, `SELECT path, kind FROM files WHERE owner = $1 AND lower(path) = lower($2) AND trash_id IS NULL`, owner, path).Scan(&existing, &kind)
	switch {
	case err == nil && existing == self:
	case err == nil && existing == path && kind == KindDir:
		return "a folder already has this name", nil
	case err == nil && existing == path:
		return "the path already exists", nil
	case err == nil:
		return "the path exists in another case: " + existing, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}

	for _, dir := range ancestors(path) {
		err := q.QueryRow(ctx, `SELECT path, kind FROM files WHERE owner = $1 AND lower(path) = lower($2) AND trash_id IS NULL`, owner, dir).Scan(&existing, &kind)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// The folder is new; ensureDirs creates it.
		case err != nil:
			return "", err
		case kind != KindDir:
			return "a file already has the name of folder " + dir, nil
		case existing != dir:
			return "folder " + dir + " exists in another case: " + existing, nil
		}
	}
	return "", nil
}

// ensureDirs adds a row for each folder above path that has none yet, keeping
// the invariant that every ancestor of a row has its own 'dir' row.
func ensureDirs(ctx context.Context, tx pgx.Tx, owner, path string) error {
	for _, dir := range ancestors(path) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO files (owner, path, kind, size, mime, sha256, modified_at)
			 VALUES ($1, $2, 'dir', 0, 'inode/directory', NULL, now())
			 ON CONFLICT (owner, lower(path)) WHERE trash_id IS NULL DO NOTHING`, owner, dir); err != nil {
			return err
		}
	}
	return nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func insertFile(ctx context.Context, tx pgx.Tx, f File) (File, error) {
	row := tx.QueryRow(ctx,
		`INSERT INTO files (owner, path, kind, size, mime, sha256, modified_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+fileColumns,
		f.Owner, f.Path, f.Kind, f.Size, f.MIME, f.SHA256, f.ModifiedAt)
	out, err := scanFile(row)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return File{}, ErrExists
	}
	return out, err
}

// Lookup returns the file stored at exactly owner/path.
func (s *Store) Lookup(ctx context.Context, owner, path string) (File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return File{}, err
	}
	path, err = CleanPath(path)
	if err != nil {
		return File{}, err
	}
	f, err := scanFile(s.Pool.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE owner = $1 AND path = $2 AND trash_id IS NULL`, owner, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	return f, err
}
