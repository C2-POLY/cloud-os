package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Default version retention: the last DefaultVersionsKeep versions of a file,
// and every version younger than DefaultVersionsMaxAge, are kept.
const (
	DefaultVersionsKeep   = 10
	DefaultVersionsMaxAge = 30 * 24 * time.Hour
)

// Version is a previous content of a file, kept when the file was replaced.
type Version struct {
	ID         int64
	FileID     int64
	Owner      string
	Size       int64
	MIME       string
	SHA256     []byte
	ModifiedAt time.Time // when this content was last modified
	CreatedAt  time.Time // when it was replaced
}

const versionColumns = `id, file_id, owner, size, mime, sha256, modified_at, created_at`

func scanVersion(row pgx.Row) (Version, error) {
	var v Version
	err := row.Scan(&v.ID, &v.FileID, &v.Owner, &v.Size, &v.MIME, &v.SHA256, &v.ModifiedAt, &v.CreatedAt)
	return v, err
}

func (s *Store) versionsDir(owner string, fileID int64) string {
	return filepath.Join(s.Root, ".cloudos", "versions", owner, strconv.FormatInt(fileID, 10))
}

func (s *Store) versionDisk(owner string, fileID, versionID int64) string {
	return filepath.Join(s.versionsDir(owner, fileID), strconv.FormatInt(versionID, 10))
}

func (s *Store) versionsKeep() int {
	if s.VersionsKeep > 0 {
		return s.VersionsKeep
	}
	return DefaultVersionsKeep
}

func (s *Store) versionsMaxAge() time.Duration {
	if s.VersionsMaxAge > 0 {
		return s.VersionsMaxAge
	}
	return DefaultVersionsMaxAge
}

// errGone tells replace that the file disappeared before it took the lock.
var errGone = errors.New("the file to replace is gone")

// replace stores the written file tmp at f.Path over an existing file, whose
// content becomes a version first. Without a file at f.Path, it is a plain Put.
func (s *Store) replace(ctx context.Context, f File, tmp string) (File, error) {
	cur, err := s.Lookup(ctx, f.Owner, f.Path)
	if errors.Is(err, ErrNotFound) {
		return s.place(ctx, f, tmp)
	}
	if err != nil {
		return File{}, err
	}
	if cur.IsDir() {
		return File{}, fmt.Errorf("%w: %s/%s is a folder", ErrExists, f.Owner, f.Path)
	}

	var out File
	err = s.withOwnerLock(ctx, f.Owner, func(conn *pgxpool.Conn) error {
		// 1. Journal: the version row and the intention.
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
		cur, err := scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE owner = $1 AND path = $2 AND trash_id IS NULL`, f.Owner, f.Path))
		if errors.Is(err, pgx.ErrNoRows) {
			return errGone
		}
		if err != nil {
			return err
		}
		if cur.IsDir() {
			return fmt.Errorf("%w: %s/%s is a folder", ErrExists, f.Owner, f.Path)
		}
		var versionID, moveID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO file_versions (file_id, owner, size, mime, sha256, modified_at) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			cur.ID, f.Owner, cur.Size, cur.MIME, cur.SHA256, cur.ModifiedAt).Scan(&versionID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO moves (owner, from_path, to_path, op, version_id) VALUES ($1, $2, '', 'version', $3) RETURNING id`,
			f.Owner, f.Path, versionID).Scan(&moveID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}

		undo := func(cause error) error {
			if _, err := conn.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
				slog.Error("drop version intention", "id", moveID, "err", err)
			}
			if _, err := conn.Exec(ctx, `DELETE FROM file_versions WHERE id = $1`, versionID); err != nil {
				slog.Error("drop version row", "id", versionID, "err", err)
			}
			return cause
		}

		// 2. Set the old content aside, then link the new one in its place.
		disk := s.diskPath(f.Owner, f.Path)
		kept := s.versionDisk(f.Owner, cur.ID, versionID)
		if err := os.MkdirAll(filepath.Dir(kept), 0o750); err != nil {
			return undo(err)
		}
		if err := renameNoReplace(disk, kept); err != nil {
			return undo(err)
		}
		if err := os.Link(tmp, disk); err != nil {
			// Put the old content back before reporting the failure.
			if rerr := renameNoReplace(kept, disk); rerr != nil {
				slog.Error("old content left as a version, the journal finishes it at startup", "path", f.Path, "err", rerr)
				return fmt.Errorf("place the new content: %w", err)
			}
			return undo(fmt.Errorf("place the new content: %w", err))
		}
		if err := syncDir(filepath.Dir(disk)); err != nil {
			return err
		}

		// 3. Index the new content and clear the intention.
		out, err = applyVersion(ctx, conn, moveID, cur.ID, f)
		return err
	})
	if errors.Is(err, errGone) {
		return s.place(ctx, f, tmp)
	}
	if err != nil {
		return File{}, err
	}
	if _, err := s.pruneFileVersions(ctx, f.Owner, out.ID); err != nil {
		slog.Warn("prune versions", "file", out.ID, "err", err)
	}
	return out, nil
}

