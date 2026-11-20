package smbfs

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/tobychui/smbsrv"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// Config describes the SMB server: each account signs in with its own SMB
// password (auth.SMBHash, never the account's password) and sees its drive,
// which it writes through storage.
type Config struct {
	// Addr is the listen address, ":445" in the smb service's container.
	Addr string
	// Auth gives the NT hash of an account's SMB password and counts the
	// attempts, per account and address.
	Auth   *auth.Service
	Store  *storage.Store
	Logger *slog.Logger

	// loginDelay holds back the answer to a failed login; zero is smbsrv's
	// two seconds. Tests shorten it.
	loginDelay time.Duration
	// freeSpace is the disk's free space; nil is the disk of Store.Root.
	// Tests fill the disk with it.
	freeSpace func() uint64
}

// Server is a running SMB server.
type Server struct {
	srv     *smbsrv.Server
	backend *backend
	logger  *slog.Logger
}

// Start listens on cfg.Addr and serves every account's drive with smbsrv
// (core/third_party/smbsrv). The server stops when ctx ends or on Close.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		return nil, errors.New("smbfs: an auth service is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("smbfs: a store is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	logger := cfg.Logger.With("smb", "smbsrv")
	b := newBackend(ctx, cfg.Auth, cfg.Store, logger, cfg.freeSpace)
	srv, err := smbsrv.New(smbsrv.Config{
		Address:    cfg.Addr,
		ServerName: "CLOUDOS",
		Backend:    b,
		Logger:     logger,
		LoginDelay: cfg.loginDelay,
		// Change notifications and lease checks would list watched folders
		// on a timer. The share is not a PollableShare: smbsrv reports the
		// changes made over SMB itself, and grants no lease.
		NotifyPoll: -1,
	})
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, err
	}
	s := &Server{srv: srv, backend: b, logger: logger}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return s, nil
}

// Addr is the address the server listens on.
func (s *Server) Addr() net.Addr { return s.srv.Addr() }

// Close stops listening and drops every connection.
func (s *Server) Close() error { return s.srv.Close() }

// DropUser closes every connection the account is signed in on, and returns
// how many went. Its client has to sign in again, with the password it has now.
func (s *Server) DropUser(account string) int {
	s.backend.forget(account)
	n := s.srv.DropUser(account)
	if n > 0 {
		s.logger.Info("smb sessions closed", "account", account, "connections", n)
	}
	return n
}

// allowed is who may connect: SMB must never face the internet, so only
// private, loopback and link-local addresses (the Docker bridge is private).
func allowed(ip net.IP) bool {
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}
