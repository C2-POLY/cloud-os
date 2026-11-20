package smbfs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tobychui/smbsrv"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/davfs"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// The adapter for smbsrv (MIT, core/third_party/smbsrv), the library that
// speaks SMB (docs/decisions/006-smb.md).

// pendingTTL is how long a credential handed to smbsrv waits for the login's
// outcome; smbsrv reports it at once, so this only bounds a leak.
const pendingTTL = time.Minute

// backend signs accounts in with their SMB password and gives each one its
// drive.
type backend struct {
	ctx    context.Context
	auth   *auth.Service
	store  *storage.Store
	logger *slog.Logger
	// reg holds the files being written, for every account (write.go).
	reg   *registry
	apple *davfs.AppleDouble
	// free is the disk's free space, in bytes.
	free func() uint64

	mu sync.Mutex
	// pending holds the credential LookupHash handed out, by the client's
	// ip:port, until AuthLog says whether the response matched.
	pending map[string]pendingLogin
	// hashes holds, by account, the NT hashes its open sessions signed in
	// with: the sweep closes them when the account's hash is no longer one of them.
	hashes map[string]map[[16]byte]struct{}
}

type pendingLogin struct {
	cred auth.SMBCredential
	at   time.Time
}

func newBackend(ctx context.Context, a *auth.Service, s *storage.Store, logger *slog.Logger, free func() uint64) *backend {
	if free == nil {
		free = func() uint64 {
			_, f := diskUsage(s.Root)
			return f
		}
	}
	return &backend{
		ctx: ctx, auth: a, store: s, logger: logger, free: free,
		reg: newRegistry(), apple: &davfs.AppleDouble{Root: s.Root},
		pending: map[string]pendingLogin{},
		hashes:  map[string]map[[16]byte]struct{}{},
	}
}

// hostOf is the IP of an ip:port.
func hostOf(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}

// LookupHash gives smbsrv the NT hash of the account's SMB password. The
// attempt counts against the account and the client's address until AuthLog
// reports a valid response (auth.SMBHash).
func (b *backend) LookupHash(username, remote string) (string, []byte, bool) {
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	cred, err := b.auth.SMBHash(ctx, username, hostOf(remote))
	switch {
	case errors.Is(err, auth.ErrThrottled):
		b.logger.Warn("smb login throttled", "user", username, "remote", remote)
		return "", nil, false
	case errors.Is(err, auth.ErrBadCredentials):
		return "", nil, false
	case err != nil:
		b.logger.Error("smb login", "user", username, "remote", remote, "err", err)
		return "", nil, false
	}
	if cred.NTHash == ([16]byte{}) {
		// smbsrv keys the response's HMAC with the hash: a zero one is a
		// key anyone can compute
		return "", nil, false
	}
	b.mu.Lock()
	now := time.Now()
	for k, p := range b.pending {
		if now.Sub(p.at) > pendingTTL {
			delete(b.pending, k)
		}
	}
	b.pending[remote] = pendingLogin{cred: cred, at: now}
	b.mu.Unlock()
	hash := make([]byte, len(cred.NTHash))
	copy(hash, cred.NTHash[:])
	return cred.Account, hash, true
}

// AuthLog hears whether the client's response matched. A match forgets the
// failed attempts of the account at that address and notes the password's use.
func (b *backend) AuthLog(username, remote string, ok bool) time.Duration {
	b.mu.Lock()
	p, found := b.pending[remote]
	delete(b.pending, remote)
	if ok && found {
		set := b.hashes[p.cred.Account]
		if set == nil {
			set = map[[16]byte]struct{}{}
			b.hashes[p.cred.Account] = set
		}
		set[p.cred.NTHash] = struct{}{}
	}
	b.mu.Unlock()
	if ok && found {
		p.cred.Succeeded(b.ctx)
	}
	b.logger.Info("smb login", "user", username, "remote", remote, "ok", ok)
	return 0 // a failed login waits smbsrv's default delay
}

