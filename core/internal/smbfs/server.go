package smbfs

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/tobychui/smbsrv"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/edge"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// Config describes the SMB server: each account signs in with its own SMB
// password (auth.SMBHash, never the account's password) and sees its drive,
// read-only.
type Config struct {
	// Addr is the listen address, ":445" in the smb service's container.
	Addr string
	// Auth gives the NT hash of an account's SMB password and counts the
	// attempts, per account and address.
	Auth   *auth.Service
	Store  *storage.Store
	Logger *slog.Logger
	// Proxies are the proxies that name their client in a PROXY protocol
	// header: the tailscale service, which relays the tailnet's SMB here
	// (docs/architecture/box.md, « Accès »). Their connections must start
	// with one; anyone else's are taken as they are. Nil reads no header.
	Proxies *edge.Proxies

	// loginDelay holds back the answer to a failed login; zero is smbsrv's
	// two seconds. Tests shorten it.
	loginDelay time.Duration
}

// Server is a running SMB server.
type Server struct {
	srv     *smbsrv.Server
	backend *backend
	logger  *slog.Logger
}

// Start listens on cfg.Addr and serves every account's drive, read-only, with
// smbsrv (core/third_party/smbsrv). The server stops when ctx ends or on Close.
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
	b := newBackend(ctx, cfg.Auth, cfg.Store, logger)
	var listener net.Listener
	if cfg.Proxies != nil {
		l, err := net.Listen("tcp", cfg.Addr)
		if err != nil {
			return nil, err
		}
		listener = edge.NewProxyListener(l, cfg.Proxies, logger)
	}
	srv, err := smbsrv.New(smbsrv.Config{
		Address:    cfg.Addr,
		Listener:   listener,
		ServerName: "CLOUDOS",
		Backend:    b,
		Logger:     logger,
		LoginDelay: cfg.loginDelay,
		// Change notifications and lease checks list watched folders on a
		// timer; a read-only drive has nothing to watch for.
		NotifyPoll: -1,
	})
	if err != nil {
		if listener != nil {
			listener.Close()
		}
		return nil, err
	}
	if err := srv.Start(); err != nil {
		if listener != nil {
			listener.Close()
		}
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

// tailnet is the range Tailscale gives its devices (100.64.0.0/10, shared
// address space); its IPv6 range, fd7a:115c:a1e0::/48, is already private.
var tailnet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// allowed is who may connect: SMB must never face the internet, so only
// private, loopback and link-local addresses (the Docker bridge is private),
// and the tailnet's devices, which the tailscale service names in its PROXY
// header. A public address in that header is refused all the same: Funnel
// never opens SMB.
func allowed(ip net.IP) bool {
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || tailnet.Contains(ip))
}
