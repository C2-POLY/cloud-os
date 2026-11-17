package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Mkdir creates the folder owner/p and its missing parents, on disk and in the index.
func (s *Store) Mkdir(ctx context.Context, owner, p string) (File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return File{}, err
	}
	p, err = CleanPath(p)
	if err != nil {
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
	why, err := conflict(ctx, tx, owner, p, "")
	if err != nil {
		return File{}, err
	}
	if why != "" {
		return File{}, fmt.Errorf("%w: %s/%s: %s", ErrExists, owner, p, why)
	}
	if err := ensureDirs(ctx, tx, owner, p); err != nil {
		return File{}, err
	}
	out, err := insertFile(ctx, tx, File{Owner: owner, Path: p, Kind: KindDir, MIME: "inode/directory", ModifiedAt: time.Now()})
	if err != nil {
		return File{}, err
	}
	if err := os.MkdirAll(s.diskPath(owner, p), 0o750); err != nil {
		return File{}, fmt.Errorf("create folder: %w", err)
	}
	return out, tx.Commit(ctx)
}

// List returns the direct children of folder dir ("" for the owner's root),
// folders first, then by name. It returns ErrNotFound when dir is not a folder.
func (s *Store) List(ctx context.Context, owner, dir string) ([]File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if dir != "" {
		dir, err = CleanPath(dir)
		if err != nil {
			return nil, err
		}
		f, err := s.Lookup(ctx, owner, dir)
		if err != nil {
			return nil, err
		}
		if !f.IsDir() {
			return nil, fmt.Errorf("%w: %s is a file", ErrNotFound, dir)
		}
		prefix = dir + "/"
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT `+fileColumns+` FROM files
		 WHERE owner = $1 AND trash_id IS NULL AND path LIKE $2 || '%' ESCAPE '\' AND position('/' in substr(path, $3)) = 0
		 ORDER BY kind = 'file', lower(path)`,
		owner, escapeLike(prefix), len(prefix)+1)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (File, error) { return scanFile(r) })
}

// Move renames or moves the file or folder owner/from to owner/to, children
// included. The intention is journaled before the disk is touched, so a crash
// at any point is finished or undone by RecoverMoves. It never replaces a file.
func (s *Store) Move(ctx context.Context, owner, from, to string) (File, error) {
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
	if from == to {
		return s.Lookup(ctx, owner, from)
	}
	if lt, lf := strings.ToLower(to), strings.ToLower(from); lt != lf && strings.HasPrefix(lt+"/", lf+"/") {
		return File{}, fmt.Errorf("%w: cannot move %s into itself", ErrInvalidPath, from)
	}

	// The owner's lock is held on one connection for the whole move, so no
	// write of this owner can land in the moved folder between the steps.
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return File{}, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(`+ownerLockKey+`)`, owner); err != nil {
		return File{}, err
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+ownerLockKey+`)`, owner); err != nil {
			slog.Error("release owner lock", "owner", owner, "err", err)
			conn.Conn().Close(context.WithoutCancel(ctx)) //nolint:errcheck // dropping the session releases the lock
		}
	}()

	// 1. Check and journal.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if _, err := scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE owner = $1 AND path = $2 AND trash_id IS NULL`, owner, from)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return File{}, fmt.Errorf("%w: %s/%s", ErrNotFound, owner, from)
		}
		return File{}, err
	}
	why, err := conflict(ctx, tx, owner, to, from)
	if err != nil {
		return File{}, err
	}
	if why != "" {
		return File{}, fmt.Errorf("%w: %s/%s: %s", ErrExists, owner, to, why)
	}
	var moveID int64
	if err := tx.QueryRow(ctx, `INSERT INTO moves (owner, from_path, to_path) VALUES ($1, $2, $3) RETURNING id`,
		owner, from, to).Scan(&moveID); err != nil {
		return File{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return File{}, err
	}

	// 2. Rename on disk. From here the journal row stands for the move until
	// step 3 clears it, even if the core stops.
	if err := s.renameOnDisk(owner, from, to); err != nil {
		if _, derr := conn.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); derr != nil {
			slog.Error("drop move intention", "id", moveID, "err", derr)
		}
		return File{}, err
	}

	// 3. Update the index and clear the intention, together.
	if err := applyMove(ctx, conn, moveID, owner, from, to); err != nil {
		return File{}, fmt.Errorf("index the move (finished at next start): %w", err)
	}
	return s.Lookup(ctx, owner, to)
}

