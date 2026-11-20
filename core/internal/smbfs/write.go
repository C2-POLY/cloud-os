package smbfs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// The registry of the files being written over SMB.
//
// SMB writes a file in pieces, at any offset, and reads it back meanwhile.
// Every handle open for writing on one path, from any connection of the
// account, shares one storage.Staged working file. Stat and List show it
// with the size it has now, even when it is new. It is stored (Commit) when
// its last handle closes with the client's CLOSE, and dropped (Abort) if a
// handle that wrote ends any other way: a connection cut, a durable handle
// nobody reclaimed, the server stopping, a delete pending. Nothing a client
// half wrote is ever stored, and the stored file stays as it was.

// registry holds the staged files of every account the server serves.
type registry struct {
	mu   sync.Mutex
	open map[string]*staging // by stagingKey
}

func newRegistry() *registry { return &registry{open: map[string]*staging{}} }

// staging is one staged file and the handles that share it.
type staging struct {
	st      *storage.Staged
	owner   string
	path    string // where it will be stored; follows a rename
	created time.Time
	handles int
	// tainted is set when a handle that wrote ends without a CLOSE, or when
	// a write failed: the content may be half of what the client meant, and
	// is not stored.
	tainted bool
	// modTime is a date the client set while the file was open; it is the
	// stored file's date at Commit.
	modTime time.Time
}

// stagingKey names a path the way smbsrv and storage compare them: per
// account, ignoring case.
func stagingKey(owner, p string) string { return owner + "\x00" + strings.ToLower(p) }

// find returns the staged file at p, or nil.
func (r *registry) find(owner, p string) *staging {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open[stagingKey(owner, p)]
}

// under returns what the staged files directly in folder dir look like now.
func (r *registry) under(owner, dir string) []Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Info
	for _, e := range r.open {
		if e.owner != owner {
			continue
		}
		parent := path.Dir(e.path)
		if parent == "." {
			parent = ""
		}
		if strings.EqualFold(parent, dir) {
			out = append(out, e.infoLocked())
		}
	}
	return out
}

// infoLocked describes a staged file as the client sees it. r.mu is held.
func (e *staging) infoLocked() Info {
	mod := e.modTime
	if mod.IsZero() {
		mod = e.created
	}
	return Info{Name: path.Base(e.path), Size: e.st.Size(), Modified: mod, Created: e.created}
}

// acquire gives a new handle on the staged file at p, staging it first from
// what storage holds unless another handle already did. truncate starts it
// empty; create allows a path storage does not have; exclusive refuses one it has.
func (r *registry) acquire(ctx context.Context, store *storage.Store, owner, p string, create, exclusive, truncate bool) (*staging, error) {
	if e, err := r.join(owner, p, create, exclusive, truncate); e != nil || err != nil {
		return e, err
	}
	var base *storage.File
	f, err := store.Lookup(ctx, owner, p)
	switch {
	case err == nil && f.IsDir():
		return nil, fmt.Errorf("smbfs: %s is a folder: %w", p, os.ErrInvalid)
	case err == nil && exclusive:
		return nil, fmt.Errorf("smbfs: %s: %w", p, os.ErrExist)
	case err == nil && !truncate:
		base = &f
	case errors.Is(err, storage.ErrNotFound):
		if !create {
			return nil, fmt.Errorf("smbfs: %s: %w", p, os.ErrNotExist)
		}
	case err != nil:
		return nil, toOS(err)
	}
	st, err := store.Stage(ctx, owner, p, base)
	if err != nil {
		return nil, toOS(err)
	}
	r.mu.Lock()
	key := stagingKey(owner, p)
	if e := r.open[key]; e != nil {
		// Another handle staged it meanwhile: share that one.
		e.handles++
		r.mu.Unlock()
		st.Abort()
		return r.truncated(e, truncate)
	}
	e := &staging{st: st, owner: owner, path: p, created: time.Now(), handles: 1}
	r.open[key] = e
	r.mu.Unlock()
	return e, nil
}

// join adds a handle to the staged file at p, if there is one.
func (r *registry) join(owner, p string, create, exclusive, truncate bool) (*staging, error) {
	r.mu.Lock()
	e := r.open[stagingKey(owner, p)]
	if e == nil {
		r.mu.Unlock()
		return nil, nil
	}
	if create && exclusive {
		r.mu.Unlock()
		return nil, fmt.Errorf("smbfs: %s: %w", p, os.ErrExist)
	}
	e.handles++
	r.mu.Unlock()
	return r.truncated(e, truncate)
}

// truncated empties the staged file of a new handle that truncates, outside
// the registry's lock: the staged file waits for a copy in progress, and the
// other accounts must not wait with it. A failure gives the handle back.
func (r *registry) truncated(e *staging, truncate bool) (*staging, error) {
	if !truncate {
		return e, nil
	}
	if err := emptyStaged(e); err != nil {
		r.mu.Lock()
		e.handles--
		e.tainted = true // its content is no longer what any client wrote
		last := e.handles == 0
		if last {
			delete(r.open, stagingKey(e.owner, e.path))
		}
		r.mu.Unlock()
		if last {
			e.st.Abort()
		}
		return nil, err
	}
	return e, nil
}

