// Package storage writes user files under the data root and keeps their index.
//
// Files live in clear at <root>/<owner>/<path>, readable without the product.
// A write goes to a temporary file under <root>/.cloudos/tmp, is checked, then
// linked into place without ever replacing an existing file, inside the
// transaction that indexes it. Nothing in this package deletes or modifies a
// stored file.
package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrExists is returned when the path, or a name it needs, is already taken.
	ErrExists = errors.New("already exists")
	// ErrMismatch is returned when the written bytes differ from what was expected.
	ErrMismatch = errors.New("size or hash mismatch")
	// ErrNotFound is returned when no file is indexed at the path.
	ErrNotFound = errors.New("not found")
)

// Store is the file store: a data root on disk and its index in Postgres.
type Store struct {
	Root string
	Pool *pgxpool.Pool
	// TrashTTL is how long a trashed item is kept; zero means DefaultTrashTTL.
	TrashTTL time.Duration
}

// PutOptions are optional checks and metadata for Put.
type PutOptions struct {
	// ModTime is the file's modification time; zero means now.
	ModTime time.Time
	// ExpectSize, when not negative, must equal the number of bytes written.
	ExpectSize int64
	// ExpectSHA256, when set, must equal the hash of the bytes written.
	ExpectSHA256 []byte
}

// NoExpectations returns PutOptions that check nothing.
func NoExpectations() PutOptions { return PutOptions{ExpectSize: -1} }

func (s *Store) tmpDir() string { return filepath.Join(s.Root, ".cloudos", "tmp") }

func (s *Store) diskPath(owner, p string) string {
	return filepath.Join(s.Root, owner, filepath.FromSlash(p))
}

// Put stores the bytes of r at owner/p and indexes them. It returns ErrExists
// when the path or one of its folders is taken (case-insensitively), and
// ErrMismatch when an expected size or hash does not match. On any error,
// nothing is left on disk or in the index.
func (s *Store) Put(ctx context.Context, owner, p string, r io.Reader, opt PutOptions) (File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return File{}, err
	}
	p, err = CleanPath(p)
	if err != nil {
		return File{}, err
	}

	tmp, sum, size, sniff, err := s.writeTemp(r)
	if err != nil {
		return File{}, err
	}
	defer os.Remove(tmp) // no-op once linked and removed below
	return s.finish(ctx, owner, p, tmp, sum, size, sniff, opt)
}

// PlaceFile stores a file already written under the data root (an upload's
// bytes) at owner/p, with the same checks as Put. It reads the file again to
// hash what is really on disk. The source is left where it is: the caller
// removes it once PlaceFile succeeds.
func (s *Store) PlaceFile(ctx context.Context, owner, p, src string, opt PutOptions) (File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return File{}, err
	}
	p, err = CleanPath(p)
	if err != nil {
		return File{}, err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return File{}, err
	}
	if !info.Mode().IsRegular() {
		return File{}, fmt.Errorf("%s is not a regular file", src)
	}
	sum, sniff, err := hashFile(src)
	if err != nil {
		return File{}, err
	}
	return s.finish(ctx, owner, p, src, sum, info.Size(), sniff, opt)
}

// finish checks the expectations, sets the modification time and places the
// written file tmp at owner/p.
func (s *Store) finish(ctx context.Context, owner, p, tmp string, sum []byte, size int64, sniff []byte, opt PutOptions) (File, error) {
	if opt.ExpectSize >= 0 && size != opt.ExpectSize {
		return File{}, fmt.Errorf("%w: wrote %d bytes, expected %d", ErrMismatch, size, opt.ExpectSize)
	}
	if opt.ExpectSHA256 != nil && !bytes.Equal(sum, opt.ExpectSHA256) {
		return File{}, fmt.Errorf("%w: sha256 %x, expected %x", ErrMismatch, sum, opt.ExpectSHA256)
	}
	mod := opt.ModTime
	if mod.IsZero() {
		mod = time.Now()
	}
	if err := os.Chtimes(tmp, mod, mod); err != nil {
		return File{}, fmt.Errorf("set modification time: %w", err)
	}
	f := File{Owner: owner, Path: p, Kind: KindFile, Size: size, MIME: detectMIME(p, sniff), SHA256: sum, ModifiedAt: mod}
	return s.place(ctx, f, tmp)
}

