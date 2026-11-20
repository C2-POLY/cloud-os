package smbfs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Revocation crosses processes: the core changes an SMB password or disables
// an account, and the smb service, which holds the sessions, closes them.
// Postgres carries the news (NOTIFY on RevokeChannel); a sweep catches what a
// lost notification missed (docs/architecture/smb.md).

// RevokeChannel is the Postgres channel that names an account whose SMB
// sessions must close.
const RevokeChannel = "smb_revoke"

// SweepEvery is how often the smb service checks its sessions against the
// database, besides each notification.
const SweepEvery = time.Minute

// NotifyRevoke is the core's auth.Service.OnSMBRevoke: it asks the smb
// service, through Postgres, to close the account's sessions. A notification
// that cannot be sent is logged; the next sweep closes the sessions anyway.
func NotifyRevoke(pool *pgxpool.Pool, logger *slog.Logger) func(account string) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(account string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `SELECT pg_notify($1, $2)`, RevokeChannel, account); err != nil {
			logger.Error("smb: notify the smb service of a revocation; the sweep will close the sessions",
				"account", account, "err", err)
		}
	}
}

// WatchRevocations closes the sessions of an account as the core revokes its
// SMB password, gives it a new one or disables it, until ctx ends. It listens
// on RevokeChannel, and sweeps every `every` and each time it starts
// listening again, so a notification sent while it was not listening is not lost.
func (s *Server) WatchRevocations(ctx context.Context, pool *pgxpool.Pool, every time.Duration) {
	go s.sweepEvery(ctx, pool, every)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := s.listen(ctx, pool)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		s.logger.Warn("smb: listening for revocations stopped, listening again", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

// listen holds a connection of its own on RevokeChannel until it fails or
// ctx ends.
func (s *Server) listen(ctx context.Context, pool *pgxpool.Pool) error {
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// A listening connection never goes back to the pool
	conn := pc.Hijack()
	defer conn.Close(context.Background()) //nolint:errcheck // closing a connection we are done with
	if _, err := conn.Exec(ctx, "LISTEN "+RevokeChannel); err != nil {
		return err
	}
	// What was revoked while nobody listened
	if _, err := s.Sweep(ctx, pool); err != nil {
		s.logger.Error("smb: sweep sessions", "err", err)
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		s.DropUser(n.Payload)
	}
}

func (s *Server) sweepEvery(ctx context.Context, pool *pgxpool.Pool, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.Sweep(ctx, pool); err != nil && ctx.Err() == nil {
				s.logger.Error("smb: sweep sessions", "err", err)
			}
		}
	}
}

// Sweep closes the sessions of every account that is deleted, disabled, has
// no SMB password any more, or whose SMB password is not the one its sessions
// signed in with. It returns how many connections it closed.
func (s *Server) Sweep(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	live := map[string]bool{}
	for _, c := range s.srv.Clients() {
		live[strings.ToLower(c.Username)] = true
	}
	if len(live) == 0 {
		s.backend.sessionHashes(live)
		return 0, nil
	}
	signedWith := s.backend.sessionHashes(live)
	names := make([]string, 0, len(live))
	for name := range live {
		names = append(names, name)
	}
	rows, err := pool.Query(ctx,
		`SELECT n.name, p.nt_hash
		   FROM unnest($1::text[]) AS n(name)
		   LEFT JOIN accounts a ON a.name = n.name AND a.disabled_at IS NULL
		   LEFT JOIN smb_passwords p ON p.account = a.name`, names)
	if err != nil {
		return 0, err
	}
	current := map[string][]byte{}
	for rows.Next() {
		var name string
		var hash []byte
		if err := rows.Scan(&name, &hash); err != nil {
			rows.Close()
			return 0, err
		}
		current[name] = hash
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	dropped := 0
	for _, name := range names {
		hash := current[name]
		// A session that has just signed in may have no hash noted yet: it
		// is judged by the account and its password only, until the next sweep
		stale := len(hash) != 16
		for _, h := range signedWith[name] {
			if !bytes.Equal(h[:], hash) {
				stale = true
			}
		}
		if stale {
			dropped += s.DropUser(name)
		}
	}
	return dropped, nil
}