// emptyStaged empties a staged working file, for an open that truncates a
// file being written: the stored file is not touched.
func emptyStaged(e *staging) error { return e.st.Truncate(0) }

// release ends one handle. The last one stores the file, unless a handle
// that wrote ended without a CLOSE, or one of its writes failed (a full
// disk): what is there is not what the client meant. clientClose is whether
// this one closed with a CLOSE.
func (r *registry) release(e *staging, wrote, failed, clientClose bool, logger *slog.Logger) error {
	r.mu.Lock()
	if (wrote && !clientClose) || failed {
		e.tainted = true
	}
	e.handles--
	if e.handles > 0 {
		r.mu.Unlock()
		return nil
	}
	delete(r.open, stagingKey(e.owner, e.path))
	tainted, p, mod := e.tainted, e.path, e.modTime
	r.mu.Unlock()

	if tainted {
		e.st.Abort()
		logger.Info("smb write dropped: cut off before its CLOSE, or a write failed", "owner", e.owner, "path", p)
		if clientClose {
			return fmt.Errorf("smbfs: %s was not saved: a write to it was cut off: %w", p, errNotSaved)
		}
		return nil
	}
	opt := storage.NoExpectations()
	opt.ModTime = mod
	_, err := e.st.Commit(opt)
	if err != nil {
		logger.Error("smb write not stored", "owner", e.owner, "path", p, "err", err)
		return toOS(err)
	}
	return nil
}

// errNotSaved is what a CLOSE gets when the file it closes was not stored.
var errNotSaved = errors.New("not saved")

// rename moves the staged file at from, if there is one, to to. It reports
// whether there was one. A staged file whose path storage has (it started
// from a stored file) is moved in storage first by the caller.
func (r *registry) rename(owner, from, to string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	oldKey, newKey := stagingKey(owner, from), stagingKey(owner, to)
	e := r.open[oldKey]
	if e == nil {
		return false, nil
	}
	if other := r.open[newKey]; other != nil && other != e {
		return true, fmt.Errorf("smbfs: %s is being written: %w", to, os.ErrPermission)
	}
	if err := e.st.SetPath(to); err != nil {
		return true, toOS(err)
	}
	delete(r.open, oldKey)
	e.path = to
	r.open[newKey] = e
	return true, nil
}

// setModTime records a date the client set on a staged file.
func (r *registry) setModTime(owner, p string, t time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.open[stagingKey(owner, p)]
	if e == nil {
		return false
	}
	e.modTime = t
	return true
}

// writeFile is one handle on a staged file. It implements smbsrv's
// PendingFile (its size before it is stored) and Aborter (dropped without a
// CLOSE).
type writeFile struct {
	share *share
	e     *staging

	mu     sync.Mutex
	wrote  bool
	failed bool // a write or a truncation failed: nothing is stored
	ended  bool
}

func (f *writeFile) ReadAt(b []byte, off int64) (int, error) { return f.e.st.ReadAt(b, off) }

// room refuses a write when the disk lacks the space for it: the copy of the
// stored content the first change makes, plus the bytes added.
func (f *writeFile) room(newSize int64) error {
	need := max(newSize-f.e.st.Size(), 0)
	if !f.e.st.Copied() {
		need += f.e.st.Size()
	}
	if !f.share.room(need) {
		return fmt.Errorf("smbfs: no room for %d more bytes: %w", need, syscall.ENOSPC)
	}
	return nil
}

// changed records a write or a truncation, and whether it failed.
func (f *writeFile) changed(err error) error {
	f.mu.Lock()
	f.wrote = true
	if err != nil {
		f.failed = true
	}
	f.mu.Unlock()
	return err
}

func (f *writeFile) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, os.ErrInvalid
	}
	if err := f.room(off + int64(len(b))); err != nil {
		return 0, f.changed(err)
	}
	n, err := f.e.st.WriteAt(b, off)
	return n, f.changed(err)
}

// Truncate sets the size of the staged working file, never the stored file's.
func (f *writeFile) Truncate(size int64) error {
	if err := f.room(size); err != nil {
		return f.changed(err)
	}
	return f.changed(f.e.st.Truncate(size))
}

func (f *writeFile) Sync() error { return nil }

func (f *writeFile) PendingSize() (int64, bool) { return f.e.st.Size(), true }

// end releases the handle once; clientClose is whether the client closed it.
func (f *writeFile) end(clientClose bool) error {
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return nil
	}
	f.ended = true
	wrote, failed := f.wrote, f.failed
	f.mu.Unlock()
	return f.share.reg.release(f.e, wrote, failed, clientClose, f.share.logger)
}

// Close is the client's CLOSE: the last handle stores the file.
func (f *writeFile) Close() error { return f.end(true) }

// Abort is any other end of the handle: what it wrote is not stored.
func (f *writeFile) Abort() error { return f.end(false) }