// forget drops the hashes an account's sessions signed in with: they are
// being closed.
func (b *backend) forget(account string) {
	b.mu.Lock()
	delete(b.hashes, strings.ToLower(account))
	b.mu.Unlock()
}

// sessionHashes returns, for each account given, the hashes its sessions
// signed in with, and forgets the accounts not given (no session left).
func (b *backend) sessionHashes(live map[string]bool) map[string][][16]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string][][16]byte{}
	for account, set := range b.hashes {
		if !live[account] {
			delete(b.hashes, account)
			continue
		}
		for h := range set {
			out[account] = append(out[account], h)
		}
	}
	return out
}

// Login gives a signed-in account its drive. account is the name LookupHash
// returned, in lower case.
func (b *backend) Login(account string) (smbsrv.UserView, error) {
	owner, err := storage.CleanOwner(account)
	if err != nil {
		return nil, os.ErrPermission
	}
	return user{name: owner, share: &share{
		ctx: b.ctx, view: &View{Store: b.store, Owner: owner},
		reg: b.reg, apple: b.apple, free: b.free, logger: b.logger,
	}}, nil
}

func (b *backend) Allow(remote net.IP) bool { return allowed(remote) }

type user struct {
	name  string
	share *share
}

func (u user) Username() string       { return u.name }
func (u user) Shares() []smbsrv.Share { return []smbsrv.Share{u.share} }

// spaceMargin is what the disk keeps free: a write that would leave less is
// refused with STATUS_DISK_FULL, so storage, the database and the versions
// always have room.
const spaceMargin = 1 << 30

// share is an account's drive. Every write goes through storage: a file is
// staged then stored on its client's CLOSE (write.go), a folder is created
// by Store.Mkdir, a delete goes to the trash, a rename is a journaled move,
// a date is metadata. The Finder's own files go to davfs.AppleDouble.
type share struct {
	ctx    context.Context
	view   *View
	reg    *registry
	apple  *davfs.AppleDouble
	free   func() uint64
	logger *slog.Logger
}

func toSmbsrv(i Info) smbsrv.FileInfo {
	return smbsrv.FileInfo{Name: i.Name, Size: i.Size, IsDir: i.IsDir, ModTime: i.Modified, BirthTime: i.Created, ID: i.ID}
}

func (s *share) owner() string { return s.view.Owner }

func (s *share) Name() string    { return ShareName }
func (s *share) Comment() string { return "Cloud OS" }

// Key names the account's files: locks, share modes and notifications of two
// accounts never meet.
func (s *share) Key() string                      { return "cloudos:" + s.view.Owner }
func (s *share) ReadOnly() bool                   { return false }
func (s *share) CanRead(string) bool              { return true }
func (s *share) CanWrite(string) bool             { return true }
func (s *share) FileChanged(string, int64, int64) {}

// HaveSpace leaves the check to the files: smbsrv asks it before a write and
// refuses on its own, and the file would then not know that a piece of it
// is missing. writeFile.room keeps spaceMargin free and, when it refuses,
// keeps the file from being stored.
func (s *share) HaveSpace(int64) bool { return true }

// room reports whether the disk can take extra more bytes and still keep
// spaceMargin free.
func (s *share) room(extra int64) bool {
	if extra <= 0 {
		return true
	}
	free := s.free()
	return free >= spaceMargin && uint64(extra) <= free-spaceMargin
}

// isApple reports a path of the Finder's own files (.DS_Store, ._*,
// .TemporaryItems and what it holds): they stay out of the drive's index.
func isApple(p string) bool { return davfs.IsApplePath(p) }

func appleInfo(fi os.FileInfo) Info {
	return Info{Name: fi.Name(), Size: fi.Size(), IsDir: fi.IsDir(), Modified: fi.ModTime(), Created: fi.ModTime()}
}

