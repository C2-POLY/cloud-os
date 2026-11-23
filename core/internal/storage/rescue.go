package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The rescue of an SMB write whose storing failed (#111, docs/architecture/smb.md).
//
// A Commit that fails after the working file is closed never drops it: the
// content goes to .cloudos/rescue/<owner>/<id>, beside a note <id>.json that
// says where it was written. SettleRescued then moves it into the trash, at
// that path, marked RescueReason, as soon as the database answers: at once
// when it does (a name taken), or at the next start of the core or of
// `core smb`, or within a minute in `core smb`. Nothing ever empties
// .cloudos/rescue but SettleRescued, one note at a time, once its content
// is in the trash.

// ErrKept is wrapped by the error of a Commit whose content could not be
// stored but was kept: in the trash already, or in .cloudos/rescue until the
// database answers. KeptError says which.
var ErrKept = errors.New("kept")

// RescueReason is the reason of a trash item that is a kept SMB write.
const RescueReason = "smb_not_saved"

// KeptError is the error of a Commit that kept the content it could not store.
// It matches ErrKept and its cause (ErrExists for a name taken).
type KeptError struct {
	Cause   error
	ID      string // the rescue id, under .cloudos/rescue/<owner>
	TrashID int64  // the trash item once in the trash, 0 while it waits
}

func (e *KeptError) Error() string {
	if e.TrashID != 0 {
		return fmt.Sprintf("not stored, kept in the trash (item %d): %v", e.TrashID, e.Cause)
	}
	return fmt.Sprintf("not stored, kept until the database answers (rescue %s): %v", e.ID, e.Cause)
}

func (e *KeptError) Unwrap() []error { return []error{ErrKept, e.Cause} }

// rescueNote is what .cloudos/rescue/<owner>/<id>.json says of its content.
type rescueNote struct {
	Owner      string    `json:"owner"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256,omitempty"` // hex; empty if it could not be hashed
	ModifiedAt time.Time `json:"modified_at"`
	Reason     string    `json:"reason"` // why it was not stored
	KeptAt     time.Time `json:"kept_at"`
}

var rescueIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (s *Store) rescueDir() string { return filepath.Join(s.Root, ".cloudos", "rescue") }

func (s *Store) rescueOwnerDir(owner string) string { return filepath.Join(s.rescueDir(), owner) }

// rescueContent is where the content of rescue id waits.
func (s *Store) rescueContent(owner, id string) string {
	return filepath.Join(s.rescueOwnerDir(owner), id)
}

func (s *Store) rescueNotePath(owner, id string) string {
	return filepath.Join(s.rescueOwnerDir(owner), id+".json")
}

// keepStaged moves the closed working file work of owner/p to the rescue
// folder, beside its note: the note first, written whole then renamed, and
// only then the content. It never erases the content: on failure, the
// working file stays where it is.
func (s *Store) keepStaged(owner, p, work string, sum []byte, size int64, mod time.Time, cause error) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	note := rescueNote{Owner: owner, Path: p, Size: size, SHA256: hex.EncodeToString(sum), ModifiedAt: mod, Reason: cause.Error(), KeptAt: time.Now()}
	data, err := json.MarshalIndent(note, "", "  ")
	if err != nil {
		return "", err
	}
	dir := s.rescueOwnerDir(owner)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("rescue folder: %w", err)
	}
	tmp := s.rescueNotePath(owner, id) + ".tmp"
	if err := writeSynced(tmp, data); err != nil {
		s.dropRescueNote(owner, id)
		return "", fmt.Errorf("rescue note: %w", err)
	}
	if err := os.Rename(tmp, s.rescueNotePath(owner, id)); err != nil {
		s.dropRescueNote(owner, id)
		return "", fmt.Errorf("rescue note: %w", err)
	}
	if err := syncDir(dir); err != nil {
		s.dropRescueNote(owner, id)
		return "", fmt.Errorf("rescue note: %w", err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(work, mod, mod); err != nil {
			slog.Warn("rescue: set the modification time", "owner", owner, "path", p, "err", err)
		}
	}
	if err := renameNoReplace(work, s.rescueContent(owner, id)); err != nil {
		// The content is still the working file: the note alone means nothing.
		s.dropRescueNote(owner, id)
		return "", fmt.Errorf("rescue the content: %w", err)
	}
	return id, nil
}

