package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	return &Store{Root: t.TempDir(), Pool: pool}
}

func put(t *testing.T, s *Store, owner, p, content string) File {
	t.Helper()
	f, err := s.Put(context.Background(), owner, p, strings.NewReader(content), NoExpectations())
	if err != nil {
		t.Fatalf("put %s: %v", p, err)
	}
	return f
}

// count returns the number of indexed files, folders left out.
func count(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM files WHERE kind = 'file'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// assertNothingLeft checks that a failed write left no file, no index row and no temporary file.
func assertNothingLeft(t *testing.T, s *Store, owner, p string, rows int) {
	t.Helper()
	if _, err := os.Lstat(s.diskPath(owner, p)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s/%s is on disk after a failed write (err %v)", owner, p, err)
	}
	if n := count(t, s); n != rows {
		t.Errorf("index has %d rows, want %d", n, rows)
	}
	entries, _ := os.ReadDir(s.tmpDir())
	if len(entries) != 0 {
		t.Errorf("temporary files left: %d", len(entries))
	}
}

func TestPutWritesAndIndexes(t *testing.T) {
	s := newStore(t)
	mod := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	content := "contrat de l'appart"
	want := sha256.Sum256([]byte(content))

	f, err := s.Put(context.Background(), "enzo", "Documents/contrat.pdf", strings.NewReader(content),
		PutOptions{ModTime: mod, ExpectSize: int64(len(content)), ExpectSHA256: want[:]})
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(s.Root, "enzo", "Documents", "contrat.pdf"))
	if err != nil || string(got) != content {
		t.Fatalf("disk content = %q, %v", got, err)
	}
	if f.Owner != "enzo" || f.Path != "Documents/contrat.pdf" || f.Size != int64(len(content)) ||
		!bytes.Equal(f.SHA256, want[:]) || f.MIME != "application/pdf" || !f.ModifiedAt.Equal(mod) {
		t.Errorf("indexed %+v", f)
	}
	info, _ := os.Stat(s.diskPath("enzo", "Documents/contrat.pdf"))
	if !info.ModTime().Equal(mod) {
		t.Errorf("disk mtime = %v, want %v", info.ModTime(), mod)
	}
	r, opened, err := s.Open(context.Background(), "enzo", "Documents/contrat.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if b, _ := io.ReadAll(r); string(b) != content || opened.ID != f.ID {
		t.Errorf("Open = %q, %+v", b, opened)
	}
	assertNothingLeft(t, s, "enzo", "absent.txt", 1)
}

type failingReader struct{ sent int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.sent >= 3 {
		return 0, errors.New("connection lost")
	}
	r.sent++
	return copy(p, "partial "), nil
}

func TestInterruptedWriteLeavesNothing(t *testing.T) {
	s := newStore(t)
	_, err := s.Put(context.Background(), "enzo", "video.mp4", &failingReader{}, NoExpectations())
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("err = %v, want the read error", err)
	}
	assertNothingLeft(t, s, "enzo", "video.mp4", 0)
}

func TestMismatchIsRefused(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	wrong := sha256.Sum256([]byte("something else"))

	_, err := s.Put(ctx, "enzo", "a.txt", strings.NewReader("abc"), PutOptions{ExpectSize: -1, ExpectSHA256: wrong[:]})
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("hash: err = %v, want ErrMismatch", err)
	}
	_, err = s.Put(ctx, "enzo", "a.txt", strings.NewReader("abc"), PutOptions{ExpectSize: 4})
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("size: err = %v, want ErrMismatch", err)
	}
	assertNothingLeft(t, s, "enzo", "a.txt", 0)
}

