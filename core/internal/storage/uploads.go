package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// UploadTTL is how long an upload lives without receiving anything.
const UploadTTL = 24 * time.Hour

var (
	// ErrBusy is returned when another request is writing the same upload.
	ErrBusy = errors.New("the upload is being written by another request")
	// ErrOffset is returned when a chunk does not start where the upload stands.
	ErrOffset = errors.New("the chunk does not start at the upload's offset")
	// ErrTooLarge is returned when a file or chunk is longer than allowed or than the free space.
	ErrTooLarge = errors.New("too large")
)

var uploadIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Upload is a resumable upload in progress. Offset is the number of bytes
// received, read from the size of its partial file.
type Upload struct {
	ID         string
	Owner      string
	Path       string
	Length     int64
	Offset     int64
	SHA256     []byte
	ModifiedAt *time.Time
	ExpiresAt  time.Time
	Replace    bool // replace a file already at Path, keeping its content as a version
}

// uploadLocks serialises the writes of one upload within this core.
var uploadLocks sync.Map // id → *sync.Mutex

func (s *Store) uploadsDir() string { return filepath.Join(s.Root, ".cloudos", "uploads") }

func (s *Store) uploadFile(id string) string { return filepath.Join(s.uploadsDir(), id) }

// CreateUpload starts an upload of length bytes to owner/p. It refuses at once
// a name already taken (ErrExists) and a file larger than maxSize, when
// positive, or than the free space (ErrTooLarge).
func (s *Store) CreateUpload(ctx context.Context, owner, p string, length int64, sha []byte, mod *time.Time, maxSize int64, replace bool) (Upload, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return Upload{}, err
	}
	if p, err = CleanPath(p); err != nil {
		return Upload{}, err
	}
	if length < 0 {
		return Upload{}, fmt.Errorf("%w: negative length", ErrInvalidPath)
	}
	if maxSize > 0 && length > maxSize {
		return Upload{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, length, maxSize)
	}
	if err := os.MkdirAll(s.uploadsDir(), 0o750); err != nil {
		return Upload{}, err
	}
	if free, err := freeBytes(s.uploadsDir()); err == nil && uint64(length) > free {
		return Upload{}, fmt.Errorf("%w: %d bytes, %d free on the disk", ErrTooLarge, length, free)
	}
	// A replacing upload only needs the path to be a file, if anything.
	existing, err := s.Lookup(ctx, owner, p)
	switch {
	case replace && err == nil && !existing.IsDir():
	case err != nil && !errors.Is(err, ErrNotFound):
		return Upload{}, err
	default:
		why, err := conflict(ctx, s.Pool, owner, p, "")
		if err != nil {
			return Upload{}, err
		}
		if why != "" {
			return Upload{}, fmt.Errorf("%w: %s/%s: %s", ErrExists, owner, p, why)
		}
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Upload{}, err
	}
	id := hex.EncodeToString(raw)
	// The row comes first: a partial file without a row is an orphan, and
	// CleanUploads may remove it.
	u := Upload{ID: id, Owner: owner, Path: p, Length: length, SHA256: sha, ModifiedAt: mod, Replace: replace}
	if err := s.Pool.QueryRow(ctx,
		`INSERT INTO uploads (id, owner, path, length, sha256, modified_at, expires_at, replace_existing)
		 VALUES ($1, $2, $3, $4, $5, $6, now() + make_interval(secs => $7), $8) RETURNING expires_at`,
		id, owner, p, length, sha, mod, UploadTTL.Seconds(), replace).Scan(&u.ExpiresAt); err != nil {
		return Upload{}, err
	}
	f, err := os.OpenFile(s.uploadFile(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		s.Pool.Exec(ctx, `DELETE FROM uploads WHERE id = $1`, id) //nolint:errcheck // best effort, CleanUploads also catches it
		return Upload{}, err
	}
	if err := f.Close(); err != nil {
		return Upload{}, err
	}
	return u, nil
}

// FinishEmptyUpload places an upload of 0 bytes, which no chunk will ever complete.
func (s *Store) FinishEmptyUpload(ctx context.Context, u Upload) (*File, error) {
	if u.Length != 0 {
		return nil, fmt.Errorf("upload %s is not empty", u.ID)
	}
	return s.finishUpload(ctx, u)
}

