package smbfs

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tobychui/smbsrv"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
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

func newBackend(ctx context.Context, a *auth.Service, s *storage.Store, logger *slog.Logger) *backend {
	return &backend{
		ctx: ctx, auth: a, store: s, logger: logger,
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
	return user{name: owner, share: &share{ctx: b.ctx, view: &View{Store: b.store, Owner: owner}}}, nil
}

func (b *backend) Allow(remote net.IP) bool { return allowed(remote) }

type user struct {
	name  string
	share *share
}

func (u user) Username() string       { return u.name }
func (u user) Shares() []smbsrv.Share { return []smbsrv.Share{u.share} }

// share is an account's drive, read-only. smbsrv asks ReadOnly and CanWrite
// before writing; the write methods refuse anyway.
type share struct {
	ctx  context.Context
	view *View
}

func toSmbsrv(i Info) smbsrv.FileInfo {
	return smbsrv.FileInfo{Name: i.Name, Size: i.Size, IsDir: i.IsDir, ModTime: i.Modified, BirthTime: i.Created, ID: i.ID}
}

func (s *share) Name() string    { return ShareName }
func (s *share) Comment() string { return "Cloud OS" }

// Key names the account's files: locks, share modes and notifications of two
// accounts never meet.
func (s *share) Key() string                      { return "cloudos:" + s.view.Owner }
func (s *share) ReadOnly() bool                   { return true }
func (s *share) CanRead(string) bool              { return true }
func (s *share) CanWrite(string) bool             { return false }
func (s *share) HaveSpace(int64) bool             { return false }
func (s *share) FileChanged(string, int64, int64) {}

func (s *share) Stat(p string) (smbsrv.FileInfo, error) {
	i, err := s.view.Stat(s.ctx, p)
	return toSmbsrv(i), err
}

func (s *share) ReadDir(p string) ([]smbsrv.FileInfo, error) {
	list, err := s.view.List(s.ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]smbsrv.FileInfo, len(list))
	for i, c := range list {
		out[i] = toSmbsrv(c)
	}
	return out, nil
}

func (s *share) Open(p string, flag int) (smbsrv.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, ErrReadOnly
	}
	f, _, err := s.view.Open(s.ctx, p)
	if err != nil {
		return nil, err
	}
	return file{f}, nil
}

func (s *share) Mkdir(string) error                 { return ErrReadOnly }
func (s *share) Remove(string) error                { return ErrReadOnly }
func (s *share) Rename(string, string) error        { return ErrReadOnly }
func (s *share) SetModTime(string, time.Time) error { return ErrReadOnly }

// Usage is what the Finder shows as the drive's size: the disk's.
func (s *share) Usage() (total, free uint64) { return diskUsage(s.view.Store.Root) }

// file is a stored file opened for reading.
type file struct{ f *os.File }

func (f file) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, os.ErrInvalid
	}
	return f.f.ReadAt(b, off)
}
func (f file) WriteAt([]byte, int64) (int, error) { return 0, ErrReadOnly }
func (f file) Truncate(int64) error               { return ErrReadOnly }
func (f file) Sync() error                        { return nil }
func (f file) Close() error                       { return f.f.Close() }