func TestConflictsAreRefusedNeverOverwritten(t *testing.T) {
	s := newStore(t)
	put(t, s, "enzo", "Factures/Facture.pdf", "original")
	put(t, s, "enzo", "notes", "a file named notes")

	for _, p := range []string{
		"Factures/Facture.pdf", // same path
		"Factures/facture.pdf", // same name, another case
		"factures/autre.pdf",   // folder exists in another case
		"Factures",             // a folder already has this name
		"notes/idée.txt",       // a file already has the folder's name
	} {
		_, err := s.Put(context.Background(), "enzo", p, strings.NewReader("new"), NoExpectations())
		if !errors.Is(err, ErrExists) {
			t.Errorf("put %q: err = %v, want ErrExists", p, err)
		}
	}
	if got, _ := os.ReadFile(s.diskPath("enzo", "Factures/Facture.pdf")); string(got) != "original" {
		t.Errorf("original overwritten: %q", got)
	}
	if n := count(t, s); n != 2 {
		t.Errorf("index has %d rows, want 2", n)
	}

	// Another owner has its own space.
	put(t, s, "johan", "Factures/Facture.pdf", "johan's")
}

func TestFileOnDiskButUnindexedIsNotReplaced(t *testing.T) {
	s := newStore(t)
	disk := s.diskPath("enzo", "orphan.txt")
	if err := os.MkdirAll(filepath.Dir(disk), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("left by a crash"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := s.Put(context.Background(), "enzo", "orphan.txt", strings.NewReader("new"), NoExpectations())
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if got, _ := os.ReadFile(disk); string(got) != "left by a crash" {
		t.Errorf("unindexed file replaced: %q", got)
	}
	if n := count(t, s); n != 0 {
		t.Errorf("index has %d rows, want 0 (rolled back)", n)
	}
}

func TestConcurrentPutsOfOnePathOneWins(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Put(context.Background(), "enzo", "same.txt", strings.NewReader("x"), NoExpectations())
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrExists):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || count(t, s) != 1 {
		t.Errorf("%d puts succeeded, %d rows; want 1 and 1", ok, count(t, s))
	}
}

func TestCleanPath(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../b", "a/./b", "a//b", "a/", `a\b`, "a\x01b", "a\x7fb", "\xff", strings.Repeat("x", 256)} {
		if _, err := CleanPath(bad); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CleanPath(%q) = %v, want ErrInvalidPath", bad, err)
		}
	}
	nfd := "été/résumé.txt" // as macOS often spells "été/résumé.txt"
	got, err := CleanPath(nfd)
	if err != nil || got != "été/résumé.txt" {
		t.Errorf("CleanPath(NFD) = %q, %v; want NFC", got, err)
	}
	for _, bad := range []string{"", "Enzo", ".cloudos", "a/b", strings.Repeat("a", 65)} {
		if _, err := CleanOwner(bad); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CleanOwner(%q) = %v, want ErrInvalidPath", bad, err)
		}
	}
}

func TestNFDNameIsStoredAsNFC(t *testing.T) {
	s := newStore(t)
	f := put(t, s, "enzo", "été.txt", "x")
	if f.Path != "été.txt" {
		t.Errorf("indexed path %q", f.Path)
	}
	if _, err := s.Lookup(context.Background(), "enzo", "été.txt"); err != nil {
		t.Errorf("lookup by the NFD spelling: %v", err)
	}
}

