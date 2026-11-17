package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func replaceWith(t *testing.T, s *Store, owner, p, content string) File {
	t.Helper()
	f, err := s.Put(context.Background(), owner, p, strings.NewReader(content), PutOptions{ExpectSize: -1, Replace: true})
	if err != nil {
		t.Fatalf("replace %s: %v", p, err)
	}
	return f
}

func versionContents(t *testing.T, s *Store, owner, p string) []string {
	t.Helper()
	versions, err := s.ListVersions(context.Background(), owner, p)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range versions {
		r, _, err := s.OpenVersion(context.Background(), owner, v.ID)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		r.Close()
		if sum := sha256.Sum256(b); !bytes.Equal(sum[:], v.SHA256) {
			t.Errorf("version %d: hash differs from its row", v.ID)
		}
		out = append(out, string(b))
	}
	return out
}

func TestReplaceKeepsEveryPreviousContent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := put(t, s, "enzo", "Docs/rapport.txt", "v1")

	// Without Replace, an existing path is still refused.
	if _, err := s.Put(ctx, "enzo", "Docs/rapport.txt", strings.NewReader("x"), NoExpectations()); !errors.Is(err, ErrExists) {
		t.Fatalf("plain put over a file: %v", err)
	}

	f := replaceWith(t, s, "enzo", "Docs/rapport.txt", "v2 plus longue")
	if f.ID != first.ID || f.Size != int64(len("v2 plus longue")) {
		t.Errorf("replaced row %+v (same id expected)", f)
	}
	replaceWith(t, s, "enzo", "Docs/rapport.txt", "v3")
	if b, _ := os.ReadFile(s.diskPath("enzo", "Docs/rapport.txt")); string(b) != "v3" {
		t.Errorf("current = %q", b)
	}
	if got := versionContents(t, s, "enzo", "Docs/rapport.txt"); fmt.Sprint(got) != "[v2 plus longue v1]" {
		t.Errorf("versions = %v, want newest first", got)
	}
	if journal(t, s) != 0 {
		t.Error("journal not cleared")
	}
	assertClean(t, s)

	// Replace on a new path is a plain put; on a folder it is refused.
	if f := replaceWith(t, s, "enzo", "Docs/nouveau.txt", "n"); f.Size != 1 {
		t.Errorf("replace a missing file: %+v", f)
	}
	if _, err := s.Put(ctx, "enzo", "Docs", strings.NewReader("x"), PutOptions{ExpectSize: -1, Replace: true}); !errors.Is(err, ErrExists) {
		t.Errorf("replace a folder: %v", err)
	}
}

func TestRestoreAVersionKeepsTheCurrentOne(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mod := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.Put(ctx, "enzo", "plan.txt", strings.NewReader("original"), PutOptions{ExpectSize: -1, ModTime: mod}); err != nil {
		t.Fatal(err)
	}
	replaceWith(t, s, "enzo", "plan.txt", "raté")
	versions, _ := s.ListVersions(ctx, "enzo", "plan.txt")

	f, err := s.RestoreVersion(ctx, "enzo", versions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "plan.txt")); string(b) != "original" {
		t.Errorf("restored content %q", b)
	}
	if !f.ModifiedAt.Equal(mod) {
		t.Errorf("restored modified_at %v, want the version's %v", f.ModifiedAt, mod)
	}
	if got := versionContents(t, s, "enzo", "plan.txt"); fmt.Sprint(got) != "[raté original]" {
		t.Errorf("versions after restore = %v", got)
	}
	if _, err := s.RestoreVersion(ctx, "johan", versions[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("johan restores enzo's version: %v", err)
	}
	assertClean(t, s)
}