// writeTemp copies r into a new temporary file, synced to disk, and returns
// its path, SHA-256, size and first bytes. The temporary file is removed on error.
func (s *Store) writeTemp(r io.Reader) (tmp string, sum []byte, size int64, sniff []byte, err error) {
	if err := os.MkdirAll(s.tmpDir(), 0o750); err != nil {
		return "", nil, 0, nil, fmt.Errorf("temporary folder: %w", err)
	}
	f, err := os.CreateTemp(s.tmpDir(), "put-*")
	if err != nil {
		return "", nil, 0, nil, fmt.Errorf("temporary file: %w", err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()

	h := sha256.New()
	head := &headBuffer{max: 512}
	size, err = io.Copy(io.MultiWriter(f, h, head), r)
	if err != nil {
		return "", nil, 0, nil, fmt.Errorf("write: %w", err)
	}
	if err = f.Sync(); err != nil {
		return "", nil, 0, nil, fmt.Errorf("sync: %w", err)
	}
	if err = f.Close(); err != nil {
		return "", nil, 0, nil, fmt.Errorf("close: %w", err)
	}
	return f.Name(), h.Sum(nil), size, head.buf, nil
}

// place indexes f and links tmp to its final path in one transaction. The link
// fails rather than replace a file already on disk. A crash after the link but
// before the commit leaves a complete, unindexed file, which Verify reports.
func (s *Store) place(ctx context.Context, f File, tmp string) (File, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit

	if err := lockOwner(ctx, tx, f.Owner); err != nil {
		return File{}, err
	}
	why, err := conflict(ctx, tx, f.Owner, f.Path, "")
	if err != nil {
		return File{}, err
	}
	if why != "" {
		return File{}, fmt.Errorf("%w: %s/%s: %s", ErrExists, f.Owner, f.Path, why)
	}
	if err := ensureDirs(ctx, tx, f.Owner, f.Path); err != nil {
		return File{}, err
	}
	out, err := insertFile(ctx, tx, f)
	if err != nil {
		return File{}, err
	}

	dst := s.diskPath(f.Owner, f.Path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return File{}, fmt.Errorf("folder: %w", err)
	}
	if err := os.Link(tmp, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return File{}, fmt.Errorf("%w: %s/%s is already on disk but not indexed", ErrExists, f.Owner, f.Path)
		}
		return File{}, fmt.Errorf("place file: %w", err)
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return File{}, fmt.Errorf("sync folder: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return File{}, fmt.Errorf("commit index (the file is on disk, unindexed): %w", err)
	}
	return out, nil
}

// adopt indexes a file already on disk at owner/p, unchanged, after checking
// its hash is want. It is how Import picks up a file left unindexed.
func (s *Store) adopt(ctx context.Context, owner, p string, want []byte) (File, error) {
	disk := s.diskPath(owner, p)
	info, err := os.Lstat(disk)
	if err != nil {
		return File{}, err
	}
	if !info.Mode().IsRegular() {
		return File{}, fmt.Errorf("%w: %s/%s is not a regular file", ErrExists, owner, p)
	}
	sum, sniff, err := hashFile(disk)
	if err != nil {
		return File{}, err
	}
	if !bytes.Equal(sum, want) {
		return File{}, fmt.Errorf("%w: %s/%s is on disk with other content", ErrExists, owner, p)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if err := lockOwner(ctx, tx, owner); err != nil {
		return File{}, err
	}
	why, err := conflict(ctx, tx, owner, p, "")
	if err != nil {
		return File{}, err
	}
	if why != "" {
		return File{}, fmt.Errorf("%w: %s/%s: %s", ErrExists, owner, p, why)
	}
	if err := ensureDirs(ctx, tx, owner, p); err != nil {
		return File{}, err
	}
	out, err := insertFile(ctx, tx, File{
		Owner: owner, Path: p, Kind: KindFile, Size: info.Size(), MIME: detectMIME(p, sniff), SHA256: sum, ModifiedAt: info.ModTime(),
	})
	if err != nil {
		return File{}, err
	}
	return out, tx.Commit(ctx)
}

// Open returns the indexed file at owner/p, opened for reading.
func (s *Store) Open(ctx context.Context, owner, p string) (*os.File, File, error) {
	f, err := s.Lookup(ctx, owner, p)
	if err != nil {
		return nil, File{}, err
	}
	r, err := os.Open(s.diskPath(f.Owner, f.Path))
	if err != nil {
		return nil, File{}, fmt.Errorf("open %s/%s: %w", f.Owner, f.Path, err)
	}
	return r, f, nil
}

// CleanTmp removes the temporary files left by interrupted writes. Call it only
// at startup, before anything can write.
func (s *Store) CleanTmp() (int, error) {
	entries, err := os.ReadDir(s.tmpDir())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.tmpDir(), e.Name())); err != nil {
			return 0, err
		}
	}
	return len(entries), nil
}

func detectMIME(p string, head []byte) string {
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}
	return http.DetectContentType(head)
}

func hashFile(name string) (sum, head []byte, err error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	h := sha256.New()
	hb := &headBuffer{max: 512}
	if _, err := io.Copy(io.MultiWriter(h, hb), f); err != nil {
		return nil, nil, err
	}
	return h.Sum(nil), hb.buf, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// headBuffer keeps the first max bytes written to it, for MIME sniffing.
type headBuffer struct {
	buf []byte
	max int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
