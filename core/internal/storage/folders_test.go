package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func names(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
		if f.IsDir() {
			out[i] += "/"
		}
	}
	return out
}

func assertClean(t *testing.T, s *Store) {
	t.Helper()
	rep, err := s.Verify(context.Background())
	if err != nil || !rep.Clean() {
		t.Fatalf("verify: %+v, %v", rep.Problems, err)
	}
}

func journal(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM moves`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPutCreatesParentFoldersAndListShowsChildren(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "Photos/2024/plage.jpg", "jpg")
	put(t, s, "enzo", "Photos/chien.jpg", "jpg")
	put(t, s, "enzo", "notes.txt", "txt")
	if _, err := s.Mkdir(ctx, "enzo", "Vide/Sous"); err != nil {
		t.Fatal(err)
	}

	root, err := s.List(ctx, "enzo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(names(root)); got != "[Photos/ Vide/ notes.txt]" {
		t.Errorf("root = %s", got)
	}
	photos, _ := s.List(ctx, "enzo", "Photos")
	if got := fmt.Sprint(names(photos)); got != "[Photos/2024/ Photos/chien.jpg]" {
		t.Errorf("Photos = %s", got)
	}
	if _, err := s.List(ctx, "enzo", "notes.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("list a file: %v", err)
	}
	if _, err := s.List(ctx, "enzo", "Absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("list a missing folder: %v", err)
	}
	if info, err := os.Stat(s.diskPath("enzo", "Vide/Sous")); err != nil || !info.IsDir() {
		t.Errorf("empty folder on disk: %v", err)
	}
	if _, err := s.Mkdir(ctx, "enzo", "photos"); !errors.Is(err, ErrExists) {
		t.Errorf("folder in another case: %v", err)
	}
	if _, err := s.Mkdir(ctx, "enzo", "notes.txt/x"); !errors.Is(err, ErrExists) {
		t.Errorf("folder under a file: %v", err)
	}
	// Another owner sees nothing of enzo's space.
	if other, err := s.List(ctx, "johan", ""); err != nil || len(other) != 0 {
		t.Errorf("johan's root: %v, %v", names(other), err)
	}
	assertClean(t, s)
}

func TestMoveFileAndFolder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "Inbox/facture.pdf", "pdf")
	put(t, s, "enzo", "Projets/Box/plan.txt", "plan")
	put(t, s, "enzo", "Projets/Box/schema/v1.png", "png")

	f, err := s.Move(ctx, "enzo", "Inbox/facture.pdf", "Papiers/2026/facture-edf.pdf")
	if err != nil || f.Path != "Papiers/2026/facture-edf.pdf" {
		t.Fatalf("move file: %+v, %v", f, err)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "Papiers/2026/facture-edf.pdf")); string(b) != "pdf" {
		t.Errorf("moved file content %q", b)
	}

	if _, err := s.Move(ctx, "enzo", "Projets/Box", "Archives/Box 2025"); err != nil {
		t.Fatalf("move folder: %v", err)
	}
	for _, p := range []string{"Archives/Box 2025", "Archives/Box 2025/plan.txt", "Archives/Box 2025/schema", "Archives/Box 2025/schema/v1.png"} {
		if _, err := s.Lookup(ctx, "enzo", p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := s.Lookup(ctx, "enzo", "Projets/Box/plan.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old path still indexed: %v", err)
	}
	if journal(t, s) != 0 {
		t.Error("journal not cleared")
	}
	assertClean(t, s)
}

func TestMoveRefusals(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "a.txt", "a")
	put(t, s, "enzo", "b.txt", "b")
	put(t, s, "enzo", "Dossier/c.txt", "c")

	for _, c := range []struct {
		from, to string
		want     error
	}{
		{"a.txt", "b.txt", ErrExists},               // target taken
		{"a.txt", "B.TXT", ErrExists},               // target taken in another case
		{"a.txt", "b.txt/x.txt", ErrExists},         // parent is a file
		{"Dossier", "Dossier/Sous", ErrInvalidPath}, // into itself
		{"Dossier", "dossier/Sous", ErrInvalidPath}, // into itself, other case
		{"absent.txt", "x.txt", ErrNotFound},
		{"a.txt", "../evasion.txt", ErrInvalidPath},
	} {
		if _, err := s.Move(ctx, "enzo", c.from, c.to); !errors.Is(err, c.want) {
			t.Errorf("Move(%q, %q) = %v, want %v", c.from, c.to, err, c.want)
		}
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "b.txt")); string(b) != "b" {
		t.Errorf("b.txt overwritten: %q", b)
	}

	// A file on disk but not indexed is never replaced, and the journal is cleared.
	if err := os.WriteFile(s.diskPath("enzo", "orphan.txt"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Move(ctx, "enzo", "a.txt", "orphan.txt"); !errors.Is(err, ErrExists) {
		t.Errorf("move onto an unindexed file: %v", err)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "orphan.txt")); string(b) != "orphan" {
		t.Errorf("orphan replaced: %q", b)
	}
	if journal(t, s) != 0 {
		t.Error("journal not cleared after a refused rename")
	}

	// A rename that only changes case is allowed.
	if f, err := s.Move(ctx, "enzo", "a.txt", "A.txt"); err != nil || f.Path != "A.txt" {
		t.Errorf("case-only rename: %+v, %v", f, err)
	}
	if f, err := s.Move(ctx, "enzo", "Dossier", "DOSSIER"); err != nil || f.Path != "DOSSIER" {
		t.Errorf("case-only folder rename: %+v, %v", f, err)
	}
	if _, err := s.Lookup(ctx, "enzo", "DOSSIER/c.txt"); err != nil {
		t.Errorf("child after case-only rename: %v", err)
	}
}

func TestRecoverMovesAfterACrash(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "avant/fini.txt", "1")
	put(t, s, "enzo", "avant/annule.txt", "2")
	put(t, s, "enzo", "avant/bloque.txt", "3")
	put(t, s, "enzo", "ailleurs/bloque.txt", "other")
	intend := func(from, to string) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path) VALUES ('enzo', $1, $2)`, from, to); err != nil {
			t.Fatal(err)
		}
	}

	// Crash after the rename: the file is at the target, the index still says the source.
	intend("avant/fini.txt", "apres/fini.txt")
	if err := os.MkdirAll(s.diskPath("enzo", "apres"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.diskPath("enzo", "avant/fini.txt"), s.diskPath("enzo", "apres/fini.txt")); err != nil {
		t.Fatal(err)
	}
	// Crash before the rename: nothing moved on disk.
	intend("avant/annule.txt", "apres/annule.txt")
	// Both ends exist: never guessed.
	intend("avant/bloque.txt", "ailleurs/bloque.txt")

	finished, dropped, err := s.RecoverMoves(ctx)
	if err != nil || finished != 1 || dropped != 1 {
		t.Fatalf("recover: finished %d, dropped %d, %v", finished, dropped, err)
	}
	if _, err := s.Lookup(ctx, "enzo", "apres/fini.txt"); err != nil {
		t.Errorf("finished move not indexed: %v", err)
	}
	if _, err := s.Lookup(ctx, "enzo", "avant/annule.txt"); err != nil {
		t.Errorf("dropped move changed the index: %v", err)
	}
	if journal(t, s) != 1 {
		t.Errorf("the ambiguous move should stay in the journal")
	}
	for p, want := range map[string]string{"avant/bloque.txt": "3", "ailleurs/bloque.txt": "other"} {
		if b, _ := os.ReadFile(s.diskPath("enzo", p)); string(b) != want {
			t.Errorf("%s = %q, want %q", p, b, want)
		}
	}
	assertClean(t, s)
}