func (s *share) Stat(name string) (smbsrv.FileInfo, error) {
	p, err := Clean(name)
	if err != nil {
		return smbsrv.FileInfo{}, err
	}
	if p != "" && isApple(p) {
		fi, err := s.apple.Stat(s.owner(), p)
		if err != nil {
			return smbsrv.FileInfo{}, err
		}
		return toSmbsrv(appleInfo(fi)), nil
	}
	i, err := s.view.Stat(s.ctx, p)
	if e := s.reg.find(s.owner(), p); e != nil && p != "" {
		switch {
		case err == nil && !i.IsDir:
			i.Size = e.st.Size()
			return toSmbsrv(i), nil
		case errors.Is(err, os.ErrNotExist):
			s.reg.mu.Lock()
			i = e.infoLocked()
			s.reg.mu.Unlock()
			return toSmbsrv(i), nil
		}
	}
	return toSmbsrv(i), err
}

// ReadDir lists a folder: the stored files, with the size a file being
// written has now, and the new files being written. The Finder's files are
// left out, as in WebDAV.
func (s *share) ReadDir(name string) ([]smbsrv.FileInfo, error) {
	p, err := Clean(name)
	if err != nil {
		return nil, err
	}
	if p != "" && isApple(p) {
		return nil, nil
	}
	list, err := s.view.List(s.ctx, p)
	if err != nil {
		return nil, err
	}
	pending := s.reg.under(s.owner(), p)
	out := make([]smbsrv.FileInfo, 0, len(list)+len(pending))
	seen := map[string]bool{}
	for _, c := range list {
		for _, w := range pending {
			if !c.IsDir && strings.EqualFold(w.Name, c.Name) {
				c.Size = w.Size
			}
		}
		seen[strings.ToLower(c.Name)] = true
		out = append(out, toSmbsrv(c))
	}
	for _, w := range pending {
		if !seen[strings.ToLower(w.Name)] {
			out = append(out, toSmbsrv(w))
		}
	}
	return out, nil
}

// Open opens a file. An open for reading of a file nobody writes reads the
// stored file; any other open is a handle on its staged file.
func (s *share) Open(name string, flag int) (smbsrv.File, error) {
	p, err := Clean(name)
	if err != nil {
		return nil, err
	}
	if p == "" {
		return nil, fmt.Errorf("smbfs: the root is a folder: %w", os.ErrInvalid)
	}
	if isApple(p) {
		f, err := s.apple.Open(s.owner(), p, flag)
		if err != nil {
			return nil, err
		}
		return &appleFile{f: f, share: s}, nil
	}
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	if !writing && s.reg.find(s.owner(), p) == nil {
		f, _, err := s.view.Open(s.ctx, p)
		if err != nil {
			return nil, err
		}
		return file{f}, nil
	}
	e, err := s.reg.acquire(s.ctx, s.view.Store, s.owner(), p,
		flag&os.O_CREATE != 0, flag&os.O_EXCL != 0, flag&os.O_TRUNC != 0)
	if err != nil {
		return nil, err
	}
	return &writeFile{share: s, e: e}, nil
}

// Mkdir creates a folder in storage, its missing parents with it.
func (s *share) Mkdir(name string) error {
	p, err := Clean(name)
	if err != nil {
		return err
	}
	if p == "" {
		return os.ErrExist
	}
	if isApple(p) {
		return s.apple.Mkdir(s.owner(), p)
	}
	_, err = s.view.Store.Mkdir(s.ctx, s.owner(), p)
	return toOS(err)
}

// Remove sends a file or folder to the trash: nothing is erased. smbsrv calls
// it once the last handle on the path is closed.
func (s *share) Remove(name string) error {
	p, err := Clean(name)
	if err != nil {
		return err
	}
	if p == "" {
		return os.ErrPermission
	}
	if isApple(p) {
		return s.apple.Remove(s.owner(), p)
	}
	_, err = s.view.Store.Trash(s.ctx, s.owner(), p)
	return toOS(err)
}