func TestRecoverAReplaceAfterACrash(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	f := put(t, s, "enzo", "a.txt", "ancien")
	intend := func() int64 {
		t.Helper()
		var vid int64
		if err := s.Pool.QueryRow(ctx, `INSERT INTO file_versions (file_id, owner, size, mime, sha256, modified_at)
			SELECT id, owner, size, mime, sha256, modified_at FROM files WHERE id = $1 RETURNING id`, f.ID).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op, version_id) VALUES ('enzo', 'a.txt', '', 'version', $1)`, vid); err != nil {
			t.Fatal(err)
		}
		return vid
	}
	countVersions := func() int {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM file_versions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1. Crash before anything moved: the intention is forgotten.
	intend()
	if _, dropped, err := s.RecoverMoves(ctx); err != nil || dropped != 1 || countVersions() != 0 {
		t.Fatalf("crash before the rename: dropped %d, versions %d, %v", dropped, countVersions(), err)
	}

	// 2. Crash after the old content was set aside, before the new one arrived:
	// the old content goes back.
	vid := intend()
	if err := os.MkdirAll(s.versionsDir("enzo", f.ID), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.diskPath("enzo", "a.txt"), s.versionDisk("enzo", f.ID, vid)); err != nil {
		t.Fatal(err)
	}
	if _, dropped, err := s.RecoverMoves(ctx); err != nil || dropped != 1 {
		t.Fatalf("crash between rename and link: %d, %v", dropped, err)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "a.txt")); string(b) != "ancien" {
		t.Errorf("old content not put back: %q", b)
	}
	assertClean(t, s)

	// 3. Crash after the new content was linked: the index is finished from the disk.
	vid = intend()
	if err := os.Rename(s.diskPath("enzo", "a.txt"), s.versionDisk("enzo", f.ID, vid)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.diskPath("enzo", "a.txt"), []byte("nouveau"), 0o600); err != nil {
		t.Fatal(err)
	}
	if finished, _, err := s.RecoverMoves(ctx); err != nil || finished != 1 {
		t.Fatalf("crash after the link: %d, %v", finished, err)
	}
	if got := versionContents(t, s, "enzo", "a.txt"); fmt.Sprint(got) != "[ancien]" {
		t.Errorf("versions = %v", got)
	}
	assertClean(t, s)
}

func TestVersionRetention(t *testing.T) {
	s := newStore(t)
	s.VersionsKeep = 3
	ctx := context.Background()
	put(t, s, "enzo", "a.txt", "v0")
	for i := 1; i <= 5; i++ {
		replaceWith(t, s, "enzo", "a.txt", fmt.Sprintf("v%d", i))
	}
	// All five are younger than 30 days: kept.
	if got := versionContents(t, s, "enzo", "a.txt"); len(got) != 5 {
		t.Fatalf("young versions = %v", got)
	}
	// Age them: only the last three survive.
	if _, err := s.Pool.Exec(ctx, `UPDATE file_versions SET created_at = created_at - interval '40 days'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneVersions(ctx); err != nil || n != 2 {
		t.Fatalf("PruneVersions = %d, %v", n, err)
	}
	if got := versionContents(t, s, "enzo", "a.txt"); fmt.Sprint(got) != "[v4 v3 v2]" {
		t.Errorf("kept versions = %v", got)
	}
	assertClean(t, s)
}

func TestTrashPurgeErasesTheVersions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	f := put(t, s, "enzo", "Docs/a.txt", "v1")
	replaceWith(t, s, "enzo", "Docs/a.txt", "v2")
	item, err := s.Trash(ctx, "enzo", "Docs")
	if err != nil {
		t.Fatal(err)
	}
	// In the trash, the versions stay; restored, they come back.
	if _, err := s.Restore(ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	if got := versionContents(t, s, "enzo", "Docs/a.txt"); fmt.Sprint(got) != "[v1]" {
		t.Errorf("versions after restore = %v", got)
	}
	item, _ = s.Trash(ctx, "enzo", "Docs")
	if err := s.Purge(ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.versionsDir("enzo", f.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("versions left after purge: %v", err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM file_versions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("version rows left: %d, %v", n, err)
	}
}

func TestReplacingUpload(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "video.mp4", "ancienne")
	if _, err := s.CreateUpload(ctx, "enzo", "video.mp4", 3, nil, nil, 0, false); !errors.Is(err, ErrExists) {
		t.Fatalf("upload over a file without replace: %v", err)
	}
	u, err := s.CreateUpload(ctx, "enzo", "video.mp4", 3, nil, nil, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AppendUpload(ctx, "enzo", u.ID, 0, strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if got := versionContents(t, s, "enzo", "video.mp4"); fmt.Sprint(got) != "[ancienne]" {
		t.Errorf("versions = %v", got)
	}
	assertClean(t, s)
}

func TestVersionEraseGuards(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	victim := s.Root + "/outside"
	if err := os.MkdirAll(victim, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := s.removeVersionFile("../..", 1, 1); err == nil {
		t.Error("removeVersionFile accepted a crafted owner")
	}
	if err := s.removeVersionsDir("enzo/../..", 1); err == nil {
		t.Error("removeVersionsDir accepted a crafted owner")
	}
	if err := s.removeVersionsDir("enzo", 0); err == nil {
		t.Error("removeVersionsDir accepted id 0")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a folder outside the versions was erased: %v", err)
	}
}
