package storage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// File is one indexed file.
type File struct {
	ID         int64
	Owner      string
	Path       string
	Size       int64
	MIME       string
	SHA256     []byte
	CreatedAt  time.Time
	ModifiedAt time.Time
	IndexedAt  time.Time
}

const fileColumns = `id, owner, path, size, mime, sha256, created_at, modified_at, indexed_at`

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func scanFile(row pgx.Row) (File, error) {
	var f File
	err := row.Scan(&f.ID, &f.Owner, &f.Path, &f.Size, &f.MIME, &f.SHA256, &f.CreatedAt, &f.ModifiedAt, &f.IndexedAt)
	return f, err
}

// lockOwner serialises every write of one owner for the rest of the
// transaction, so the conflict checks below cannot race.
func lockOwner(ctx context.Context, tx pgx.Tx, owner string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('cloudos.files:' || $1, 0))`, owner)
	return err
}

// conflict reports why path cannot be added for owner, or "" if it can:
// the same path in another case, a file where a folder is needed, a folder
// where the file would go, or a folder that exists in another case.
func conflict(ctx context.Context, q queryer, owner, path string) (string, error) {
	var existing string
	err := q.QueryRow(ctx, `SELECT path FROM files WHERE owner = $1 AND lower(path) = lower($2)`, owner, path).Scan(&existing)
	switch {
	case err == nil && existing == path:
		return "the path already exists", nil
	case err == nil:
		return "the path exists in another case: " + existing, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}

	for _, dir := range ancestors(path) {
		err := q.QueryRow(ctx, `SELECT path FROM files WHERE owner = $1 AND lower(path) = lower($2)`, owner, dir).Scan(&existing)
		if err == nil {
			return "a file already has the name of folder " + dir, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		err = q.QueryRow(ctx,
			`SELECT path FROM files WHERE owner = $1 AND lower(path) LIKE lower($2) || '/%' ESCAPE '\' LIMIT 1`,
			owner, escapeLike(dir)).Scan(&existing)
		if err == nil && !strings.HasPrefix(existing, dir+"/") {
			return "folder " + dir + " exists in another case, as in " + existing, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}

	err = q.QueryRow(ctx,
		`SELECT path FROM files WHERE owner = $1 AND lower(path) LIKE lower($2) || '/%' ESCAPE '\' LIMIT 1`,
		owner, escapeLike(path)).Scan(&existing)
	if err == nil {
		return "a folder already has this name", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return "", nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func insertFile(ctx context.Context, tx pgx.Tx, f File) (File, error) {
	row := tx.QueryRow(ctx,
		`INSERT INTO files (owner, path, size, mime, sha256, modified_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+fileColumns,
		f.Owner, f.Path, f.Size, f.MIME, f.SHA256, f.ModifiedAt)
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
	f, err := scanFile(s.Pool.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE owner = $1 AND path = $2`, owner, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	return f, err
}
