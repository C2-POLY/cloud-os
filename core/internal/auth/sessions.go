package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrBadCredentials is the single answer to a wrong name, a wrong password
	// or a disabled account, so a failed login reveals nothing.
	ErrBadCredentials = errors.New("wrong name or password")
	// ErrThrottled is returned after too many failed logins.
	ErrThrottled = errors.New("too many failed attempts, try again in a minute")
	// ErrNoSession is returned for a missing, unknown or expired token.
	ErrNoSession = errors.New("not signed in")
)

// Login checks a name and password and opens a session. from identifies the
// client (its IP) for the failed-attempt limit.
func (s *Service) Login(ctx context.Context, name, password, from string) (string, Account, error) {
	a, err := s.CheckCredentials(ctx, name, password, from)
	if err != nil {
		return "", Account{}, err
	}
	token, err := s.NewSession(ctx, a.Name)
	return token, a, err
}

// CheckCredentials checks a name and password without opening a session, with
// the same failed-attempt limit and the same single error as Login.
func (s *Service) CheckCredentials(ctx context.Context, name, password, from string) (Account, error) {
	key := name + "|" + from
	if !s.Limiter.Allow(key) {
		return Account{}, ErrThrottled
	}

	var hash string
	var disabled *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT password_hash, disabled_at FROM accounts WHERE name = $1`, name).Scan(&hash, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		hash = dummyHash // spend the same time as for a real account
	} else if err != nil {
		return Account{}, err
	}
	ok, verr := VerifyPassword(password, hash)
	if err != nil || verr != nil || !ok || disabled != nil {
		s.Limiter.Fail(key)
		return Account{}, ErrBadCredentials
	}
	s.Limiter.Reset(key)
	return s.Get(ctx, name)
}

// NewSession opens a session for the account and returns its token. Only the
// token's SHA-256 is stored.
func (s *Service) NewSession(ctx context.Context, account string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, account, expires_at) VALUES ($1, $2, now() + make_interval(secs => $3))`,
		sum[:], account, s.SessionTTL.Seconds())
	return token, err
}

// Authenticate returns the account of a valid session and pushes its expiry
// back. A disabled account has no valid session.
func (s *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	if token == "" {
		return Account{}, ErrNoSession
	}
	sum := sha256.Sum256([]byte(token))
	a, err := scanAccount(s.Pool.QueryRow(ctx,
		`UPDATE sessions SET last_seen_at = now(), expires_at = now() + make_interval(secs => $2)
		 FROM accounts
		 WHERE sessions.token_hash = $1 AND sessions.expires_at > now()
		   AND accounts.name = sessions.account AND accounts.disabled_at IS NULL
		 RETURNING accounts.name, accounts.display_name, accounts.role, accounts.disabled_at, accounts.created_at`,
		sum[:], s.SessionTTL.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNoSession
	}
	return a, err
}

// Logout ends the session of the token.
func (s *Service) Logout(ctx context.Context, token string) error {
	sum := sha256.Sum256([]byte(token))
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, sum[:])
	return err
}

// Limiter counts failed logins per key within a window. It lives in memory:
// one core serves the box, and a restart only resets the counts.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
	now    func() time.Time
}

// NewLimiter allows max failures per key within window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, fails: map[string][]time.Time{}, now: time.Now}
}

func (l *Limiter) recent(key string) []time.Time {
	cut := l.now().Add(-l.window)
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = kept
	return kept
}

// Allow reports whether another attempt is allowed for key.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) < l.max
}

// Fail records a failed attempt for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.recent(key), l.now())
}

// Reset forgets the failures of key after a success.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
