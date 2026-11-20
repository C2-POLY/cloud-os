package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Staged is a file being written at random offsets by a client that sends its
// bytes over many calls and reads them back meanwhile (SMB). Its bytes live in
// a working file of its own under .cloudos/tmp/smb, never in the stored file:
// Commit hashes the working file and links it into place like Put, and Abort
// drops it. Nothing is visible in the index until Commit succeeds.
//
// A staged file that starts from a stored file reads that file until the
// first write or truncation, and copies it into the working file only then:
// the Finder opens files for writing and often writes nothing.
type Staged struct {
	s     *Store
	ctx   context.Context
	owner string

	mu   sync.Mutex
	path string
	base *os.File // the stored content it started from, read until the first change
	work *os.File // the working file, nil until the first change
	// size is read without mu (Size), so that a listing never waits for a
	// copy in progress; it changes under mu.
	size atomic.Int64
	// changed is set once the content may differ from what is stored: a
	// write, a truncation, or a staged file that starts empty.
	changed bool
	done    bool
}

// stageDir holds the working files of SMB writes. Only `core smb` empties it,
// at its start (CleanStaged); CleanTmp leaves it alone, so a restart of the
// core never erases an SMB write in progress.
func (s *Store) stageDir() string { return filepath.Join(s.tmpDir(), "smb") }

// Stage starts writing owner/p. base is the stored file the content starts
// from (Lookup's row), or nil for an empty file: a new file, or a stored file
// the client truncates on open. Nothing is stored before Commit.
func (s *Store) Stage(ctx context.Context, owner, p string, base *File) (*Staged, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	if p, err = CleanPath(p); err != nil {
		return nil, err
	}
	st := &Staged{s: s, ctx: ctx, owner: owner, path: p}
	if base == nil {
		if err := st.startWork(); err != nil {
			return nil, err
		}
		st.changed = true
		return st, nil
	}
	if base.Owner != owner || base.IsDir() {
		return nil, fmt.Errorf("%w: stage %s/%s from %s/%s", ErrInvalidPath, owner, p, base.Owner, base.Path)
	}
	r, err := s.OpenFile(*base)
	if err != nil {
		return nil, err
	}
	info, err := r.Stat()
	if err != nil {
		r.Close()
		return nil, err
	}
	st.base = r
	st.size.Store(info.Size())
	return st, nil
}

// startWork creates the empty working file.
func (st *Staged) startWork() error {
	if err := os.MkdirAll(st.s.stageDir(), 0o750); err != nil {
		return fmt.Errorf("staging folder: %w", err)
	}
	f, err := os.CreateTemp(st.s.stageDir(), "stage-*")
	if err != nil {
		return fmt.Errorf("working file: %w", err)
	}
	st.work = f
	return nil
}

// materialize copies the first n bytes of the stored content into a new
// working file before the first change. It is a copy, never a link: the
// stored file is not touched.
func (st *Staged) materialize(n int64) error {
	if st.work != nil {
		return nil
	}
	if err := st.startWork(); err != nil {
		return err
	}
	if _, err := io.Copy(st.work, io.NewSectionReader(st.base, 0, min(n, st.size.Load()))); err != nil {
		st.dropWork()
		return fmt.Errorf("copy the stored content: %w", err)
	}
	st.base.Close()
	st.base = nil
	return nil
}

// Path is where the staged file will be stored.
func (st *Staged) Path() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.path
}

// SetPath changes where the staged file will be stored, after the client
// renamed it while it was open.
func (st *Staged) SetPath(p string) error {
	p, err := CleanPath(p)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.path = p
	return nil
}

// Size is the size of the content now, written but not yet stored included.
// It never waits for a write or a copy in progress.
func (st *Staged) Size() int64 { return st.size.Load() }

// Changed reports whether Commit would store anything.
func (st *Staged) Changed() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.changed
}

// Copied reports whether the stored content was already copied, or needs no
// copy: false while the first write would still copy Size bytes.
func (st *Staged) Copied() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.work != nil
}

var errStageDone = errors.New("storage: the staged file is already committed or aborted")

