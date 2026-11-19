package smbfs

import (
	"context"
	"errors"
	"log/slog"
	"net"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// Config describes the SMB server of the trial: one login, mapped to one
// account, sees that account's drive, read-only.
type Config struct {
	// Addr is the listen address, ":445" in the core's container.
	Addr string
	// User and Password are the trial's own credentials, read from
	// CLOUDOS_SMB_TRIAL_USER and CLOUDOS_SMB_TRIAL_PASSWORD. They are not the
	// account's password, which is kept as argon2id only (NTLM needs the NT hash).
	User     string
	Password string
	// Owner is the account whose drive the login sees.
	Owner  string
	Store  *storage.Store
	Logger *slog.Logger
}

// Server is a running SMB server.
type Server interface {
	Addr() net.Addr
	Close() error
}

// Start listens on cfg.Addr and serves cfg.Owner's drive, read-only, with
// smbsrv. The server stops when ctx ends or on Close.
func Start(ctx context.Context, cfg Config) (Server, error) {
	if cfg.User == "" || cfg.Password == "" {
		return nil, errors.New("smbfs: the trial needs CLOUDOS_SMB_TRIAL_USER and CLOUDOS_SMB_TRIAL_PASSWORD")
	}
	owner, err := storage.CleanOwner(cfg.Owner)
	if err != nil {
		return nil, err
	}
	if cfg.Store == nil {
		return nil, errors.New("smbfs: a store is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	cfg.Logger = cfg.Logger.With("smb", "smbsrv")
	return startSmbsrv(ctx, cfg, &View{Store: cfg.Store, Owner: owner})
}

// allowed is who may connect: SMB must never face the internet, so only
// private, loopback and link-local addresses (the Docker bridge is private).
func allowed(ip net.IP) bool {
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}
