package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// cutReader yields its data, then fails: a connection lost mid-chunk.
type cutReader struct {
	data string
	sent bool
}

func (r *cutReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(p, r.data), nil
}

func TestUploadInChunksWithAnInterruption(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	content := "0123456789abcdefghij" // 20 bytes
	sum := sha256.Sum256([]byte(content))
	mod := time.Date(2025, 8, 1, 10, 0, 0, 0, time.UTC)

	u, err := s.CreateUpload(ctx, "enzo", "Vidéos/plage.mp4", int64(len(content)), sum[:], &mod, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if u.ExpiresAt.Before(time.Now().Add(23 * time.Hour)) {
		t.Errorf("expires at %v", u.ExpiresAt)
	}

	// First chunk arrives, then the connection drops 3 bytes into the second.
	if u, _, err = s.AppendUpload(ctx, "enzo", u.ID, 0, strings.NewReader(content[:8])); err != nil || u.Offset != 8 {
		t.Fatalf("chunk 1: offset %d, %v", u.Offset, err)
	}
	if u, _, err = s.AppendUpload(ctx, "enzo", u.ID, 8, &cutReader{data: content[8:11]}); err == nil || u.Offset != 11 {
		t.Fatalf("cut chunk: offset %d, %v (want 11 and an error)", u.Offset, err)
	}

	// The client asks where to resume, and sends the rest.
	got, err := s.GetUpload(ctx, "enzo", u.ID)
	if err != nil || got.Offset != 11 {
		t.Fatalf("resume at %d, %v", got.Offset, err)
	}
	_, f, err := s.AppendUpload(ctx, "enzo", u.ID, 11, strings.NewReader(content[11:]))
	if err != nil || f == nil {
		t.Fatalf("last chunk: %+v, %v", f, err)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "Vidéos/plage.mp4")); string(b) != content {
		t.Errorf("stored %q", b)
	}
	if !f.ModifiedAt.Equal(mod) {
		t.Errorf("modified_at %v, want %v", f.ModifiedAt, mod)
	}
	if _, err := s.GetUpload(ctx, "enzo", u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("upload still there after finishing: %v", err)
	}
	if entries, _ := os.ReadDir(s.uploadsDir()); len(entries) != 0 {
		t.Errorf("partial files left: %d", len(entries))
	}
	assertClean(t, s)
}

func TestUploadRefusals(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "pris.txt", "already here")

	if _, err := s.CreateUpload(ctx, "enzo", "pris.txt", 3, nil, nil, 0, false); !errors.Is(err, ErrExists) {
		t.Errorf("name taken: %v", err)
	}
	if _, err := s.CreateUpload(ctx, "enzo", "gros.bin", 101, nil, nil, 100, false); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the limit: %v", err)
	}
	if _, err := s.CreateUpload(ctx, "enzo", "disque.bin", 1<<62, nil, nil, 0, false); !errors.Is(err, ErrTooLarge) {
		t.Errorf("larger than the disk: %v", err)
	}
	if _, err := s.CreateUpload(ctx, "enzo", "../x", 3, nil, nil, 0, false); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("bad path: %v", err)
	}

	u, err := s.CreateUpload(ctx, "enzo", "a.bin", 5, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AppendUpload(ctx, "enzo", u.ID, 2, strings.NewReader("abc")); !errors.Is(err, ErrOffset) {
		t.Errorf("wrong offset: %v", err)
	}
	if _, _, err := s.AppendUpload(ctx, "enzo", u.ID, 0, strings.NewReader("abcdefgh")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("chunk past the length: %v", err)
	}
	if got, _ := s.GetUpload(ctx, "enzo", u.ID); got.Offset != 0 {
		t.Errorf("a refused chunk left %d bytes", got.Offset)
	}
	// Another account cannot see, write or delete it.
	if _, err := s.GetUpload(ctx, "johan", u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("johan sees enzo's upload: %v", err)
	}
	if _, _, err := s.AppendUpload(ctx, "johan", u.ID, 0, strings.NewReader("ab")); !errors.Is(err, ErrNotFound) {
		t.Errorf("johan writes enzo's upload: %v", err)
	}
	if err := s.DeleteUpload(ctx, "johan", u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("johan deletes enzo's upload: %v", err)
	}
	if err := s.DeleteUpload(ctx, "enzo", u.ID); err != nil {
		t.Errorf("enzo abandons: %v", err)
	}
	if _, err := os.Stat(s.uploadFile(u.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial file left after abandon: %v", err)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "pris.txt")); string(b) != "already here" {
		t.Errorf("stored file touched: %q", b)
	}
}

func TestUploadWrongHashOrTakenNameEndsTheUpload(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	wrong := sha256.Sum256([]byte("not this"))
	u, err := s.CreateUpload(ctx, "enzo", "faux.bin", 3, wrong[:], nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AppendUpload(ctx, "enzo", u.ID, 0, strings.NewReader("abc")); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong hash: %v", err)
	}
	if _, err := os.Stat(s.diskPath("enzo", "faux.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file with a wrong hash was stored: %v", err)
	}

	// The name gets taken while the upload runs: the original is kept.
	u, err = s.CreateUpload(ctx, "enzo", "course.txt", 3, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, "enzo", "course.txt", "first")
	if _, _, err := s.AppendUpload(ctx, "enzo", u.ID, 0, strings.NewReader("abc")); !errors.Is(err, ErrExists) {
		t.Errorf("name taken at the end: %v", err)
	}
	if b, _ := os.ReadFile(s.diskPath("enzo", "course.txt")); string(b) != "first" {
		t.Errorf("course.txt overwritten: %q", b)
	}
	if entries, _ := os.ReadDir(s.uploadsDir()); len(entries) != 0 {
		t.Errorf("partial files left: %d", len(entries))
	}
}

func TestConcurrentChunksOfOneUpload(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u, err := s.CreateUpload(ctx, "enzo", "a.bin", 6, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	// Hold the upload's lock, as a long chunk would: a second writer is refused.
	mu, _ := uploadLocks.Load(u.ID)
	if mu == nil {
		mu, _ = uploadLocks.LoadOrStore(u.ID, &sync.Mutex{})
	}
	mu.(*sync.Mutex).Lock()
	_, _, err = s.AppendUpload(ctx, "enzo", u.ID, 0, strings.NewReader("abc"))
	mu.(*sync.Mutex).Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Errorf("second writer: %v", err)
	}
	if _, _, err := s.AppendUpload(ctx, "enzo", u.ID, 0, strings.NewReader("abc")); err != nil {
		t.Errorf("after the first writer: %v", err)
	}
}

func TestEmptyUploadAndCleanUploads(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	empty, err := s.CreateUpload(ctx, "enzo", "vide.txt", 0, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := s.FinishEmptyUpload(ctx, empty); err != nil || f.Size != 0 {
		t.Fatalf("empty upload: %+v, %v", f, err)
	}

	stale, err := s.CreateUpload(ctx, "enzo", "abandon.bin", 10, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.CreateUpload(ctx, "enzo", "en-cours.bin", 10, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE uploads SET expires_at = now() - interval '1 second' WHERE id = $1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.uploadFile("ffffffffffffffffffffffffffffffff"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := s.CleanUploads(ctx)
	if err != nil || n != 2 {
		t.Fatalf("CleanUploads = %d, %v; want the expired upload and the orphan", n, err)
	}
	if _, err := s.GetUpload(ctx, "enzo", live.ID); err != nil {
		t.Errorf("live upload removed: %v", err)
	}
	if _, err := os.Stat(s.uploadFile(stale.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expired partial file kept: %v", err)
	}
	assertClean(t, s)
}
