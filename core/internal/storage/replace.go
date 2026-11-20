package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// errNoTarget and errSourceHasVersions tell MoveReplace, once the owner's lock
// is released, to fall back on another operation.
var (
	errNoTarget          = errors.New("no file to replace")
	errSourceHasVersions = errors.New("the source has versions")
)

// MoveReplace renames the file owner/from onto the file owner/to, which it
// replaces: the content of to before becomes a version of to, and to keeps
// its identity (its versions, albums and share links); the row of from goes,
// its content now being to's. It is one journaled operation (op 'replace' in
// moves), under the owner's lock, finished or undone by RecoverMoves; it never
// renames onto an occupied path. Without a file at to, it is Move.
//
// A source with versions of its own would lose them with its row. Then to goes
// to the trash first (with its versions), and from is moved: nothing is lost
// either way, and the old content is restored from the trash.
func (s *Store) MoveReplace(ctx context.Context, owner, from, to string) (File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return File{}, err
	}
	if from, err = CleanPath(from); err != nil {
		return File{}, err
	}
	if to, err = CleanPath(to); err != nil {
		return File{}, err
	}
	if strings.EqualFold(from, to) {
		return s.Move(ctx, owner, from, to)
	}

	var out File
	var head []byte
	err = s.withOwnerLock(ctx, owner, func(conn *pgxpool.Conn) error {
		// 1. Check and journal: the version row and the intention.
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
		src, err := scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE owner = $1 AND path = $2 AND trash_id IS NULL`, owner, from))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s/%s", ErrNotFound, owner, from)
		}
		if err != nil {
			return err
		}
		dst, err := scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE owner = $1 AND lower(path) = lower($2) AND trash_id IS NULL`, owner, to))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoTarget
		}
		if err != nil {
			return err
		}
		if src.IsDir() || dst.IsDir() {
			return fmt.Errorf("%w: %s/%s: only a file replaces a file", ErrExists, owner, dst.Path)
		}
		var versions bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM file_versions WHERE file_id = $1)`, src.ID).Scan(&versions); err != nil {
			return err
		}
		if versions {
			return errSourceHasVersions
		}
		var versionID, moveID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO file_versions (file_id, owner, size, mime, sha256, modified_at) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			dst.ID, owner, dst.Size, dst.MIME, dst.SHA256, dst.ModifiedAt).Scan(&versionID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO moves (owner, from_path, to_path, op, version_id) VALUES ($1, $2, $3, 'replace', $4) RETURNING id`,
			owner, src.Path, dst.Path, versionID).Scan(&moveID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}

		undo := func(cause error) error {
			if _, err := conn.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
				slog.Error("drop replace intention", "id", moveID, "err", err)
			}
			if _, err := conn.Exec(ctx, `DELETE FROM file_versions WHERE id = $1`, versionID); err != nil {
				slog.Error("drop version row", "id", versionID, "err", err)
			}
			return cause
		}

		// 2. Set the old content of to aside as its version, then rename
		// from in its place. From here the journal row stands for the
		// operation until step 3 clears it.
		target := s.diskPath(owner, dst.Path)
		kept := s.versionDisk(owner, dst.ID, versionID)
		if err := os.MkdirAll(filepath.Dir(kept), 0o750); err != nil {
			return undo(err)
		}
		if err := renameNoReplace(target, kept); err != nil {
			return undo(err)
		}
		if err := renameNoReplace(s.diskPath(owner, src.Path), target); err != nil {
			// Put the old content back before reporting the failure.
			if rerr := renameNoReplace(kept, target); rerr != nil {
				slog.Error("old content left as a version, the journal finishes it at startup", "path", dst.Path, "err", rerr)
				return fmt.Errorf("rename onto %s: %w", dst.Path, err)
			}
			return undo(fmt.Errorf("rename onto %s: %w", dst.Path, err))
		}

		// 3. Index: to gets from's content, from's row goes, the intention is cleared.
		head, err = sniffHead(target)
		if err != nil {
			return fmt.Errorf("read the renamed file (finished at next start): %w", err)
		}
		content := File{Size: src.Size, MIME: detectMIME(dst.Path, head), SHA256: src.SHA256, ModifiedAt: src.ModifiedAt}
		out, err = applyReplace(ctx, conn, moveID, owner, src.Path, dst.ID, content)
		if err != nil {
			return fmt.Errorf("index the replace (finished at next start): %w", err)
		}
		return nil
	})
	switch {
	case errors.Is(err, errNoTarget):
		return s.Move(ctx, owner, from, to)
	case errors.Is(err, errSourceHasVersions):
		if _, err := s.Trash(ctx, owner, to); err != nil {
			return File{}, err
		}
		return s.Move(ctx, owner, from, to)
	case err != nil:
		return File{}, err
	}
	if _, err := s.pruneFileVersions(ctx, owner, out.ID); err != nil {
		slog.Warn("prune versions", "file", out.ID, "err", err)
	}
	if s.AfterStore != nil {
		s.AfterStore(ctx, out, head)
	}
	s.notify(ctx, owner, "replaced", out.Path)
	return out, nil
}

