package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// rescued lists what .cloudos/rescue/<owner> holds.
func rescued(t *testing.T, s *Store, owner string) []string {
	t.Helper()
	entries, err := os.ReadDir(s.rescueOwnerDir(owner))
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
	sort.Strings(out)
	return out
}

// staged starts a new SMB write of content at owner/p.
func staged(t *testing.T, s *Store, owner, p, content string) *Staged {
	t.Helper()
	st, err := s.Stage(context.Background(), owner, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteAt([]byte(content), 0); err != nil {
		t.Fatal(err)
	}
	return st
}

// onlyTrashItem returns the owner's one trash item, checking it is a kept SMB write.
func onlyTrashItem(t *testing.T, s *Store, owner, p string, size int) TrashItem {
	t.Helper()
	items, err := s.ListTrash(context.Background(), owner)
	if err != nil || len(items) != 1 {
		t.Fatalf("trash: %+v, %v; want one item", items, err)
	}
	it := items[0]
	if it.Path != p || it.Kind != KindFile || it.Size != int64(size) || it.Reason != RescueReason {
		t.Fatalf("trash item %+v, want %s (%d bytes, %s)", it, p, size, RescueReason)
	}
	if left := time.Until(it.ExpiresAt); left < DefaultTrashTTL-time.Hour || left > DefaultTrashTTL {
		t.Errorf("expires in %v", left)
	}
	return it
}

// The database goes away before the CLOSE: the content waits in the rescue
// folder, not in the tree and not among the working files. Once the box
// restarts with the database, it is in the trash at its path, with its
// reason, and restores with its bytes and date.
func TestCommitKeepsWhenTheDatabaseIsDown(t *testing.T) {
	pool, _ := testdb.Migrated(t)
	url := pool.Config().ConnString()
	s := &Store{Root: t.TempDir(), Pool: pool}
	ctx := context.Background()
	const content = "le rapport du trimestre, écrit en SMB"

	st := staged(t, s, "enzo", "Docs/rapport.txt", content)
	pool.Close()
	mod := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	opt := NoExpectations()
	opt.ModTime = mod
	_, err := st.Commit(opt)
	var kept *KeptError
	if !errors.Is(err, ErrKept) || !errors.As(err, &kept) || kept.TrashID != 0 || kept.ID == "" {
		t.Fatalf("Commit with the database down = %v, want a KeptError waiting for the database", err)
	}
	if _, err := os.Lstat(s.diskPath("enzo", "Docs/rapport.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the content is in the tree: %v", err)
	}
	if left := stagedFiles(t, s); len(left) != 0 {
		t.Errorf("working files left: %v", left)
	}
	if got := rescued(t, s, "enzo"); strings.Join(got, " ") != kept.ID+" "+kept.ID+".json" {
		t.Fatalf("rescue folder holds %v", got)
	}
	if b, err := os.ReadFile(s.rescueContent("enzo", kept.ID)); err != nil || string(b) != content {
		t.Fatalf("rescued content %q, %v", b, err)
	}
	if n, err := s.RescuedCount(); n != 1 || err != nil {
		t.Errorf("RescuedCount = %d, %v", n, err)
	}

	// The box starts again: a new store, a new pool.
	pool2, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool2.Close)
	s2 := &Store{Root: s.Root, Pool: pool2}
	if n, err := s2.SettleRescued(ctx); n != 1 || err != nil {
		t.Fatalf("SettleRescued = %d, %v", n, err)
	}
	it := onlyTrashItem(t, s2, "enzo", "Docs/rapport.txt", len(content))
	if got := rescued(t, s2, "enzo"); len(got) != 0 {
		t.Errorf("rescue folder still holds %v", got)
	}
	if n, err := s2.SettleRescued(ctx); n != 0 || err != nil {
		t.Errorf("SettleRescued again = %d, %v", n, err)
	}
	if journal(t, s2) != 0 {
		t.Error("journal not cleared")
	}
	assertClean(t, s2)

	f, err := s2.Restore(ctx, "enzo", it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != "Docs/rapport.txt" || !bytes.Equal(f.SHA256, sumOf(content)) || !f.ModifiedAt.Equal(mod) ||
		diskContent(t, s2, "enzo", "Docs/rapport.txt") != content {
		t.Errorf("restored %+v", f)
	}
	assertClean(t, s2)
}

// The name is taken when the CLOSE comes (a folder made meanwhile, or a file
// in another case): the database answers, so the content is in the trash at
// once, and restores beside what took its name.
func TestCommitKeepsWhenTheNameIsTaken(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		what, path, taken, restored string
		take                        func(s *Store) error
	}{
		{"a folder", "a/b.txt", "a/b.txt", "a/b (restauré).txt", func(s *Store) error {
			_, err := s.Mkdir(ctx, "enzo", "a/b.txt")
			return err
		}},
		{"another case", "notes.txt", "NOTES.txt", "notes (restauré).txt", func(s *Store) error {
			_, err := s.Put(ctx, "enzo", "NOTES.txt", strings.NewReader("l'autre"), NoExpectations())
			return err
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			s := newStore(t)
			st := staged(t, s, "enzo", tc.path, "contenu reçu")
			if err := tc.take(s); err != nil {
				t.Fatal(err)
			}
			_, err := st.Commit(NoExpectations())
			var kept *KeptError
			if !errors.Is(err, ErrExists) || !errors.As(err, &kept) || kept.TrashID == 0 {
				t.Fatalf("Commit onto a taken name = %v, want a KeptError in the trash wrapping ErrExists", err)
			}
			it := onlyTrashItem(t, s, "enzo", tc.path, len("contenu reçu"))
			if it.ID != kept.TrashID {
				t.Errorf("trash item %d, the error says %d", it.ID, kept.TrashID)
			}
			if got := rescued(t, s, "enzo"); len(got) != 0 {
				t.Errorf("rescue folder still holds %v", got)
			}
			if left := stagedFiles(t, s); len(left) != 0 {
				t.Errorf("working files left: %v", left)
			}
			if _, err := s.Lookup(ctx, "enzo", tc.taken); err != nil {
				t.Errorf("what took the name: %v", err)
			}
			assertClean(t, s)
			f, err := s.Restore(ctx, "enzo", it.ID)
			if err != nil {
				t.Fatal(err)
			}
			if f.Path != tc.restored || diskContent(t, s, "enzo", tc.restored) != "contenu reçu" {
				t.Errorf("restored at %s", f.Path)
			}
			assertClean(t, s)
		})
	}
}

