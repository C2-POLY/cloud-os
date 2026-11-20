package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// stagedFiles lists the SMB working files left under .cloudos/tmp/smb.
func stagedFiles(t *testing.T, s *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(s.stageDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func readAll(t *testing.T, st *Staged) string {
	t.Helper()
	b := make([]byte, st.Size())
	n, err := st.ReadAt(b, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return string(b[:n])
}

// A new file written at random offsets is stored as written, with its hash,
// and leaves no working file.
func TestStageNewFile(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	st, err := s.Stage(ctx, "enzo", "Docs/note.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		off  int64
		text string
	}{{6, "monde"}, {0, "Salut"}, {5, " "}, {11, " !!!"}} {
		if _, err := st.WriteAt([]byte(w.text), w.off); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Truncate(13); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, st); got != "Salut monde !" || st.Size() != 13 {
		t.Fatalf("staged %q (%d bytes)", got, st.Size())
	}
	if _, err := s.Lookup(ctx, "enzo", "Docs/note.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("indexed before Commit: %v", err)
	}
	mod := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	opt := NoExpectations()
	opt.ModTime = mod
	f, err := st.Commit(opt)
	if err != nil {
		t.Fatal(err)
	}
	if diskContent(t, s, "enzo", "Docs/note.txt") != "Salut monde !" || !bytes.Equal(f.SHA256, sumOf("Salut monde !")) ||
		!f.ModifiedAt.Equal(mod) || f.MIME != "text/plain; charset=utf-8" {
		t.Errorf("stored %+v", f)
	}
	if left := stagedFiles(t, s); len(left) != 0 {
		t.Errorf("working files left: %v", left)
	}
	if _, err := st.WriteAt([]byte("x"), 0); err == nil {
		t.Error("a committed staged file took a write")
	}
	assertClean(t, s)
}

// A stored file is read until the first change, which copies it: a staged
// file that only reads stores nothing and makes no version. An edit stores
// the new content and keeps the old one as a version.
func TestStageEditKeepsTheVersionAndCopiesLazily(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	orig := put(t, s, "enzo", "plan.txt", "version un")

	st, err := s.Stage(ctx, "enzo", "plan.txt", &orig)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, st); got != "version un" || st.Copied() || st.Changed() {
		t.Fatalf("before any change: %q, copied %v, changed %v", got, st.Copied(), st.Changed())
	}
	if left := stagedFiles(t, s); len(left) != 0 {
		t.Fatalf("a working file before any change: %v", left)
	}
	if _, err := st.Commit(NoExpectations()); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ListVersions(ctx, "enzo", "plan.txt"); len(v) != 0 {
		t.Fatalf("an unchanged staged file made %d versions", len(v))
	}

	st, err = s.Stage(ctx, "enzo", "plan.txt", &orig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteAt([]byte("deux"), 8); err != nil {
		t.Fatal(err)
	}
	if !st.Copied() || readAll(t, st) != "version deux" {
		t.Fatalf("after the first write: copied %v, %q", st.Copied(), readAll(t, st))
	}
	if diskContent(t, s, "enzo", "plan.txt") != "version un" {
		t.Fatal("the stored file changed before Commit")
	}
	f, err := st.Commit(NoExpectations())
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != orig.ID || diskContent(t, s, "enzo", "plan.txt") != "version deux" {
		t.Errorf("after Commit: id %d (was %d), %q", f.ID, orig.ID, diskContent(t, s, "enzo", "plan.txt"))
	}
	if got := versionContents(t, s, "enzo", "plan.txt"); fmt.Sprint(got) != "[version un]" {
		t.Errorf("versions = %v", got)
	}

	// A truncation copies only what it keeps
	cur, _ := s.Lookup(ctx, "enzo", "plan.txt")
	st, err = s.Stage(ctx, "enzo", "plan.txt", &cur)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Truncate(7); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Commit(NoExpectations()); err != nil {
		t.Fatal(err)
	}
	if diskContent(t, s, "enzo", "plan.txt") != "version" {
		t.Errorf("after the truncation: %q", diskContent(t, s, "enzo", "plan.txt"))
	}
	assertClean(t, s)
}

// When someone else stored the file meanwhile (the web, WebDAV), the last
// writer wins, and the other content stays as a version.
func TestStageLastWriterWinsAndKeepsTheOther(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	orig := put(t, s, "enzo", "a.txt", "original")
	st, err := s.Stage(ctx, "enzo", "a.txt", &orig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteAt([]byte("SMB"), 0); err != nil {
		t.Fatal(err)
	}
	replaceWith(t, s, "enzo", "a.txt", "from the web")
	if _, err := st.Commit(NoExpectations()); err != nil {
		t.Fatal(err)
	}
	if got := diskContent(t, s, "enzo", "a.txt"); got != "SMBginal" {
		t.Errorf("stored %q", got)
	}
	if got := versionContents(t, s, "enzo", "a.txt"); fmt.Sprint(got) != "[from the web original]" {
		t.Errorf("versions = %v", got)
	}
	// A staged new file whose path got taken meanwhile replaces it the same way
	st, err = s.Stage(ctx, "enzo", "b.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteAt([]byte("over SMB"), 0); err != nil {
		t.Fatal(err)
	}
	put(t, s, "enzo", "b.txt", "web first")
	if _, err := st.Commit(NoExpectations()); err != nil {
		t.Fatal(err)
	}
	if got := versionContents(t, s, "enzo", "b.txt"); fmt.Sprint(got) != "[web first]" || diskContent(t, s, "enzo", "b.txt") != "over SMB" {
		t.Errorf("b.txt: %q, versions %v", diskContent(t, s, "enzo", "b.txt"), got)
	}
	assertClean(t, s)
}

// Abort leaves nothing: no working file, no row, the stored file as it was.
func TestStageAbortLeavesNothing(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	orig := put(t, s, "enzo", "a.txt", "keep me")
	before := snapshot(t, s.Root)

	edit, err := s.Stage(ctx, "enzo", "a.txt", &orig)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Stage(ctx, "enzo", "new.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []*Staged{edit, fresh} {
		if _, err := st.WriteAt(bytes.Repeat([]byte("x"), 1<<20), 3); err != nil {
			t.Fatal(err)
		}
		st.Abort()
		st.Abort() // twice is harmless
		if _, err := st.Commit(NoExpectations()); err == nil {
			t.Error("an aborted staged file committed")
		}
	}
	if left := stagedFiles(t, s); len(left) != 0 {
		t.Errorf("working files left: %v", left)
	}
	// The staging folder itself may stay; nothing else changed.
	after := snapshot(t, s.Root)
	delete(after, s.stageDir())
	if !equalSnapshots(before, after) {
		t.Errorf("Abort changed the disk:\n%v\n%v", before, after)
	}
	if _, err := s.Lookup(ctx, "enzo", "new.bin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("new.bin indexed: %v", err)
	}
	if v, _ := s.ListVersions(ctx, "enzo", "a.txt"); len(v) != 0 {
		t.Errorf("%d versions", len(v))
	}
	assertClean(t, s)
}

// CleanTmp leaves the SMB working files alone: the smb service may be
// writing them. Only CleanStaged, at its own start, removes them.
func TestCleanTmpLeavesTheSMBWorkingFiles(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	writeTree(t, s.tmpDir(), map[string]string{"put-1": "partial", "smb/stage-1": "being written"})
	if n, err := s.CleanTmp(); n != 1 || err != nil {
		t.Fatalf("CleanTmp = %d, %v", n, err)
	}
	if b, err := os.ReadFile(filepath.Join(s.stageDir(), "stage-1")); err != nil || string(b) != "being written" {
		t.Fatalf("CleanTmp touched the SMB working file: %q, %v", b, err)
	}
	if n, err := s.CleanStaged(); n != 1 || err != nil {
		t.Fatalf("CleanStaged = %d, %v", n, err)
	}
	if left := stagedFiles(t, s); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

// MoveReplace renames a file onto another: the target keeps its identity,
// gets the source's content, and keeps its content before as a version.
func TestMoveReplace(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	target := put(t, s, "enzo", "doc.txt", "ancien")
	put(t, s, "enzo", "tmp/doc.sb-1234", "nouveau")
	f, err := s.MoveReplace(ctx, "enzo", "tmp/doc.sb-1234", "doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != target.ID || f.MIME != "text/plain; charset=utf-8" || diskContent(t, s, "enzo", "doc.txt") != "nouveau" {
		t.Errorf("after MoveReplace: %+v, %q", f, diskContent(t, s, "enzo", "doc.txt"))
	}
	if _, err := s.Lookup(ctx, "enzo", "tmp/doc.sb-1234"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the source is still indexed: %v", err)
	}
	if got := versionContents(t, s, "enzo", "doc.txt"); fmt.Sprint(got) != "[ancien]" {
		t.Errorf("versions = %v", got)
	}
	if journal(t, s) != 0 {
		t.Error("journal not cleared")
	}

	// Without a target it is a move; a folder never replaces nor is replaced.
	put(t, s, "enzo", "b.txt", "b")
	if f, err := s.MoveReplace(ctx, "enzo", "b.txt", "c.txt"); err != nil || f.Path != "c.txt" {
		t.Errorf("move without a target: %+v, %v", f, err)
	}
	if _, err := s.MoveReplace(ctx, "enzo", "tmp", "c.txt"); !errors.Is(err, ErrExists) {
		t.Errorf("a folder replaced a file: %v", err)
	}
	if _, err := s.MoveReplace(ctx, "enzo", "c.txt", "tmp"); !errors.Is(err, ErrExists) {
		t.Errorf("a file replaced a folder: %v", err)
	}

	// A source with versions of its own would lose them with its row: the
	// target goes to the trash instead, and nothing is lost.
	put(t, s, "enzo", "v.txt", "v1")
	replaceWith(t, s, "enzo", "v.txt", "v2")
	if _, err := s.MoveReplace(ctx, "enzo", "v.txt", "c.txt"); err != nil {
		t.Fatal(err)
	}
	if diskContent(t, s, "enzo", "c.txt") != "v2" || fmt.Sprint(versionContents(t, s, "enzo", "c.txt")) != "[v1]" {
		t.Errorf("c.txt: %q, versions %v", diskContent(t, s, "enzo", "c.txt"), versionContents(t, s, "enzo", "c.txt"))
	}
	items, err := s.ListTrash(ctx, "enzo")
	if err != nil || len(items) != 1 || items[0].Path != "c.txt" {
		t.Errorf("trash: %+v, %v", items, err)
	}
	assertClean(t, s)
}

// A MoveReplace cut at each step is finished or undone by RecoverMoves.
func TestRecoverAMoveReplaceAfterACrash(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	target := put(t, s, "enzo", "doc.txt", "ancien")
	put(t, s, "enzo", "new.txt", "nouveau")
	intend := func() int64 {
		t.Helper()
		var vid int64
		if err := s.Pool.QueryRow(ctx, `INSERT INTO file_versions (file_id, owner, size, mime, sha256, modified_at)
			SELECT id, owner, size, mime, sha256, modified_at FROM files WHERE id = $1 RETURNING id`, target.ID).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op, version_id) VALUES ('enzo', 'new.txt', 'doc.txt', 'replace', $1)`, vid); err != nil {
			t.Fatal(err)
		}
		return vid
	}
	versions := func() int {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM file_versions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1. Cut before the disk was touched: the intention goes.
	intend()
	if _, dropped, err := s.RecoverMoves(ctx); err != nil || dropped != 1 || versions() != 0 || journal(t, s) != 0 {
		t.Fatalf("cut before the disk: dropped %d, versions %d, %v", dropped, versions(), err)
	}
	assertClean(t, s)

	// 2. Cut once the old content was set aside: it goes back.
	vid := intend()
	if err := os.MkdirAll(s.versionsDir("enzo", target.ID), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.diskPath("enzo", "doc.txt"), s.versionDisk("enzo", target.ID, vid)); err != nil {
		t.Fatal(err)
	}
	if _, dropped, err := s.RecoverMoves(ctx); err != nil || dropped != 1 || versions() != 0 {
		t.Fatalf("cut after setting aside: dropped %d, versions %d, %v", dropped, versions(), err)
	}
	if diskContent(t, s, "enzo", "doc.txt") != "ancien" || diskContent(t, s, "enzo", "new.txt") != "nouveau" {
		t.Error("the old content did not go back")
	}
	assertClean(t, s)

	// 3. Cut once both renames were made: the index is finished.
	vid = intend()
	if err := os.Rename(s.diskPath("enzo", "doc.txt"), s.versionDisk("enzo", target.ID, vid)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.diskPath("enzo", "new.txt"), s.diskPath("enzo", "doc.txt")); err != nil {
		t.Fatal(err)
	}
	if finished, _, err := s.RecoverMoves(ctx); err != nil || finished != 1 {
		t.Fatalf("cut after both renames: finished %d, %v", finished, err)
	}
	f, err := s.Lookup(ctx, "enzo", "doc.txt")
	if err != nil || f.ID != target.ID || !bytes.Equal(f.SHA256, sumOf("nouveau")) {
		t.Errorf("doc.txt after recovery: %+v, %v", f, err)
	}
	if _, err := s.Lookup(ctx, "enzo", "new.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("new.txt still indexed: %v", err)
	}
	if got := versionContents(t, s, "enzo", "doc.txt"); fmt.Sprint(got) != "[ancien]" {
		t.Errorf("versions = %v", got)
	}
	if journal(t, s) != 0 {
		t.Error("journal not cleared")
	}
	assertClean(t, s)
}

// RecoverMoves settles an entry only once its owner's lock is free: an
// operation another process (the smb service) is in the middle of finishes
// first and clears its own entry.
func TestRecoverMovesWaitsForTheOwnersLock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "a.txt", "a")
	var moveID int64
	if err := s.Pool.QueryRow(ctx, `INSERT INTO moves (owner, from_path, to_path) VALUES ('enzo', 'a.txt', 'b.txt') RETURNING id`).Scan(&moveID); err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.withOwnerLock(ctx, "enzo", func(conn *pgxpool.Conn) error {
			close(locked)
			<-release
			// The operation finishes as Move would: disk, then index and journal
			if err := s.renameOnDisk("enzo", "a.txt", "b.txt"); err != nil {
				return err
			}
			return applyMove(ctx, conn, moveID, "enzo", "a.txt", "b.txt")
		})
	}()
	<-locked
	recovered := make(chan error, 1)
	go func() {
		_, _, err := s.RecoverMoves(ctx)
		recovered <- err
	}()
	select {
	case err := <-recovered:
		t.Fatalf("RecoverMoves did not wait for the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-recovered; err != nil {
		t.Fatal(err)
	}
	if diskContent(t, s, "enzo", "b.txt") != "a" || journal(t, s) != 0 {
		t.Error("the move was not settled once")
	}
	assertClean(t, s)
}

// SetModTime changes the date, in the index and on disk, and nothing else.
func TestSetModTime(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	f := put(t, s, "enzo", "Photos/plage.jpg", "jpeg")
	when := time.Date(2018, 8, 15, 10, 0, 0, 0, time.UTC)
	for _, p := range []string{"Photos/plage.jpg", "Photos"} {
		got, err := s.SetModTime(ctx, "enzo", p, when)
		if err != nil {
			t.Fatal(err)
		}
		if !got.ModifiedAt.Equal(when) {
			t.Errorf("%s: modified %v", p, got.ModifiedAt)
		}
		info, err := os.Stat(s.diskPath("enzo", p))
		if err != nil || !info.ModTime().Equal(when) {
			t.Errorf("%s on disk: %v, %v", p, info.ModTime(), err)
		}
	}
	if diskContent(t, s, "enzo", "Photos/plage.jpg") != "jpeg" {
		t.Error("the content changed")
	}
	if got, _ := s.Lookup(ctx, "enzo", "Photos/plage.jpg"); got.ID != f.ID || !bytes.Equal(got.SHA256, f.SHA256) {
		t.Errorf("the row changed beyond its date: %+v", got)
	}
	if _, err := s.SetModTime(ctx, "enzo", "missing.txt", when); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	assertClean(t, s)
}
