package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"os"
)

// PendingFile is a file being written by a client that sends its bytes over
// several calls (WebDAV). Its bytes go to a temporary file; Commit stores them
// with the same checks as Put, and Abort drops them. Nothing is visible until
// Commit succeeds.
type PendingFile struct {
	s     *Store
	ctx   context.Context
	owner string
	path  string
	opt   PutOptions
	tmp   *os.File
	sum   hash.Hash
	head  *headBuffer
	size  int64
	done  bool
}

// Create starts writing a file at owner/p. With opt.Replace, Commit replaces an
// existing file and keeps its content as a version.
func (s *Store) Create(ctx context.Context, owner, p string, opt PutOptions) (*PendingFile, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	if p, err = CleanPath(p); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.tmpDir(), 0o750); err != nil {
		return nil, fmt.Errorf("temporary folder: %w", err)
	}
	tmp, err := os.CreateTemp(s.tmpDir(), "dav-*")
	if err != nil {
		return nil, fmt.Errorf("temporary file: %w", err)
	}
	return &PendingFile{s: s, ctx: ctx, owner: owner, path: p, opt: opt, tmp: tmp, sum: sha256.New(), head: &headBuffer{max: 512}}, nil
}

// Write appends bytes to the pending file.
func (w *PendingFile) Write(b []byte) (int, error) {
	if w.done {
		return 0, errors.New("write to a committed or aborted file")
	}
	n, err := w.tmp.Write(b)
	w.sum.Write(b[:n])
	w.head.Write(b[:n]) //nolint:errcheck // headBuffer never fails
	w.size += int64(n)
	return n, err
}

// Commit syncs the bytes and stores them at the file's path.
func (w *PendingFile) Commit() (File, error) {
	if w.done {
		return File{}, errors.New("file already committed or aborted")
	}
	w.done = true
	return w.s.commitPending(w)
}

// Abort drops the bytes written so far.
func (w *PendingFile) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.s.dropPending(w)
}

// commitPending places the pending bytes, then removes its own temporary file.
func (s *Store) commitPending(w *PendingFile) (File, error) {
	name := w.tmp.Name()
	defer os.Remove(name) // its own temporary file under .cloudos/tmp; linked into place on success
	if err := w.tmp.Sync(); err != nil {
		w.tmp.Close()
		return File{}, fmt.Errorf("sync: %w", err)
	}
	if err := w.tmp.Close(); err != nil {
		return File{}, fmt.Errorf("close: %w", err)
	}
	return s.finish(w.ctx, w.owner, w.path, name, w.sum.Sum(nil), w.size, w.head.buf, w.opt)
}

// dropPending removes the temporary file of an aborted write.
func (s *Store) dropPending(w *PendingFile) {
	w.tmp.Close()
	os.Remove(w.tmp.Name()) //nolint:errcheck // its own temporary file under .cloudos/tmp
}
