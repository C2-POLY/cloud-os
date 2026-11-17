package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestTrashAndRestoreAFolder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "Projets/Box/plan.txt", "plan")
	put(t, s, "enzo", "Projets/Box/schema/v1.png", "png")
	put(t, s, "enzo", "Projets/autre.txt", "autre")

	item, err := s.Trash(ctx, "enzo", "Projets/Box")
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != KindDir || item.Size != int64(len("plan")+len("png")) || item.ExpiresAt.Sub(item.DeletedAt) < 29*24*3600e9 {
		t.Errorf("trash item %+v", item)
	}
	// Gone from the drive, on disk in the trash, its name free again.
	if got, _ := s.List(ctx, "enzo", "Projets"); fmt.Sprint(names(got)) != "[Projets/autre.txt]" {
		t.Errorf("Projets after trash = %v", names(got))
	}
	if _, err := s.Lookup(ctx, "enzo", "Projets/Box/plan.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("trashed child still found: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.trashDisk("enzo", item.ID, "Projets/Box"), "plan.txt")); string(b) != "plan" {
		t.Errorf("bytes in the trash: %q", b)
	}
	assertClean(t, s)

	f, err := s.Restore(ctx, "enzo", item.ID)
	if err != nil || f.Path != "Projets/Box" {
		t.Fatalf("restore: %+v, %v", f, err)
	}
	for p, want := range map[string]string{"Projets/Box/plan.txt": "plan", "Projets/Box/schema/v1.png": "png"} {
		if b, _ := os.ReadFile(s.diskPath("enzo", p)); string(b) != want {
			t.Errorf("%s = %q", p, b)
		}
	}
	if items, _ := s.ListTrash(ctx, "enzo"); len(items) != 0 {
		t.Errorf("trash not empty after restore: %+v", items)
	}
	if _, err := os.Stat(s.trashDir("enzo", item.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("empty trash folder left: %v", err)
	}
	if journal(t, s) != 0 {
		t.Error("journal not cleared")
	}
	assertClean(t, s)
}

func TestRestoreWhenThePathIsTakenOrTheFolderIsGone(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "Docs/rapport.pdf", "v1")
	first, err := s.Trash(ctx, "enzo", "Docs/rapport.pdf")
	if err != nil {
		t.Fatal(err)
	}
	// The name is reused, then trashed again: two items with one original path.
	put(t, s, "enzo", "Docs/rapport.pdf", "v2")
	second, err := s.Trash(ctx, "enzo", "Docs/rapport.pdf")
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, "enzo", "Docs/rapport.pdf", "v3")

	f1, err := s.Restore(ctx, "enzo", first.ID)
	if err != nil || f1.Path != "Docs/rapport (restauré).pdf" {
		t.Fatalf("first restore: %+v, %v", f1, err)
	}
	f2, err := s.Restore(ctx, "enzo", second.ID)
	if err != nil || f2.Path != "Docs/rapport (restauré 2).pdf" {
		t.Fatalf("second restore: %+v, %v", f2, err)
	}
	for p, want := range map[string]string{"Docs/rapport.pdf": "v3", "Docs/rapport (restauré).pdf": "v1", "Docs/rapport (restauré 2).pdf": "v2"} {
		if b, _ := os.ReadFile(s.diskPath("enzo", p)); string(b) != want {
			t.Errorf("%s = %q, want %q", p, b, want)
		}
	}

	// The folder of the item is gone: it is recreated.
	put(t, s, "enzo", "Vieux/Sous/note.txt", "note")
	note, _ := s.Trash(ctx, "enzo", "Vieux/Sous/note.txt")
	if _, err := s.Trash(ctx, "enzo", "Vieux"); err != nil {
		t.Fatal(err)
	}
	if f, err := s.Restore(ctx, "enzo", note.ID); err != nil || f.Path != "Vieux/Sous/note.txt" {
		t.Errorf("restore into a gone folder: %+v, %v", f, err)
	}

	// The folder became a file: the item comes back at the root.
	put(t, s, "enzo", "Bureau/photo.jpg", "jpg")
	photo, _ := s.Trash(ctx, "enzo", "Bureau/photo.jpg")
	if _, err := s.Trash(ctx, "enzo", "Bureau"); err != nil {
		t.Fatal(err)
	}
	put(t, s, "enzo", "Bureau", "now a file")
	if f, err := s.Restore(ctx, "enzo", photo.ID); err != nil || f.Path != "photo.jpg" {
		t.Errorf("restore under a file: %+v, %v", f, err)
	}
	assertClean(t, s)
}

