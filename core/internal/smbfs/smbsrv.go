package smbfs

import (
	"context"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tobychui/smbsrv"
)

// The adapter for github.com/tobychui/smbsrv (MIT), the library that speaks
// SMB (docs/decisions/006-smb.md).

func startSmbsrv(ctx context.Context, cfg Config, view *View) (Server, error) {
	b := &smbsrvBackend{cfg: cfg, hash: smbsrv.NTHash(cfg.Password), share: &smbsrvShare{ctx: ctx, view: view}}
	srv, err := smbsrv.New(smbsrv.Config{
		Address:    cfg.Addr,
		ServerName: "CLOUDOS",
		Backend:    b,
		Logger:     cfg.Logger,
		// Change notifications and lease checks list watched folders on a
		// timer; a read-only trial has nothing to watch for.
		NotifyPoll: -1,
	})
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, err
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return srv, nil
}

// smbsrvBackend has one login, the trial's, which sees one share.
type smbsrvBackend struct {
	cfg   Config
	hash  []byte
	share *smbsrvShare
}

func (b *smbsrvBackend) LookupHash(username string) (string, []byte, bool) {
	if !strings.EqualFold(username, b.cfg.User) {
		return "", nil, false
	}
	return b.cfg.User, b.hash, true
}

func (b *smbsrvBackend) Login(username string) (smbsrv.UserView, error) {
	if !strings.EqualFold(username, b.cfg.User) {
		return nil, os.ErrPermission
	}
	return smbsrvUser{name: b.cfg.User, share: b.share}, nil
}

func (b *smbsrvBackend) Allow(remote net.IP) bool { return allowed(remote) }

func (b *smbsrvBackend) AuthLog(username, remote string, ok bool) time.Duration {
	b.cfg.Logger.Info("smb login", "user", username, "remote", remote, "ok", ok)
	return 0 // a failed login waits smbsrv's default delay
}

type smbsrvUser struct {
	name  string
	share *smbsrvShare
}

func (u smbsrvUser) Username() string       { return u.name }
func (u smbsrvUser) Shares() []smbsrv.Share { return []smbsrv.Share{u.share} }

// smbsrvShare is the drive, read-only. smbsrv asks ReadOnly and CanWrite
// before writing; the write methods refuse anyway.
type smbsrvShare struct {
	ctx  context.Context
	view *View
}

func toSmbsrv(i Info) smbsrv.FileInfo {
	return smbsrv.FileInfo{Name: i.Name, Size: i.Size, IsDir: i.IsDir, ModTime: i.Modified, BirthTime: i.Created, ID: i.ID}
}

func (s *smbsrvShare) Name() string                     { return ShareName }
func (s *smbsrvShare) Comment() string                  { return "Cloud OS" }
func (s *smbsrvShare) Key() string                      { return "cloudos:" + s.view.Owner }
func (s *smbsrvShare) ReadOnly() bool                   { return true }
func (s *smbsrvShare) CanRead(string) bool              { return true }
func (s *smbsrvShare) CanWrite(string) bool             { return false }
func (s *smbsrvShare) HaveSpace(int64) bool             { return false }
func (s *smbsrvShare) FileChanged(string, int64, int64) {}

func (s *smbsrvShare) Stat(p string) (smbsrv.FileInfo, error) {
	i, err := s.view.Stat(s.ctx, p)
	return toSmbsrv(i), err
}

func (s *smbsrvShare) ReadDir(p string) ([]smbsrv.FileInfo, error) {
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

func (s *smbsrvShare) Open(p string, flag int) (smbsrv.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, ErrReadOnly
	}
	f, _, err := s.view.Open(s.ctx, p)
	if err != nil {
		return nil, err
	}
	return smbsrvFile{f}, nil
}

func (s *smbsrvShare) Mkdir(string) error                 { return ErrReadOnly }
func (s *smbsrvShare) Remove(string) error                { return ErrReadOnly }
func (s *smbsrvShare) Rename(string, string) error        { return ErrReadOnly }
func (s *smbsrvShare) SetModTime(string, time.Time) error { return ErrReadOnly }

// Usage is what the Finder shows as the drive's size: the disk's.
func (s *smbsrvShare) Usage() (total, free uint64) { return diskUsage(s.view.Store.Root) }

// smbsrvFile is a stored file opened for reading.
type smbsrvFile struct{ f *os.File }

func (f smbsrvFile) ReadAt(b []byte, off int64) (int, error) { return f.f.ReadAt(b, off) }
func (f smbsrvFile) WriteAt([]byte, int64) (int, error)      { return 0, ErrReadOnly }
func (f smbsrvFile) Truncate(int64) error                    { return ErrReadOnly }
func (f smbsrvFile) Sync() error                             { return nil }
func (f smbsrvFile) Close() error                            { return f.f.Close() }
