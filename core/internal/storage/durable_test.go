package storage

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recordSyncs wraps syncFile and syncDir for the test, and records the path
// of each file and folder they make durable.
func recordSyncs(t *testing.T) (files, dirs *[]string) {
	t.Helper()
	var fileLog, dirLog []string
	origFile, origDir := syncFile, syncDir
	syncFile = func(f *os.File) error {
		fileLog = append(fileLog, filepath.Clean(f.Name()))
		return origFile(f)
	}
	syncDir = func(dir string) error {
		dirLog = append(dirLog, filepath.Clean(dir))
		return origDir(dir)
	}
	t.Cleanup(func() { syncFile, syncDir = origFile, origDir })
	return &fileLog, &dirLog
}

// assertSynced fails unless every folder in want was synced.
func assertSynced(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(got, filepath.Clean(w)) {
			t.Errorf("%s: %s was not synced (synced: %v)", what, w, got)
		}
	}
}

// A file put into folders that do not exist yet is on the disk, with every
// new folder's name, when Put returns: a power cut afterwards loses nothing.
func TestPutIsDurableWhenItReturns(t *testing.T) {
	s := newStore(t)
	files, dirs := recordSyncs(t)

	put(t, s, "enzo", "Neuf/2024/plage.jpg", "jpg")

	if !slices.ContainsFunc(*files, func(f string) bool { return strings.HasPrefix(filepath.Base(f), "put-") }) {
		t.Errorf("the content was not synced before it was linked (synced files: %v)", *files)
	}
	dst := s.diskPath("enzo", "Neuf/2024/plage.jpg")
	assertSynced(t, "put", *dirs,
		filepath.Dir(dst),                             // the file's name
		filepath.Dir(filepath.Dir(dst)),               // the new 2024
		filepath.Dir(filepath.Dir(filepath.Dir(dst))), // the new Neuf
		s.Root, // the new enzo
	)
}

// Each answer of a resumable upload comes after the disk has what it says:
// the partial file and its name at creation, each chunk's bytes, then the
// placed file's name and its new folder.
func TestUploadIsDurableBeforeEachAnswer(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	files, dirs := recordSyncs(t)
	content := "0123456789"

	u, err := s.CreateUpload(ctx, "enzo", "Vidéos/plage.mp4", int64(len(content)), nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	partial := s.uploadFile(u.ID)
	if !slices.Contains(*files, filepath.Clean(partial)) {
		t.Errorf("create: the partial file was not synced (synced files: %v)", *files)
	}
	assertSynced(t, "create", *dirs, s.uploadsDir(), filepath.Dir(s.uploadsDir()), s.Root)

	*files, *dirs = nil, nil
	if _, _, err := s.AppendUpload(ctx, "enzo", u.ID, 0, strings.NewReader(content[:4])); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(*files, filepath.Clean(partial)) {
		t.Errorf("chunk: the bytes were not synced (synced files: %v)", *files)
	}

	*files, *dirs = nil, nil
	_, f, err := s.AppendUpload(ctx, "enzo", u.ID, 4, strings.NewReader(content[4:]))
	if err != nil || f == nil {
		t.Fatalf("last chunk: %+v, %v", f, err)
	}
	if !slices.Contains(*files, filepath.Clean(partial)) {
		t.Errorf("last chunk: the bytes were not synced (synced files: %v)", *files)
	}
	dst := s.diskPath("enzo", "Vidéos/plage.mp4")
	assertSynced(t, "last chunk", *dirs, filepath.Dir(dst), filepath.Dir(filepath.Dir(dst)))
	if b, _ := os.ReadFile(dst); string(b) != content {
		t.Errorf("stored %q", b)
	}
	assertClean(t, s)
}

// Replacing a file syncs both names it changes: the new content's and the old
// content's, now a version.
func TestReplaceIsDurableWhenItReturns(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "notes.txt", "v1")
	_, dirs := recordSyncs(t)

	f, err := s.Put(ctx, "enzo", "notes.txt", strings.NewReader("v2"), PutOptions{ExpectSize: -1, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := s.Pool.QueryRow(ctx, `SELECT id FROM file_versions WHERE file_id = $1`, f.ID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	assertSynced(t, "replace", *dirs,
		filepath.Dir(s.diskPath("enzo", "notes.txt")),
		filepath.Dir(s.versionDisk("enzo", f.ID, versionID)),
	)
	assertClean(t, s)
}