// GetUpload returns owner's upload id with its current offset. Another
// owner's upload, an expired one or an unknown id is ErrNotFound.
func (s *Store) GetUpload(ctx context.Context, owner, id string) (Upload, error) {
	if !uploadIDRe.MatchString(id) {
		return Upload{}, ErrNotFound
	}
	var u Upload
	err := s.Pool.QueryRow(ctx,
		`SELECT id, owner, path, length, sha256, modified_at, expires_at, replace_existing FROM uploads
		 WHERE id = $1 AND owner = $2 AND expires_at > now()`, id, owner).
		Scan(&u.ID, &u.Owner, &u.Path, &u.Length, &u.SHA256, &u.ModifiedAt, &u.ExpiresAt, &u.Replace)
	if errors.Is(err, pgx.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	info, err := os.Stat(s.uploadFile(id))
	if errors.Is(err, fs.ErrNotExist) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	u.Offset = info.Size()
	return u, nil
}

// AppendUpload writes the bytes of r at offset, which must be the upload's
// current offset. Bytes received before an interruption are kept: they are
// where the client resumes. When the last byte arrives, the file is placed
// with PlaceFile and the upload ends; the returned File is then set.
func (s *Store) AppendUpload(ctx context.Context, owner, id string, offset int64, r io.Reader) (Upload, *File, error) {
	mu, _ := uploadLocks.LoadOrStore(id, &sync.Mutex{})
	if !mu.(*sync.Mutex).TryLock() {
		return Upload{}, nil, ErrBusy
	}
	defer mu.(*sync.Mutex).Unlock()

	u, err := s.GetUpload(ctx, owner, id)
	if errors.Is(err, ErrNotFound) {
		uploadLocks.Delete(id) // an unknown id must not leave a lock behind
	}
	if err != nil {
		return Upload{}, nil, err
	}
	if offset != u.Offset {
		return u, nil, fmt.Errorf("%w: got %d, the upload is at %d", ErrOffset, offset, u.Offset)
	}

	n, copyErr := s.appendBytes(u, r)
	u.Offset += n
	if n > 0 {
		if _, err := s.Pool.Exec(ctx, `UPDATE uploads SET expires_at = now() + make_interval(secs => $2) WHERE id = $1`,
			id, UploadTTL.Seconds()); err != nil {
			return u, nil, err
		}
		u.ExpiresAt = time.Now().Add(UploadTTL)
	}
	if copyErr != nil {
		return u, nil, copyErr
	}
	if u.Offset < u.Length {
		return u, nil, nil
	}
	f, err := s.finishUpload(ctx, u)
	return u, f, err
}

// appendBytes appends at most the missing bytes of u from r and syncs them.
// A body longer than the upload is refused and the partial file cut back to
// where it was.
func (s *Store) appendBytes(u Upload, r io.Reader) (int64, error) {
	f, err := os.OpenFile(s.uploadFile(u.ID), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	missing := u.Length - u.Offset
	n, copyErr := io.Copy(f, io.LimitReader(r, missing+1))
	if n > missing {
		if err := f.Truncate(u.Offset); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("%w: the chunk goes past the upload's length of %d", ErrTooLarge, u.Length)
	}
	if err := f.Sync(); err != nil {
		return n, err
	}
	return n, copyErr
}

// finishUpload places a complete upload and ends it. An upload whose content
// or name cannot be placed (hash mismatch, name taken) is ended too: sending
// it again would not change the answer.
func (s *Store) finishUpload(ctx context.Context, u Upload) (*File, error) {
	opt := PutOptions{ExpectSize: u.Length, ExpectSHA256: u.SHA256, Replace: u.Replace}
	if u.ModifiedAt != nil {
		opt.ModTime = *u.ModifiedAt
	}
	f, err := s.PlaceFile(ctx, u.Owner, u.Path, s.uploadFile(u.ID), opt)
	if err != nil && !errors.Is(err, ErrExists) && !errors.Is(err, ErrMismatch) {
		return nil, err // kept: an empty PATCH at the end retries
	}
	if derr := s.endUpload(ctx, u.ID); derr != nil {
		slog.Error("end upload", "id", u.ID, "err", derr)
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// DeleteUpload abandons owner's upload id and removes its partial file.
func (s *Store) DeleteUpload(ctx context.Context, owner, id string) error {
	mu, _ := uploadLocks.LoadOrStore(id, &sync.Mutex{})
	if !mu.(*sync.Mutex).TryLock() {
		return ErrBusy
	}
	defer mu.(*sync.Mutex).Unlock()
	if _, err := s.GetUpload(ctx, owner, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			uploadLocks.Delete(id)
		}
		return err
	}
	return s.endUpload(ctx, id)
}

// endUpload removes the partial file, then the row. Only files under
// .cloudos/uploads are ever removed here.
func (s *Store) endUpload(ctx context.Context, id string) error {
	if err := os.Remove(s.uploadFile(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM uploads WHERE id = $1`, id)
	uploadLocks.Delete(id)
	return err
}

// CleanUploads ends the expired uploads and removes partial files that no
// upload owns. It only touches .cloudos/uploads.
func (s *Store) CleanUploads(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM uploads WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	expired, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range expired {
		mu, _ := uploadLocks.LoadOrStore(id, &sync.Mutex{})
		if !mu.(*sync.Mutex).TryLock() {
			continue // being written: it is not abandoned
		}
		err := s.endUpload(ctx, id)
		mu.(*sync.Mutex).Unlock()
		if err != nil {
			return removed, err
		}
		removed++
	}

	entries, err := os.ReadDir(s.uploadsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return removed, nil
	}
	if err != nil {
		return removed, err
	}
	for _, e := range entries {
		var exists bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM uploads WHERE id = $1)`, e.Name()).Scan(&exists); err != nil {
			return removed, err
		}
		if exists {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.uploadsDir(), e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// freeBytes returns the space available to the core on dir's file system.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // Bsize is positive
}