// renameOnDisk moves one path, refusing to replace anything at the target.
// A rename that only changes case is allowed on the same entry.
func (s *Store) renameOnDisk(owner, from, to string) error {
	src, dst := s.diskPath(owner, from), s.diskPath(owner, to)
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("source on disk: %w", err)
	}
	if dstInfo, err := os.Lstat(dst); err == nil {
		if !os.SameFile(srcInfo, dstInfo) {
			return fmt.Errorf("%w: %s/%s is on disk but not indexed", ErrExists, owner, to)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
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

type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// applyMove rewrites the paths of the moved row and its children and deletes
// the journal row, in one transaction.
func applyMove(ctx context.Context, db beginner, moveID int64, owner, from, to string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if err := lockOwner(ctx, tx, owner); err != nil {
		return err
	}
	if err := ensureDirs(ctx, tx, owner, to); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE files SET path = CASE WHEN path = $2 THEN $3 ELSE $3 || substr(path, length($2) + 1) END
		 WHERE owner = $1 AND trash_id IS NULL AND (path = $2 OR path LIKE $4 || '/%' ESCAPE '\')`,
		owner, from, to, escapeLike(from)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecoverMoves settles the moves, trashes and restores left by a crash. Call
// it at startup, before serving. One whose target is on disk and source is
// gone is finished; one whose source is still there and target is not is
// dropped; anything else is left in the journal and logged, never guessed.
func (s *Store) RecoverMoves(ctx context.Context) (finished, dropped int, err error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, owner, from_path, to_path, op, trash_id, version_id FROM moves ORDER BY id`)
	if err != nil {
		return 0, 0, err
	}
	type move struct {
		id              int64
		owner, from, to string
		op              string
		trashID         *int64
		versionID       *int64
	}
	var moves []move
	for rows.Next() {
		var m move
		if err := rows.Scan(&m.id, &m.owner, &m.from, &m.to, &m.op, &m.trashID, &m.versionID); err != nil {
			rows.Close()
			return 0, 0, err
		}
		moves = append(moves, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	for _, m := range moves {
		if m.op == "version" && m.versionID != nil {
			fin, drop, err := s.recoverVersion(ctx, m.id, m.owner, m.from, *m.versionID)
			if err != nil {
				return finished, dropped, err
			}
			if fin {
				finished++
			}
			if drop {
				dropped++
			}
			continue
		}
		src, dst := s.diskPath(m.owner, m.from), s.diskPath(m.owner, m.to)
		var trashID int64
		if m.trashID != nil {
			trashID = *m.trashID
		}
		switch m.op {
		case "trash":
			dst = s.trashDisk(m.owner, trashID, m.from)
		case "restore":
			src = s.trashDisk(m.owner, trashID, m.from)
		}
		_, srcErr := os.Lstat(src)
		_, dstErr := os.Lstat(dst)
		srcThere, dstThere := srcErr == nil, dstErr == nil
		switch {
		case !srcThere && dstThere:
			var err error
			switch m.op {
			case "trash":
				err = applyTrash(ctx, s.Pool, m.id, m.owner, m.from, trashID)
			case "restore":
				removeEmptyTrashDir(s.trashDir(m.owner, trashID))
				err = applyRestore(ctx, s.Pool, m.id, m.owner, m.from, m.to, trashID)
			default:
				err = applyMove(ctx, s.Pool, m.id, m.owner, m.from, m.to)
			}
			if err != nil {
				return finished, dropped, err
			}
			finished++
		case srcThere && !dstThere:
			if _, err := s.Pool.Exec(ctx, `DELETE FROM moves WHERE id = $1`, m.id); err != nil {
				return finished, dropped, err
			}
			if m.op == "trash" {
				// The item never reached the trash: its trash row goes too.
				if _, err := s.Pool.Exec(ctx, `DELETE FROM trash WHERE id = $1`, trashID); err != nil {
					return finished, dropped, err
				}
			}
			dropped++
		default:
			slog.Error("operation left in the journal: check the disk by hand", "op", m.op,
				"owner", m.owner, "from", m.from, "to", m.to, "source_on_disk", srcThere, "target_on_disk", dstThere)
		}
	}
	return finished, dropped, nil
}

// Tree returns every live file (not folder) under folder dir, at any depth,
// ordered by path. It returns ErrNotFound when dir is not a live folder.
func (s *Store) Tree(ctx context.Context, owner, dir string) ([]File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	dir, err = CleanPath(dir)
	if err != nil {
		return nil, err
	}
	f, err := s.Lookup(ctx, owner, dir)
	if err != nil {
		return nil, err
	}
	if !f.IsDir() {
		return nil, fmt.Errorf("%w: %s is a file", ErrNotFound, dir)
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT `+fileColumns+` FROM files
		 WHERE owner = $1 AND trash_id IS NULL AND kind = 'file' AND path LIKE $2 || '%' ESCAPE '\'
		 ORDER BY path`,
		owner, escapeLike(dir+"/"))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (File, error) { return scanFile(r) })
}