func writeSynced(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// dropRescueNote removes the note of rescue id, and its unfinished write if
// any. It erases only those two names, strictly in .cloudos/rescue/<owner>,
// never a content: a content is only ever renamed out of there.
func (s *Store) dropRescueNote(owner, id string) {
	if _, err := CleanOwner(owner); err != nil || !rescueIDRe.MatchString(id) {
		slog.Error("refusing to erase a rescue note: invalid owner or id", "owner", owner, "id", id)
		return
	}
	note := s.rescueNotePath(owner, id)
	for _, name := range []string{note, note + ".tmp"} {
		if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Error("rescue note left behind", "file", name, "err", err)
		}
	}
	if err := syncDir(s.rescueOwnerDir(owner)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("rescue: sync the folder", "owner", owner, "err", err)
	}
}

// RescuedCount is how many SMB writes wait in .cloudos/rescue for the
// database, for the box's status (#102).
func (s *Store) RescuedCount() (int, error) {
	n := 0
	err := s.eachRescue(false, func(owner, id string) error { n++; return nil })
	return n, err
}

// eachRescue calls fn with each rescue that has its note, owner by owner.
// unfinished adds the notes a stop left half written (<id>.json.tmp alone).
func (s *Store) eachRescue(unfinished bool, fn func(owner, id string) error) error {
	owners, err := os.ReadDir(s.rescueDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, o := range owners {
		if _, err := CleanOwner(o.Name()); err != nil || !o.IsDir() {
			slog.Warn("rescue: not an owner's folder, left alone", "name", o.Name())
			continue
		}
		entries, err := os.ReadDir(s.rescueOwnerDir(o.Name()))
		if err != nil {
			return err
		}
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok && unfinished {
				if id, ok = strings.CutSuffix(e.Name(), ".json.tmp"); ok && exists(s.rescueNotePath(o.Name(), id)) {
					ok = false // its note is there: fn has it already
				}
			}
			if !ok || !rescueIDRe.MatchString(id) {
				continue
			}
			if err := fn(o.Name(), id); err != nil {
				return err
			}
		}
	}
	return nil
}

// SettleRescued moves every kept SMB write into the trash, and returns how
// many it moved. A rescue it cannot settle safely (no content, a content
// that differs from its note) is logged and left, never guessed. It stops at
// the first database error: the next call starts again.
func (s *Store) SettleRescued(ctx context.Context) (int, error) {
	n := 0
	err := s.eachRescue(true, func(owner, id string) error {
		trashID, err := s.settleRescue(ctx, owner, id)
		if trashID != 0 {
			n++
			slog.Warn("smb write kept in the trash, now that the database answers", "owner", owner, "rescue", id, "trash_id", trashID)
		}
		return err
	})
	return n, err
}