func applyVersion(ctx context.Context, db beginner, moveID, fileID int64, f File) (File, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	out, err := scanFile(tx.QueryRow(ctx,
		`UPDATE files SET size = $2, mime = $3, sha256 = $4, modified_at = $5, indexed_at = now()
		 WHERE id = $1 RETURNING `+fileColumns, fileID, f.Size, f.MIME, f.SHA256, f.ModifiedAt))
	if err != nil {
		return File{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
		return File{}, err
	}
	return out, tx.Commit(ctx)
}

// ListVersions returns the versions of the live file owner/p, newest first.
func (s *Store) ListVersions(ctx context.Context, owner, p string) ([]Version, error) {
	f, err := s.Lookup(ctx, owner, p)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+versionColumns+` FROM file_versions WHERE file_id = $1 AND owner = $2 ORDER BY created_at DESC, id DESC`, f.ID, f.Owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Version, error) { return scanVersion(r) })
}

func (s *Store) getVersion(ctx context.Context, owner string, id int64) (Version, error) {
	v, err := scanVersion(s.Pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM file_versions WHERE id = $1 AND owner = $2`, id, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, fmt.Errorf("%w: version %d", ErrNotFound, id)
	}
	return v, err
}

// OpenVersion returns owner's version id, opened for reading.
func (s *Store) OpenVersion(ctx context.Context, owner string, id int64) (*os.File, Version, error) {
	v, err := s.getVersion(ctx, owner, id)
	if err != nil {
		return nil, Version{}, err
	}
	r, err := os.Open(s.versionDisk(v.Owner, v.FileID, v.ID))
	if err != nil {
		return nil, Version{}, fmt.Errorf("open version %d: %w", id, err)
	}
	return r, v, nil
}

// RestoreVersion makes version id the file's content again. It replaces the
// file with a copy of the version: the version stays, and the current content
// becomes a version too. Nothing is lost.
func (s *Store) RestoreVersion(ctx context.Context, owner string, id int64) (File, error) {
	v, err := s.getVersion(ctx, owner, id)
	if err != nil {
		return File{}, err
	}
	var p string
	if err := s.Pool.QueryRow(ctx, `SELECT path FROM files WHERE id = $1 AND trash_id IS NULL`, v.FileID).Scan(&p); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return File{}, fmt.Errorf("%w: the file of version %d is in the trash", ErrNotFound, id)
		}
		return File{}, err
	}
	r, _, err := s.OpenVersion(ctx, owner, id)
	if err != nil {
		return File{}, err
	}
	defer r.Close()
	return s.Put(ctx, owner, p, r, PutOptions{ExpectSize: v.Size, ExpectSHA256: v.SHA256, ModTime: v.ModifiedAt, Replace: true})
}

// PruneVersions applies the retention to every file and returns how many
// versions were erased.
func (s *Store) PruneVersions(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT owner, file_id FROM file_versions`)
	if err != nil {
		return 0, err
	}
	type key struct {
		owner string
		id    int64
	}
	files, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (key, error) {
		var k key
		return k, r.Scan(&k.owner, &k.id)
	})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, k := range files {
		n, err := s.pruneFileVersions(ctx, k.owner, k.id)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// pruneFileVersions erases the versions of one file beyond the retention: not
// among the last versionsKeep, and older than versionsMaxAge. Bytes go first,
// then the row.
func (s *Store) pruneFileVersions(ctx context.Context, owner string, fileID int64) (int, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id FROM (
		     SELECT id, created_at, row_number() OVER (ORDER BY created_at DESC, id DESC) AS rank
		     FROM file_versions WHERE file_id = $1 AND owner = $2
		 ) v
		 WHERE rank > $3 AND created_at < now() - make_interval(secs => $4)
		   AND NOT EXISTS (SELECT 1 FROM moves WHERE version_id = v.id)`,
		fileID, owner, s.versionsKeep(), s.versionsMaxAge().Seconds())
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.removeVersionFile(owner, fileID, id); err != nil {
			return 0, err
		}
		if _, err := s.Pool.Exec(ctx, `DELETE FROM file_versions WHERE id = $1`, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// versionsRoot is the only place version bytes are ever erased from.
func (s *Store) versionsRoot() string { return filepath.Join(s.Root, ".cloudos", "versions") }

// removeVersionFile erases one version's bytes. It refuses any path that is
// not exactly .cloudos/versions/<owner>/<file id>/<version id>.
func (s *Store) removeVersionFile(owner string, fileID, versionID int64) error {
	if _, err := CleanOwner(owner); err != nil || fileID <= 0 || versionID <= 0 {
		return fmt.Errorf("refusing to erase version %q/%d/%d: invalid owner or id", owner, fileID, versionID)
	}
	p := s.versionDisk(owner, fileID, versionID)
	rel, err := filepath.Rel(s.versionsRoot(), p)
	if err != nil || rel != filepath.Join(owner, strconv.FormatInt(fileID, 10), strconv.FormatInt(versionID, 10)) {
		return fmt.Errorf("refusing to erase %s: not a version file", p)
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// removeVersionsDir erases every version of one file, when the trash purges
// it. It refuses any path that is not exactly .cloudos/versions/<owner>/<file id>.
func (s *Store) removeVersionsDir(owner string, fileID int64) error {
	if _, err := CleanOwner(owner); err != nil || fileID <= 0 {
		return fmt.Errorf("refusing to erase the versions of %q/%d: invalid owner or id", owner, fileID)
	}
	dir := s.versionsDir(owner, fileID)
	rel, err := filepath.Rel(s.versionsRoot(), dir)
	if err != nil || rel != filepath.Join(owner, strconv.FormatInt(fileID, 10)) {
		return fmt.Errorf("refusing to erase %s: not a versions folder", dir)
	}
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// recoverVersion settles a replace left by a crash: without the version file,
// nothing moved; with the version file and no file, the old content goes back;
// with both, the new content is indexed.
func (s *Store) recoverVersion(ctx context.Context, moveID int64, owner, p string, versionID int64) (finished, dropped bool, err error) {
	var fileID int64
	if err := s.Pool.QueryRow(ctx, `SELECT file_id FROM file_versions WHERE id = $1`, versionID).Scan(&fileID); err != nil {
		return false, false, err
	}
	disk := s.diskPath(owner, p)
	kept := s.versionDisk(owner, fileID, versionID)
	_, keptErr := os.Lstat(kept)
	_, diskErr := os.Lstat(disk)
	forget := func() error {
		if _, err := s.Pool.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
			return err
		}
		_, err := s.Pool.Exec(ctx, `DELETE FROM file_versions WHERE id = $1`, versionID)
		return err
	}
	switch {
	case keptErr != nil:
		return false, true, forget()
	case diskErr != nil:
		if err := renameNoReplace(kept, disk); err != nil {
			return false, false, err
		}
		return false, true, forget()
	default:
		sum, sniff, err := hashFile(disk)
		if err != nil {
			return false, false, err
		}
		info, err := os.Stat(disk)
		if err != nil {
			return false, false, err
		}
		_, err = applyVersion(ctx, s.Pool, moveID, fileID, File{Size: info.Size(), MIME: detectMIME(p, sniff), SHA256: sum, ModifiedAt: info.ModTime()})
		return err == nil, false, err
	}
}

// verifyVersions checks every version's bytes against its row.
func (s *Store) verifyVersions(ctx context.Context, rep *Report) error {
	rows, err := s.Pool.Query(ctx, `SELECT `+versionColumns+` FROM file_versions ORDER BY id`)
	if err != nil {
		return err
	}
	versions, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Version, error) { return scanVersion(r) })
	if err != nil {
		return err
	}
	for _, v := range versions {
		disk := s.versionDisk(v.Owner, v.FileID, v.ID)
		name := fmt.Sprintf("version %d of file %d", v.ID, v.FileID)
		sum, _, err := hashFile(disk)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			rep.Problems = append(rep.Problems, Problem{Kind: "missing", Owner: v.Owner, Path: name})
		case err != nil:
			return err
		case !bytes.Equal(sum, v.SHA256):
			rep.Problems = append(rep.Problems, Problem{Kind: "modified", Owner: v.Owner, Path: name, Note: "content differs from the index"})
		default:
			rep.OK++
		}
	}
	return nil
}
