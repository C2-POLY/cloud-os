package albums

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

func newService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (name, display_name, password_hash, role) VALUES
		('enzo', 'Enzo', 'x', 'admin'), ('johan', 'Johan', 'x', 'member')`); err != nil {
		t.Fatal(err)
	}
	s := &Service{Pool: pool, Files: &storage.Store{Root: t.TempDir(), Pool: pool}}
	for _, p := range []string{"Photos/plage.jpg", "Photos/phare.jpg", "Photos/port.jpg"} {
		put(t, s, "enzo", p)
	}
	put(t, s, "johan", "Photos/secret.jpg")
	return s, ctx
}

func put(t *testing.T, s *Service, owner, p string) storage.File {
	t.Helper()
	f, err := s.Files.Put(context.Background(), owner, p, strings.NewReader("image "+owner+p), storage.NoExpectations())
	if err != nil {
		t.Fatalf("put %s: %v", p, err)
	}
	return f
}

func paths(t *testing.T, s *Service, owner string, id int64) []string {
	t.Helper()
	items, err := s.Items(context.Background(), owner, "UTC", id, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, it := range items {
		out = append(out, it.Path)
	}
	return out
}

func TestCreateRenameDeleteNeverTouchTheFiles(t *testing.T) {
	s, ctx := newService(t)
	a, err := s.Create(ctx, "enzo", "  Bretagne 2024 ")
	if err != nil || a.Name != "Bretagne 2024" {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if err := s.Add(ctx, "enzo", a.ID, []string{"Photos/plage.jpg", "Photos/phare.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(ctx, "enzo", a.ID, "Bretagne"); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(ctx, "enzo")
	if err != nil || len(all) != 1 || all[0].Name != "Bretagne" || all[0].Count != 2 || all[0].Cover == nil {
		t.Fatalf("list = %+v, %v", all, err)
	}
	if err := s.Delete(ctx, "enzo", a.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Photos/plage.jpg", "Photos/phare.jpg"} {
		if _, err := s.Files.Lookup(ctx, "enzo", p); err != nil {
			t.Errorf("%s after deleting its album: %v", p, err)
		}
	}
	if _, err := s.Get(ctx, "enzo", a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted album: %v", err)
	}
}

func TestNamesAreUniquePerAccountAndChecked(t *testing.T) {
	s, ctx := newService(t)
	if _, err := s.Create(ctx, "enzo", "Noël"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "enzo", "NOËL"); !errors.Is(err, ErrExists) {
		t.Errorf("same name, other case: %v, want ErrExists", err)
	}
	if _, err := s.Create(ctx, "johan", "Noël"); err != nil {
		t.Errorf("same name in another account: %v", err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("é", MaxNameLen+1), "a\nb"} {
		if _, err := s.Create(ctx, "enzo", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("create %q: %v, want ErrInvalid", bad, err)
		}
	}
}

func TestAddIsIdempotentAndRemoveKeepsTheFile(t *testing.T) {
	s, ctx := newService(t)
	a, _ := s.Create(ctx, "enzo", "Plage")
	for range 2 {
		if err := s.Add(ctx, "enzo", a.ID, []string{"Photos/plage.jpg"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := paths(t, s, "enzo", a.ID); len(got) != 1 {
		t.Fatalf("items = %v, want one", got)
	}
	if err := s.Remove(ctx, "enzo", a.ID, []string{"Photos/plage.jpg"}); err != nil {
		t.Fatal(err)
	}
	if got := paths(t, s, "enzo", a.ID); len(got) != 0 {
		t.Errorf("items after remove = %v", got)
	}
	if _, err := s.Files.Lookup(ctx, "enzo", "Photos/plage.jpg"); err != nil {
		t.Errorf("file after remove: %v", err)
	}
	if err := s.Add(ctx, "enzo", a.ID, []string{"Photos"}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("add a folder: %v, want ErrNotFound", err)
	}
	if err := s.Add(ctx, "enzo", a.ID, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("add nothing: %v, want ErrInvalid", err)
	}
}

func TestAPhotoFollowsItsMoveTrashAndRestore(t *testing.T) {
	s, ctx := newService(t)
	a, _ := s.Create(ctx, "enzo", "Voyage")
	if err := s.Add(ctx, "enzo", a.ID, []string{"Photos/plage.jpg", "Photos/phare.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Files.Move(ctx, "enzo", "Photos/plage.jpg", "Photos/plage-ete.jpg"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths(t, s, "enzo", a.ID), " "); !strings.Contains(got, "Photos/plage-ete.jpg") {
		t.Errorf("after move: %s", got)
	}
	item, err := s.Files.Trash(ctx, "enzo", "Photos/phare.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(t, s, "enzo", a.ID); len(got) != 1 {
		t.Errorf("with a photo in the trash: %v, want only the other one", got)
	}
	if all, _ := s.List(ctx, "enzo"); all[0].Count != 1 {
		t.Errorf("count with a photo in the trash = %d, want 1", all[0].Count)
	}
	if _, err := s.Files.Restore(ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	if got := paths(t, s, "enzo", a.ID); len(got) != 2 {
		t.Errorf("after restore: %v, want both", got)
	}
	item, _ = s.Files.Trash(ctx, "enzo", "Photos/phare.jpg")
	if err := s.Files.Purge(ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	var refs int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM album_items WHERE album_id = $1`, a.ID).Scan(&refs); err != nil || refs != 1 {
		t.Errorf("references after purge = %d, %v, want 1", refs, err)
	}
}

func TestAnAccountReachesOnlyItsOwnAlbumsAndFiles(t *testing.T) {
	s, ctx := newService(t)
	mine, _ := s.Create(ctx, "enzo", "À moi")
	theirs, _ := s.Create(ctx, "johan", "À Johan")
	if err := s.Add(ctx, "enzo", mine.ID, []string{"Photos/secret.jpg"}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("add another account's file: %v, want ErrNotFound", err)
	}
	if err := s.Add(ctx, "enzo", theirs.ID, []string{"Photos/plage.jpg"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("add to another account's album: %v, want ErrNotFound", err)
	}
	if _, err := s.Items(ctx, "enzo", "UTC", theirs.ID, nil, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("read another account's album: %v", err)
	}
	if err := s.Rename(ctx, "enzo", theirs.ID, "pris"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename another account's album: %v", err)
	}
	if err := s.Delete(ctx, "enzo", theirs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete another account's album: %v", err)
	}
	if all, _ := s.List(ctx, "johan"); len(all) != 1 {
		t.Errorf("johan's albums after enzo's attempts: %+v", all)
	}
}