// ReadAt reads the content now: the working file, or the stored file before
// the first change.
func (st *Staged) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return 0, errStageDone
	}
	size := st.size.Load()
	if off >= size {
		return 0, io.EOF
	}
	if int64(len(b)) > size-off {
		b = b[:size-off]
	}
	var n int
	var err error
	if st.work != nil {
		n, err = st.work.ReadAt(b, off)
	} else {
		n, err = st.base.ReadAt(b, off)
	}
	if err == nil && n < len(b) {
		err = io.EOF
	}
	return n, err
}

// WriteAt writes into the working file.
func (st *Staged) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return 0, errStageDone
	}
	if err := st.materialize(st.size.Load()); err != nil {
		return 0, err
	}
	n, err := st.work.WriteAt(b, off)
	if n > 0 {
		st.changed = true
		st.size.Store(max(st.size.Load(), off+int64(n)))
	}
	return n, err
}

// Truncate sets the size of the working file: it cuts the client's own
// working copy, never the stored file.
func (st *Staged) Truncate(size int64) error {
	if size < 0 {
		return fs.ErrInvalid
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return errStageDone
	}
	if size == st.size.Load() && st.work != nil {
		return nil
	}
	// Only the part kept needs copying.
	if err := st.materialize(size); err != nil {
		return err
	}
	if err := st.work.Truncate(size); err != nil {
		return err
	}
	st.size.Store(size)
	st.changed = true
	return nil
}

// Commit stores the content at the staged file's path, as Put with Replace: a
// file already there, whether the one it started from or one stored there
// since by someone else, has its content kept as a version first. The last
// writer wins and nothing is lost. A content identical to the stored one
// stores nothing. The working file is gone afterwards, whatever the outcome.
func (st *Staged) Commit(opt PutOptions) (File, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return File{}, errStageDone
	}
	st.done = true
	if !st.changed {
		// Opened for writing, nothing written: there is nothing to store,
		// even if the file has gone meanwhile.
		st.closeBase()
		f, err := st.s.Lookup(st.ctx, st.owner, st.path)
		if errors.Is(err, ErrNotFound) {
			return File{}, nil
		}
		return f, err
	}
	return st.s.commitStage(st, opt)
}

// Abort drops what was written. The stored file, if any, is untouched.
func (st *Staged) Abort() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return
	}
	st.done = true
	st.closeBase()
	st.dropWork()
}

func (st *Staged) closeBase() {
	if st.base != nil {
		st.base.Close()
		st.base = nil
	}
}

// commitStage syncs and closes the working file, then stores it. The file is
// closed before it is linked: nothing can write through it once stored.
func (s *Store) commitStage(st *Staged, opt PutOptions) (File, error) {
	st.closeBase()
	name := st.work.Name()
	// Its own working file under .cloudos/tmp/smb: linked into place on
	// success, dropped on failure.
	defer os.Remove(name)
	if err := st.work.Sync(); err != nil {
		st.work.Close()
		return File{}, fmt.Errorf("sync: %w", err)
	}
	if err := st.work.Close(); err != nil {
		return File{}, fmt.Errorf("close: %w", err)
	}
	info, err := os.Lstat(name)
	if err != nil {
		return File{}, err
	}
	sum, sniff, err := hashFile(name)
	if err != nil {
		return File{}, err
	}
	if cur, err := s.Lookup(st.ctx, st.owner, st.path); err == nil && !cur.IsDir() &&
		cur.Size == info.Size() && bytes.Equal(cur.SHA256, sum) {
		return cur, nil
	}
	opt.Replace = true
	return s.finish(st.ctx, st.owner, st.path, name, sum, info.Size(), sniff, opt)
}

// dropWork removes the working file of an aborted write.
func (st *Staged) dropWork() {
	if st.work == nil {
		return
	}
	st.work.Close()
	os.Remove(st.work.Name()) //nolint:errcheck // its own working file under .cloudos/tmp/smb
	st.work = nil
}

// CleanStaged removes the working files left by SMB writes that never
// finished. Only `core smb` calls it, at its start, before it serves: nobody
// else writes there.
func (s *Store) CleanStaged() (int, error) {
	entries, err := os.ReadDir(s.stageDir())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.stageDir(), e.Name())); err != nil {
			return 0, err
		}
	}
	return len(entries), nil
}