// applyReplace gives file toID the content that was renamed onto it, deletes
// the source's row and clears the intention, in one transaction.
func applyReplace(ctx context.Context, db beginner, moveID int64, owner, from string, toID int64, content File) (File, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if err := lockOwner(ctx, tx, owner); err != nil {
		return File{}, err
	}
	out, err := scanFile(tx.QueryRow(ctx,
		`UPDATE files SET size = $2, mime = $3, sha256 = $4, modified_at = $5, indexed_at = now()
		 WHERE id = $1 RETURNING `+fileColumns, toID, content.Size, content.MIME, content.SHA256, content.ModifiedAt))
	if err != nil {
		return File{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM files WHERE owner = $1 AND path = $2 AND trash_id IS NULL AND kind = 'file'`, owner, from); err != nil {
		return File{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
		return File{}, err
	}
	return out, tx.Commit(ctx)
}

// recoverReplace settles a MoveReplace left by a crash:
//   - nothing moved on disk: the intention and its version row go;
//   - the old content set aside, the source not yet renamed: the old content
//     goes back, and the intention goes;
//   - both renamed: the index is finished, with the content hashed again;
//   - anything else is left in the journal and logged, never guessed.
func (s *Store) recoverReplace(ctx context.Context, conn *pgxpool.Conn, moveID int64, owner, from, to string, versionID int64) (finished, dropped bool, err error) {
	var fileID int64
	if err := conn.QueryRow(ctx, `SELECT file_id FROM file_versions WHERE id = $1`, versionID).Scan(&fileID); err != nil {
		return false, false, err
	}
	src, dst, kept := s.diskPath(owner, from), s.diskPath(owner, to), s.versionDisk(owner, fileID, versionID)
	srcThere, dstThere, keptThere := exists(src), exists(dst), exists(kept)
	forget := func() error {
		if _, err := conn.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
			return err
		}
		_, err := conn.Exec(ctx, `DELETE FROM file_versions WHERE id = $1`, versionID)
		return err
	}
	switch {
	case srcThere && dstThere && !keptThere:
		return false, true, forget()
	case srcThere && !dstThere && keptThere:
		if err := renameNoReplace(kept, dst); err != nil {
			return false, false, err
		}
		return false, true, forget()
	case !srcThere && dstThere && keptThere:
		sum, sniff, err := hashFile(dst)
		if err != nil {
			return false, false, err
		}
		info, err := os.Stat(dst)
		if err != nil {
			return false, false, err
		}
		_, err = applyReplace(ctx, conn, moveID, owner, from, fileID,
			File{Size: info.Size(), MIME: detectMIME(to, sniff), SHA256: sum, ModifiedAt: info.ModTime()})
		return err == nil, false, err
	}
	slog.Error("replace left in the journal: check the disk by hand", "owner", owner, "from", from, "to", to,
		"source_on_disk", srcThere, "target_on_disk", dstThere, "version_on_disk", keptThere)
	return false, false, nil
}

func exists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}

// sniffHead reads the first bytes of a file, for its MIME type.
func sniffHead(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, 512)
	n, err := f.Read(b)
	if err != nil && n == 0 && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return b[:n], nil
}

// SetModTime sets the modification date of the file or folder owner/p, in the
// index and on disk. It is metadata: the content is never touched. The Finder
// keeps a copy's dates this way.
func (s *Store) SetModTime(ctx context.Context, owner, p string, t time.Time) (File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return File{}, err
	}
	if p, err = CleanPath(p); err != nil {
		return File{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if err := lockOwner(ctx, tx, owner); err != nil {
		return File{}, err
	}
	f, err := scanFile(tx.QueryRow(ctx,
		`UPDATE files SET modified_at = $3 WHERE owner = $1 AND path = $2 AND trash_id IS NULL RETURNING `+fileColumns, owner, p, t))
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, fmt.Errorf("%w: %s/%s", ErrNotFound, owner, p)
	}
	if err != nil {
		return File{}, err
	}
	if err := os.Chtimes(s.diskPath(owner, f.Path), t, t); err != nil {
		return File{}, fmt.Errorf("set the modification time: %w", err)
	}
	return f, tx.Commit(ctx)
}