// Rename moves a file or folder. smbsrv has checked that the client asked to
// replace an existing target, and that the target is not open; a rename onto
// a file is Store.MoveReplace, which keeps the target's content as a version.
// A file being written follows its handle: a new one only in the registry,
// a stored one in storage too.
func (s *share) Rename(oldName, newName string) error {
	from, err := Clean(oldName)
	if err != nil {
		return err
	}
	to, err := Clean(newName)
	if err != nil {
		return err
	}
	if from == "" || to == "" {
		return os.ErrPermission
	}
	own := s.owner()
	switch fromApple, toApple := isApple(from), isApple(to); {
	case fromApple && toApple:
		return s.apple.Rename(own, from, to)
	case fromApple:
		return s.adoptApple(from, to)
	case toApple:
		// A document hidden among the Finder's files would leave the drive
		return fmt.Errorf("smbfs: %s cannot become a Finder file: %w", from, os.ErrPermission)
	}
	if _, err := s.view.Store.Lookup(s.ctx, own, from); err == nil {
		if _, err := s.view.Store.MoveReplace(s.ctx, own, from, to); err != nil {
			return toOS(err)
		}
	} else if !errors.Is(err, storage.ErrNotFound) {
		return toOS(err)
	} else if s.reg.find(own, from) == nil {
		return fmt.Errorf("smbfs: %s: %w", from, os.ErrNotExist)
	}
	_, err = s.reg.rename(own, from, to)
	return err
}

// adoptApple stores a Finder file renamed to a document's name (an app that
// saves through .TemporaryItems), as a Put that keeps the content it
// replaces as a version, then drops the Finder file.
func (s *share) adoptApple(from, to string) error {
	f, err := s.apple.Open(s.owner(), from, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return fmt.Errorf("smbfs: %s is not a file: %w", from, os.ErrPermission)
	}
	// Put writes a full copy: a sparse Finder file must not fill the disk
	if !s.room(info.Size()) {
		return fmt.Errorf("smbfs: no room to store %s: %w", to, syscall.ENOSPC)
	}
	opt := storage.NoExpectations()
	opt.Replace = true
	opt.ModTime = info.ModTime()
	if _, err := s.view.Store.Put(s.ctx, s.owner(), to, f, opt); err != nil {
		return toOS(err)
	}
	return s.apple.Remove(s.owner(), from)
}

// SetModTime sets a date: on a stored file or folder, in storage; on a file
// being written, at its Commit. The Finder's files keep the disk's own.
func (s *share) SetModTime(name string, t time.Time) error {
	p, err := Clean(name)
	if err != nil {
		return err
	}
	if p == "" || isApple(p) {
		return nil
	}
	if s.reg.setModTime(s.owner(), p, t) {
		return nil
	}
	_, err = s.view.Store.SetModTime(s.ctx, s.owner(), p, t)
	return toOS(err)
}

// Usage is what the Finder shows as the drive's size: the disk's.
func (s *share) Usage() (total, free uint64) { return diskUsage(s.view.Store.Root) }

// file is a stored file opened for reading. Writing goes through writeFile.
type file struct{ f *os.File }

func (f file) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, os.ErrInvalid
	}
	return f.f.ReadAt(b, off)
}
func (f file) WriteAt([]byte, int64) (int, error) { return 0, os.ErrPermission }
func (f file) Truncate(int64) error               { return os.ErrPermission }
func (f file) Sync() error                        { return nil }
func (f file) Close() error                       { return f.f.Close() }

// appleFile is one of the Finder's own files, under .cloudos/appledouble.
type appleFile struct {
	f     *os.File
	share *share
}

func (f *appleFile) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, os.ErrInvalid
	}
	return f.f.ReadAt(b, off)
}

func (f *appleFile) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, os.ErrInvalid
	}
	// What the write adds past the end, holes included
	if info, err := f.f.Stat(); err != nil || !f.share.room(off+int64(len(b))-info.Size()) {
		return 0, syscall.ENOSPC
	}
	return f.f.WriteAt(b, off)
}

// Truncate cuts or grows a Finder file under .cloudos/appledouble, never a document.
func (f *appleFile) Truncate(size int64) error {
	if info, err := f.f.Stat(); err == nil && !f.share.room(size-info.Size()) {
		return syscall.ENOSPC
	}
	return f.f.Truncate(size)
}
func (f *appleFile) Sync() error  { return nil }
func (f *appleFile) Close() error { return f.f.Close() }