// keepAside rescues content for owner/p as a failed Commit would.
func keepAside(t *testing.T, s *Store, owner, p, content string) string {
	t.Helper()
	if err := os.MkdirAll(s.stageDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(s.stageDir(), "stage-test")
	if err := os.WriteFile(work, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	id, err := s.keepStaged(owner, p, work, sumOf(content), int64(len(content)), time.Now(), errors.New("the database is down"))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// intendRescue journals the rescue of id as settleRescue does before it
// touches the disk, and returns the trash item.
func intendRescue(t *testing.T, s *Store, owner, id, p, content string) int64 {
	t.Helper()
	ctx := context.Background()
	var trashID int64
	if err := s.Pool.QueryRow(ctx, `INSERT INTO trash (owner, path, kind, size, expires_at, reason)
		VALUES ($1, $2, 'file', $3, now() + interval '30 days', 'smb_not_saved') RETURNING id`, owner, p, len(content)).Scan(&trashID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO files (owner, path, kind, size, mime, sha256, modified_at, trash_id)
		VALUES ($1, $2, 'file', $3, 'text/plain', $4, now(), $5)`, owner, p, len(content), sumOf(content), trashID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO moves (owner, from_path, to_path, op, trash_id) VALUES ($1, $2, $3, 'rescue', $4)`,
		owner, id, p, trashID); err != nil {
		t.Fatal(err)
	}
	return trashID
}

func trashCount(t *testing.T, s *Store) (items, rows int) {
	t.Helper()
	if err := s.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM trash), (SELECT count(*) FROM files)`).Scan(&items, &rows); err != nil {
		t.Fatal(err)
	}
	return items, rows
}

// A stop in the middle of a rescue: before the rename, the rows go and the
// next SettleRescued starts again, from RecoverMoves (the core) or from
// SettleRescued itself (core smb, which runs no RecoverMoves); after it, the
// journal is finished and the note goes.
func TestRecoverARescueAfterACrash(t *testing.T) {
	ctx := context.Background()

	t.Run("content still rescued, then the core starts", func(t *testing.T) {
		s := newStore(t)
		id := keepAside(t, s, "enzo", "Docs/a.txt", "aaa")
		intendRescue(t, s, "enzo", id, "Docs/a.txt", "aaa")
		if _, dropped, err := s.RecoverMoves(ctx); dropped != 1 || err != nil {
			t.Fatalf("RecoverMoves dropped %d, %v", dropped, err)
		}
		if items, rows := trashCount(t, s); items != 0 || rows != 0 || journal(t, s) != 0 {
			t.Fatalf("after the drop: %d trash items, %d rows, %d journal", items, rows, journal(t, s))
		}
		if got := rescued(t, s, "enzo"); len(got) != 2 {
			t.Fatalf("the rescue is gone: %v", got)
		}
		if n, err := s.SettleRescued(ctx); n != 1 || err != nil {
			t.Fatalf("SettleRescued = %d, %v", n, err)
		}
		onlyTrashItem(t, s, "enzo", "Docs/a.txt", 3)
		assertClean(t, s)
	})

	t.Run("content still rescued, then core smb starts", func(t *testing.T) {
		s := newStore(t)
		id := keepAside(t, s, "enzo", "Docs/a.txt", "aaa")
		intendRescue(t, s, "enzo", id, "Docs/a.txt", "aaa")
		if n, err := s.SettleRescued(ctx); n != 1 || err != nil {
			t.Fatalf("SettleRescued = %d, %v", n, err)
		}
		onlyTrashItem(t, s, "enzo", "Docs/a.txt", 3)
		if got := rescued(t, s, "enzo"); len(got) != 0 || journal(t, s) != 0 {
			t.Errorf("left: %v, %d journal", got, journal(t, s))
		}
		assertClean(t, s)
	})

	t.Run("content in the trash", func(t *testing.T) {
		s := newStore(t)
		id := keepAside(t, s, "enzo", "Docs/a.txt", "aaa")
		trashID := intendRescue(t, s, "enzo", id, "Docs/a.txt", "aaa")
		if err := os.MkdirAll(s.trashDir("enzo", trashID), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(s.rescueContent("enzo", id), s.trashDisk("enzo", trashID, "Docs/a.txt")); err != nil {
			t.Fatal(err)
		}
		if finished, _, err := s.RecoverMoves(ctx); finished != 1 || err != nil {
			t.Fatalf("RecoverMoves finished %d, %v", finished, err)
		}
		onlyTrashItem(t, s, "enzo", "Docs/a.txt", 3)
		if got := rescued(t, s, "enzo"); len(got) != 0 || journal(t, s) != 0 {
			t.Errorf("left: %v, %d journal", got, journal(t, s))
		}
		assertClean(t, s)
	})
}

// What SettleRescued cannot trust stays where it is, except a note whose
// content never reached the rescue folder, or a note half written, which go.
func TestSettleRescuedLeavesWhatItCannotTrust(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	changed := keepAside(t, s, "enzo", "a.txt", "aaa")
	if err := os.WriteFile(s.rescueContent("enzo", changed), []byte("bbb!"), 0o640); err != nil {
		t.Fatal(err)
	}
	orphan := strings.Repeat("ab", 16)
	if err := os.WriteFile(s.rescueNotePath("enzo", orphan), []byte(`{"owner":"enzo","path":"b.txt","size":1}`), 0o640); err != nil {
		t.Fatal(err)
	}
	unfinished := strings.Repeat("cd", 16)
	if err := os.WriteFile(s.rescueNotePath("enzo", unfinished)+".tmp", []byte(`{"owner":`), 0o640); err != nil {
		t.Fatal(err)
	}
	if n, err := s.SettleRescued(ctx); n != 0 || err != nil {
		t.Fatalf("SettleRescued = %d, %v", n, err)
	}
	if got := rescued(t, s, "enzo"); strings.Join(got, " ") != changed+" "+changed+".json" {
		t.Errorf("rescue folder holds %v", got)
	}
	if items, rows := trashCount(t, s); items != 0 || rows != 0 {
		t.Errorf("%d trash items, %d rows", items, rows)
	}
	if b, _ := os.ReadFile(s.rescueContent("enzo", changed)); string(b) != "bbb!" {
		t.Error("the content that differs was touched")
	}
}

// Neither CleanTmp (the core's start) nor CleanStaged (core smb's start)
// touches the rescue folder.
func TestCleanTmpAndCleanStagedLeaveTheRescue(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	writeTree(t, s.tmpDir(), map[string]string{"put-1": "partial", "smb/stage-1": "being written"})
	id := keepAside(t, s, "enzo", "a.txt", "gardé")
	if _, err := s.CleanTmp(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CleanStaged(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(s.rescueContent("enzo", id)); err != nil || string(b) != "gardé" {
		t.Fatalf("the rescued content: %q, %v", b, err)
	}
	note, err := os.ReadFile(s.rescueNotePath("enzo", id))
	if err != nil || !strings.Contains(string(note), `"path": "a.txt"`) || !strings.Contains(string(note), hex.EncodeToString(sumOf("gardé"))) {
		t.Fatalf("the note: %s, %v", note, err)
	}
	if n, err := s.RescuedCount(); n != 1 || err != nil {
		t.Errorf("RescuedCount = %d, %v", n, err)
	}
}