func TestMovesIgnoreTrashedRows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "Docs/a.txt", "old")
	old, _ := s.Trash(ctx, "enzo", "Docs/a.txt")
	put(t, s, "enzo", "Docs/a.txt", "new")

	if _, err := s.Move(ctx, "enzo", "Docs", "Papiers"); err != nil {
		t.Fatal(err)
	}
	// The trashed row keeps its original path; restoring brings it back there.
	if f, err := s.Restore(ctx, "enzo", old.ID); err != nil || f.Path != "Docs/a.txt" {
		t.Errorf("restore after the folder moved: %+v, %v", f, err)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "Papiers/a.txt")); string(b) != "new" {
		t.Errorf("moved file = %q", b)
	}
	assertClean(t, s)
}

func TestRecoverTrashAndRestoreAfterACrash(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "fini.txt", "1")
	put(t, s, "enzo", "annule.txt", "2")
	intend := func(p string) int64 {
		t.Helper()
		var id int64
		if err := s.Pool.QueryRow(ctx, `INSERT INTO trash (owner, path, kind, size, expires_at) VALUES ('enzo', $1, 'file', 1, now() + interval '1 day') RETURNING id`, p).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op, trash_id) VALUES ('enzo', $1, '', 'trash', $2)`, p, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Crash after the rename into the trash, then crash before it.
	done := intend("fini.txt")
	if err := os.MkdirAll(s.trashDir("enzo", done), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.diskPath("enzo", "fini.txt"), s.trashDisk("enzo", done, "fini.txt")); err != nil {
		t.Fatal(err)
	}
	intend("annule.txt")

	finished, dropped, err := s.RecoverMoves(ctx)
	if err != nil || finished != 1 || dropped != 1 {
		t.Fatalf("recover: %d finished, %d dropped, %v", finished, dropped, err)
	}
	items, _ := s.ListTrash(ctx, "enzo")
	if len(items) != 1 || items[0].Path != "fini.txt" {
		t.Errorf("trash after recovery: %+v", items)
	}
	if _, err := s.Lookup(ctx, "enzo", "annule.txt"); err != nil {
		t.Errorf("a trash that never reached the disk changed the drive: %v", err)
	}
	assertClean(t, s)

	// A restore cut after its rename is finished too.
	if _, err := s.Pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op, trash_id) VALUES ('enzo', 'fini.txt', 'fini.txt', 'restore', $1)`, done); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.trashDisk("enzo", done, "fini.txt"), s.diskPath("enzo", "fini.txt")); err != nil {
		t.Fatal(err)
	}
	if finished, _, err := s.RecoverMoves(ctx); err != nil || finished != 1 {
		t.Fatalf("recover restore: %d, %v", finished, err)
	}
	if _, err := s.Lookup(ctx, "enzo", "fini.txt"); err != nil {
		t.Errorf("restored file not indexed: %v", err)
	}
	assertClean(t, s)
}

func TestPurge(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "a.txt", "a")
	put(t, s, "enzo", "b/c.txt", "c")
	put(t, s, "johan", "j.txt", "j")
	a, _ := s.Trash(ctx, "enzo", "a.txt")
	b, _ := s.Trash(ctx, "enzo", "b")
	j, _ := s.Trash(ctx, "johan", "j.txt")

	if err := s.Purge(ctx, "johan", a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("johan purges enzo's item: %v", err)
	}
	if err := s.Purge(ctx, "enzo", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.trashDir("enzo", a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("purged bytes still there: %v", err)
	}

	// Expiry: b is due, johan's item is not.
	if _, err := s.Pool.Exec(ctx, `UPDATE trash SET expires_at = now() - interval '1 second' WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PurgeExpired(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v", n, err)
	}
	if items, _ := s.ListTrash(ctx, "enzo"); len(items) != 0 {
		t.Errorf("enzo's trash = %+v", items)
	}
	if items, _ := s.ListTrash(ctx, "johan"); len(items) != 1 || items[0].ID != j.ID {
		t.Errorf("johan's trash = %+v", items)
	}
	var rows int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE owner = 'enzo'`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("enzo's purged rows: %d, %v", rows, err)
	}
	assertClean(t, s)

	if n, err := s.EmptyTrash(ctx, "johan"); err != nil || n != 1 {
		t.Errorf("EmptyTrash = %d, %v", n, err)
	}
}

func TestRemoveTrashDirStaysInsideTheTrash(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	victim := filepath.Join(s.Root, "etc", "1")
	if err := os.MkdirAll(victim, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		owner string
		id    int64
	}{{"../../etc", 1}, {"enzo/../..", 1}, {"", 1}, {"enzo", 0}, {"enzo", -3}} {
		if err := s.removeTrashDir(c.owner, c.id); err == nil {
			t.Errorf("removeTrashDir(%q, %d) accepted a folder outside the trash", c.owner, c.id)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a folder outside the trash was erased: %v", err)
	}
}