// TestConcurrentMovesAndPutsStayConsistent moves a folder to a new name again
// and again while files are written into whatever folder the writer sees: a
// write may land before a move, after it, or recreate the old name; the index
// and the disk must agree at the end.
func TestConcurrentMovesAndPutsStayConsistent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Mkdir(ctx, "enzo", "d00"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var moved, written int
	wg.Add(2)
	go func() {
		defer wg.Done()
		current := "d00"
		for i := 1; i <= 15; i++ {
			next := fmt.Sprintf("d%02d", i)
			if _, err := s.Move(ctx, "enzo", current, next); err != nil {
				t.Errorf("move %s to %s: %v", current, next, err)
				return
			}
			current = next
			moved++
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 30 {
			root, err := s.List(ctx, "enzo", "")
			if err != nil || len(root) == 0 {
				t.Errorf("list: %v", err)
				return
			}
			_, err = s.Put(ctx, "enzo", fmt.Sprintf("%s/f%02d.txt", root[len(root)-1].Path, i), strings.NewReader("x"), NoExpectations())
			switch {
			case err == nil:
				written++
			case !errors.Is(err, ErrExists):
				t.Errorf("put %d: %v", i, err)
			}
		}
	}()
	wg.Wait()
	t.Logf("%d moves and %d writes succeeded", moved, written)
	if moved != 15 || written < 20 {
		t.Fatalf("the test did not exercise both: %d moves, %d writes", moved, written)
	}
	assertClean(t, s)
}