func TestVerify(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "ok.txt", "fine")
	put(t, s, "enzo", "changed.txt", "before")
	put(t, s, "enzo", "gone.txt", "bye")

	rep, err := s.Verify(ctx)
	if err != nil || !rep.Clean() || rep.OK != 3 {
		t.Fatalf("clean store: %+v, %v", rep, err)
	}

	if err := os.WriteFile(s.diskPath("enzo", "changed.txt"), []byte("after!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.diskPath("enzo", "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.diskPath("enzo", "dropped.txt"), []byte("by hand"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err = s.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range rep.Problems {
		got[p.Path] = p.Kind
	}
	want := map[string]string{"changed.txt": "modified", "gone.txt": "missing", "dropped.txt": "unindexed"}
	if rep.OK != 1 || len(got) != len(want) {
		t.Errorf("report %+v", rep)
	}
	for p, kind := range want {
		if got[p] != kind {
			t.Errorf("%s: %q, want %q", p, got[p], kind)
		}
	}
	// Verify only reports: the modified file and the dropped one are untouched.
	if b, _ := os.ReadFile(s.diskPath("enzo", "changed.txt")); string(b) != "after!" {
		t.Errorf("verify changed a file: %q", b)
	}
	if n := count(t, s); n != 3 {
		t.Errorf("verify changed the index: %d rows", n)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportKeepsEveryHashAndOnlyReadsTheSource(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	src := t.TempDir()
	files := map[string]string{
		"Photos/2024/plage.jpg":   "\xff\xd8\xff jpeg",
		"Photos/2024/chien.heic":  "heic",
		"Documents/contrat.pdf":   "%PDF-1.7",
		"Documents/sous/note.txt": "note",
		"racine.txt":              "root",
	}
	writeTree(t, src, files)
	if err := os.Symlink(filepath.Join(src, "racine.txt"), filepath.Join(src, "lien")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, src)

	rep, err := s.Import(ctx, "enzo", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Imported) != len(files) || len(rep.Skipped) != 1 || rep.Skipped[0].Path != "lien" {
		t.Fatalf("report %+v", rep)
	}
	for p, content := range files {
		f, err := s.Lookup(ctx, "enzo", p)
		want := sha256.Sum256([]byte(content))
		if err != nil || !bytes.Equal(f.SHA256, want[:]) {
			t.Errorf("%s: %+v, %v", p, f, err)
		}
		srcInfo, _ := os.Stat(filepath.Join(src, filepath.FromSlash(p)))
		// Postgres keeps microseconds; the stored file keeps the source's exact time.
		if !f.ModifiedAt.Equal(srcInfo.ModTime().Truncate(time.Microsecond)) {
			t.Errorf("%s: modified_at %v, source %v", p, f.ModifiedAt, srcInfo.ModTime())
		}
		if diskInfo, _ := os.Stat(s.diskPath("enzo", p)); !diskInfo.ModTime().Equal(srcInfo.ModTime()) {
			t.Errorf("%s: disk mtime %v, source %v", p, diskInfo.ModTime(), srcInfo.ModTime())
		}
	}
	if after := snapshot(t, src); !equalSnapshots(before, after) {
		t.Error("the source changed during the import")
	}
	if rep, err := s.Verify(ctx); err != nil || !rep.Clean() {
		t.Errorf("verify after import: %+v, %v", rep, err)
	}

	// A second import changes nothing.
	again, err := s.Import(ctx, "enzo", src)
	if err != nil || len(again.Present) != len(files) || len(again.Imported) != 0 {
		t.Errorf("second import: %+v, %v", again, err)
	}
}

func TestImportAdoptsAFileLeftUnindexed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.txt": "same", "b.txt": "source"})
	// As after a crash between the link and the commit: a complete file, no row.
	writeTree(t, filepath.Join(s.Root, "enzo"), map[string]string{"a.txt": "same", "b.txt": "different"})

	rep, err := s.Import(ctx, "enzo", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Adopted) != 1 || rep.Adopted[0] != "a.txt" || len(rep.Skipped) != 1 || rep.Skipped[0].Path != "b.txt" {
		t.Errorf("report %+v", rep)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "b.txt")); string(b) != "different" {
		t.Errorf("b.txt replaced: %q", b)
	}
}

func TestImportReportsCaseConflicts(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"Facture.pdf": "one"})
	if err := os.WriteFile(filepath.Join(src, "facture.pdf"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(src); len(entries) != 2 {
		t.Skip("the test folder's file system ignores case, so the source cannot hold both names")
	}
	s := newStore(t)
	rep, err := s.Import(context.Background(), "enzo", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Imported) != 1 || len(rep.Skipped) != 1 {
		t.Errorf("report %+v", rep)
	}
}

func TestCleanTmp(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if n, err := s.CleanTmp(); n != 0 || err != nil {
		t.Fatalf("no tmp folder: %d, %v", n, err)
	}
	writeTree(t, s.tmpDir(), map[string]string{"put-1": "partial", "put-2": "partial"})
	if n, err := s.CleanTmp(); n != 2 || err != nil {
		t.Fatalf("CleanTmp = %d, %v", n, err)
	}
	if entries, _ := os.ReadDir(s.tmpDir()); len(entries) != 0 {
		t.Errorf("left %d", len(entries))
	}
}

type fileState struct {
	sum  [32]byte
	mod  time.Time
	mode os.FileMode
}

func snapshot(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		st := fileState{mod: info.ModTime(), mode: info.Mode()}
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			st.sum = sha256.Sum256(b)
		}
		out[p] = st
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func equalSnapshots(a, b map[string]fileState) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || v.sum != w.sum || !v.mod.Equal(w.mod) || v.mode != w.mode {
			return false
		}
	}
	return true
}

