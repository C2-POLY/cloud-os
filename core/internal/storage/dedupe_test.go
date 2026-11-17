package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"
)

func sumOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func diskContent(t *testing.T, s *Store, owner, p string) string {
	t.Helper()
	b, err := os.ReadFile(s.diskPath(owner, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDedupeCopiesAContentTheOwnerHas(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "Photos/plage.jpg", "plage")
	when := time.Date(2024, 7, 14, 18, 3, 22, 0, time.UTC)

	f, res, err := s.Dedupe(ctx, "enzo", "Phone/IMG_0001.jpg", sumOf("plage"), when)
	if err != nil || res != DedupeCopied {
		t.Fatalf("dedupe: %v %v", res, err)
	}
	if !bytes.Equal(f.SHA256, sumOf("plage")) || !f.ModifiedAt.Equal(when) || diskContent(t, s, "enzo", "Phone/IMG_0001.jpg") != "plage" {
		t.Errorf("copy: %+v", f)
	}
	// The original is untouched, and both are independent files.
	if diskContent(t, s, "enzo", "Photos/plage.jpg") != "plage" {
		t.Error("original changed")
	}
	a, _ := os.Stat(s.diskPath("enzo", "Photos/plage.jpg"))
	b, _ := os.Stat(s.diskPath("enzo", "Phone/IMG_0001.jpg"))
	if os.SameFile(a, b) {
		t.Error("a hard link, not a copy")
	}

	// Again at the same path: already there, nothing done.
	if again, res, err := s.Dedupe(ctx, "enzo", "Phone/IMG_0001.jpg", sumOf("plage"), when); err != nil || res != DedupePresent || again.ID != f.ID {
		t.Errorf("again: %v %v %+v", res, err, again)
	}
	// The path holds another content: refused.
	if _, _, err := s.Dedupe(ctx, "enzo", "Photos/plage.jpg", sumOf("autre"), when); !errors.Is(err, ErrExists) {
		t.Errorf("taken path: %v", err)
	}
}

func TestDedupeOnlyLooksAtTheOwnersLiveFiles(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "johan", "secret.jpg", "secret")
	put(t, s, "enzo", "jeté.jpg", "jeté")
	if _, err := s.Trash(ctx, "enzo", "jeté.jpg"); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"secret", "jeté", "jamais vu"} {
		if _, _, err := s.Dedupe(ctx, "enzo", "x.jpg", sumOf(content), time.Time{}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", content, err)
		}
	}
	known, err := s.Known(ctx, "enzo", [][]byte{sumOf("secret"), sumOf("jeté"), sumOf("jamais vu")})
	if err != nil || len(known) != 0 {
		t.Errorf("known: %d %v", len(known), err)
	}
	assertNothingLeft(t, s, "enzo", "x.jpg", 2) // johan's file and the trashed one
}

func TestDedupeSkipsASourceChangedOnDisk(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "a.jpg", "contenu")
	put(t, s, "enzo", "b.jpg", "contenu")
	// Someone rewrote a.jpg behind the product's back.
	if err := os.WriteFile(s.diskPath("enzo", "a.jpg"), []byte("abîmé!!"), 0o640); err != nil {
		t.Fatal(err)
	}
	f, res, err := s.Dedupe(ctx, "enzo", "c.jpg", sumOf("contenu"), time.Time{})
	if err != nil || res != DedupeCopied || diskContent(t, s, "enzo", "c.jpg") != "contenu" {
		t.Fatalf("from b: %v %v %+v", res, err, f)
	}
	// Both sources changed: nothing to copy from, nothing left behind.
	if err := os.WriteFile(s.diskPath("enzo", "b.jpg"), []byte("abîmé!!"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.diskPath("enzo", "c.jpg"), []byte("abîmé!!"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Dedupe(ctx, "enzo", "d.jpg", sumOf("contenu"), time.Time{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("all changed: %v", err)
	}
	if _, err := os.Stat(s.diskPath("enzo", "d.jpg")); !os.IsNotExist(err) {
		t.Errorf("d.jpg left on disk: %v", err)
	}
}

func TestKnownAnswersWhatTheOwnerHas(t *testing.T) {
	s := newStore(t)
	put(t, s, "enzo", "a.jpg", "a")
	put(t, s, "enzo", "copie de a.jpg", "a")
	put(t, s, "enzo", "b.jpg", "b")
	known, err := s.Known(context.Background(), "enzo", [][]byte{sumOf("a"), sumOf("b"), sumOf("c")})
	if err != nil || len(known) != 2 {
		t.Fatalf("known %d %v", len(known), err)
	}
	if _, err := s.Known(context.Background(), "enzo", make([][]byte, MaxKnown+1)); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("too many: %v", err)
	}
	if _, _, err := s.Dedupe(context.Background(), "enzo", "x", []byte("court"), time.Time{}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("bad hash: %v", err)
	}
}