// settleRescue moves rescue id of owner into the trash, under the owner's
// lock, journaled like a trash: the rows and the intention first, then the
// rename, then the intention goes, and the note last. It returns the trash
// item, or 0 when there was nothing to settle.
func (s *Store) settleRescue(ctx context.Context, owner, id string) (int64, error) {
	var trashID int64
	err := s.withOwnerLock(ctx, owner, func(conn *pgxpool.Conn) error {
		// An earlier attempt cut short: settle its journal entry first.
		var moveID, leftTrash int64
		err := conn.QueryRow(ctx, `SELECT id, trash_id FROM moves WHERE owner = $1 AND op = 'rescue' AND from_path = $2`, owner, id).Scan(&moveID, &leftTrash)
		switch {
		case err == nil:
			finished, dropped, err := s.recoverMove(ctx, conn, moveID)
			if err != nil {
				return err
			}
			if finished {
				trashID = leftTrash
				return nil
			}
			if !dropped {
				return nil // left in the journal, and logged
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		// Settled meanwhile by the other process, or a note never finished.
		data, err := os.ReadFile(s.rescueNotePath(owner, id))
		if errors.Is(err, fs.ErrNotExist) {
			if !exists(s.rescueContent(owner, id)) {
				s.dropRescueNote(owner, id)
			}
			return nil
		}
		if err != nil {
			return err
		}
		var note rescueNote
		if err := json.Unmarshal(data, &note); err != nil {
			slog.Error("rescue note unreadable, left in .cloudos/rescue: check it by hand", "owner", owner, "id", id, "err", err)
			return nil
		}
		p, err := CleanPath(note.Path)
		if err != nil || note.Owner != owner {
			slog.Error("rescue note invalid, left in .cloudos/rescue: check it by hand", "owner", owner, "id", id, "note_owner", note.Owner, "path", note.Path)
			return nil
		}
		content := s.rescueContent(owner, id)
		info, err := os.Lstat(content)
		if errors.Is(err, fs.ErrNotExist) {
			// The box stopped between the two renames of keepStaged: the
			// content was still a working file, which the client never saw stored.
			slog.Error("rescue note without its content, removed", "owner", owner, "path", p, "kept_at", note.KeptAt)
			s.dropRescueNote(owner, id)
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			slog.Error("rescued content is not a regular file, left in .cloudos/rescue", "owner", owner, "id", id)
			return nil
		}
		sum, sniff, err := hashFile(content)
		if err != nil {
			return err
		}
		if want, herr := hex.DecodeString(note.SHA256); note.SHA256 != "" && (herr != nil || !bytes.Equal(want, sum) || note.Size != info.Size()) {
			slog.Error("rescued content differs from its note, left in .cloudos/rescue: check it by hand", "owner", owner, "id", id,
				"path", p, "note_size", note.Size, "size", info.Size())
			return nil
		}
		mod := note.ModifiedAt
		if mod.IsZero() {
			mod = info.ModTime()
		}

		// 1. Journal: the trash item, the file born in it, the intention.
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
		item, err := scanTrash(tx.QueryRow(ctx,
			`INSERT INTO trash (owner, path, kind, size, expires_at, reason)
			 VALUES ($1, $2, 'file', $3, now() + make_interval(secs => $4), $5)
			 RETURNING `+trashColumns, owner, p, info.Size(), s.trashTTL().Seconds(), RescueReason))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO files (owner, path, kind, size, mime, sha256, modified_at, trash_id)
			 VALUES ($1, $2, 'file', $3, $4, $5, $6, $7)`,
			owner, p, info.Size(), detectMIME(p, sniff), sum, mod, item.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO moves (owner, from_path, to_path, op, trash_id) VALUES ($1, $2, $3, 'rescue', $4) RETURNING id`,
			owner, id, p, item.ID).Scan(&moveID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}

		// 2. Move the content into the trash.
		if err := os.MkdirAll(s.trashDir(owner, item.ID), 0o750); err != nil {
			return s.dropRescueIntention(ctx, conn, moveID, item.ID, err)
		}
		if err := renameNoReplace(content, s.trashDisk(owner, item.ID, p)); err != nil {
			return s.dropRescueIntention(ctx, conn, moveID, item.ID, err)
		}

		// 3. Clear the intention, then the note.
		if err := applyRescue(ctx, conn, moveID); err != nil {
			return err
		}
		s.dropRescueNote(owner, id)
		trashID = item.ID
		return nil
	})
	return trashID, err
}

// applyRescue clears the intention of a rescue whose content is in the trash.
func applyRescue(ctx context.Context, db beginner, moveID int64) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if _, err := tx.Exec(ctx, `DELETE FROM moves WHERE id = $1`, moveID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// dropRescueIntention undoes a journaled rescue whose content never left
// .cloudos/rescue: the intention, the file row and the trash item go, and
// the next SettleRescued starts again.
func (s *Store) dropRescueIntention(ctx context.Context, conn *pgxpool.Conn, moveID, trashID int64, cause error) error {
	if err := undoRescue(ctx, conn, moveID, trashID); err != nil {
		slog.Error("drop rescue intention", "id", moveID, "err", err)
	}
	return cause
}

func undoRescue(ctx context.Context, db beginner, moveID, trashID int64) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if _, err := tx.Exec(ctx, `DELETE FROM files WHERE trash_id = $1`, trashID); err != nil {
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

// recoverRescue settles a rescue left by a crash: its content in the trash,
// the intention goes with the note; its content still in .cloudos/rescue,
// the rows go and the next SettleRescued starts again; anything else is left
// in the journal and logged, never guessed.
func (s *Store) recoverRescue(ctx context.Context, conn *pgxpool.Conn, moveID int64, owner, id, p string, trashID int64) (finished, dropped bool, err error) {
	if !rescueIDRe.MatchString(id) {
		slog.Error("rescue left in the journal with an invalid id: check it by hand", "owner", owner, "id", id)
		return false, false, nil
	}
	src, dst := s.rescueContent(owner, id), s.trashDisk(owner, trashID, p)
	srcThere, dstThere := exists(src), exists(dst)
	switch {
	case !srcThere && dstThere:
		if err := applyRescue(ctx, conn, moveID); err != nil {
			return false, false, err
		}
		s.dropRescueNote(owner, id)
		return true, false, nil
	case srcThere && !dstThere:
		if err := undoRescue(ctx, conn, moveID, trashID); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	slog.Error("rescue left in the journal: check the disk by hand", "owner", owner, "id", id, "path", p,
		"rescue_on_disk", srcThere, "trash_on_disk", dstThere)
	return false, false, nil
}