func TestHEIFIsRecognisedByExtensionAndContent(t *testing.T) {
	box := func(brand string) []byte { return []byte("\x00\x00\x00\x18ftyp" + brand + "\x00\x00\x00\x00mif1heic") }
	for _, c := range []struct {
		path string
		head []byte
		want string
	}{
		{"iPhone/IMG_0001.HEIC", nil, "image/heic"}, // upper case, as an iPhone names it
		{"a.heif", nil, "image/heif"},
		{"photo", box("heic"), "image/heic"},
		{"photo", box("heix"), "image/heic"},
		{"photo", box("mif1"), "image/heif"},
		{"photo", box("msf1"), "image/heif"},
		{"notes", []byte("hello"), "text/plain; charset=utf-8"},
	} {
		if got := detectMIME(c.path, c.head); got != c.want {
			t.Errorf("detectMIME(%q, %q) = %q, want %q", c.path, c.head, got, c.want)
		}
	}
	for _, head := range [][]byte{box("avif"), box("isom"), []byte("\x00\x00\x00\x18ftyphe")} {
		if got := SniffMIME(head); got == "image/heic" || got == "image/heif" {
			t.Errorf("SniffMIME(%q) = %q", head, got)
		}
	}
}

func TestDocumentsAreRecognisedByExtension(t *testing.T) {
	for p, want := range map[string]string{
		"Contrats/bail.pdf": "application/pdf",
		"lettre.docx":       "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"budget.xlsx":       "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"slides.pptx":       "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"lettre.odt":        "application/vnd.oasis.opendocument.text",
		"budget.ods":        "application/vnd.oasis.opendocument.spreadsheet",
		"slides.odp":        "application/vnd.oasis.opendocument.presentation",
		"README.md":         "text/markdown; charset=utf-8",
		"notes.markdown":    "text/markdown; charset=utf-8",
		"export.tsv":        "text/tab-separated-values; charset=utf-8",
		"comptes.csv":       "text/csv; charset=utf-8",
		"notes.txt":         "text/plain; charset=utf-8",
	} {
		// The content says zip or text; the extension wins.
		if got := detectMIME(p, []byte("PK\x03\x04")); got != want {
			t.Errorf("detectMIME(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestVideosAreRecognisedByExtensionAndContent(t *testing.T) {
	box := func(brand, compat string) []byte {
		return []byte("\x00\x00\x00\x18ftyp" + brand + "\x00\x00\x02\x00" + compat + "\x00\x00\x00\x08free")
	}
	for _, c := range []struct {
		path string
		head []byte
		want string
	}{
		{"iPhone/IMG_0002.MOV", nil, "video/quicktime"}, // upper case, as an iPhone names it
		{"clip.mp4", nil, "video/mp4"},
		{"clip.m4v", nil, "video/mp4"},
		{"video", box("qt  ", "qt  "), "video/quicktime"},
		{"video", box("isom", "iso2"), "video/mp4"}, // no "mp4" brand: http.DetectContentType misses it
		{"video", box("mp42", "isom"), "video/mp4"},
		{"video", box("M4V ", "M4V "), "video/mp4"},
	} {
		if got := detectMIME(c.path, c.head); got != c.want {
			t.Errorf("detectMIME(%q, %q) = %q, want %q", c.path, c.head, got, c.want)
		}
	}
	for _, head := range [][]byte{box("M4A ", "isom"), box("avif", "mif1"), box("crx ", "isom")} {
		if got := SniffMIME(head); strings.HasPrefix(got, "video/") {
			t.Errorf("SniffMIME(%q) = %q", head, got)
		}
	}
}
