package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/edge"
)

// BasicTTL is how long a checked Basic login is trusted before the password
// is checked again. Finder sends credentials on every request; argon2id costs
// ~100 ms, so each one cannot be checked from scratch.
const BasicTTL = 10 * time.Minute

// BasicAuth authenticates WebDAV clients (Finder, Explorer), which only send
// HTTP Basic. A checked name and password are trusted for BasicTTL, keyed by
// the SHA-256 of both, never the password itself; the account is still read
// on every request, so a disabled account is refused at once.
type BasicAuth struct {
	svc   *Service
	realm string
	ttl   time.Duration
	mu    sync.Mutex
	seen  map[[32]byte]time.Time
	now   func() time.Time
}

// NewBasicAuth returns a Basic authenticator for the realm the client shows.
func (s *Service) NewBasicAuth(realm string) *BasicAuth {
	return &BasicAuth{svc: s, realm: realm, ttl: BasicTTL, seen: map[[32]byte]time.Time{}, now: time.Now}
}

func credentialKey(name, password string) [32]byte {
	return sha256.Sum256([]byte(name + "\x00" + password))
}

func (b *BasicAuth) trusted(key [32]byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.seen[key]
	if ok && b.now().Before(until) {
		return true
	}
	delete(b.seen, key)
	return false
}

func (b *BasicAuth) remember(key [32]byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for k, until := range b.seen { // keep the map small
		if now.After(until) {
			delete(b.seen, k)
		}
	}
	b.seen[key] = now.Add(b.ttl)
}

func (b *BasicAuth) authenticate(ctx context.Context, r *http.Request) (Account, error) {
	name, password, ok := r.BasicAuth()
	if !ok {
		return Account{}, ErrNoSession
	}
	key := credentialKey(name, password)
	if b.trusted(key) {
		a, err := b.svc.Get(ctx, name)
		if err == nil && a.DisabledAt == nil {
			return a, nil
		}
		b.mu.Lock()
		delete(b.seen, key)
		b.mu.Unlock()
		return Account{}, ErrBadCredentials
	}
	// The client's address behind the trusted proxy: the attempt limit counts by it.
	a, err := b.svc.CheckCredentials(ctx, name, password, edge.ClientIP(r))
	if err != nil {
		return Account{}, err
	}
	b.remember(key)
	return a, nil
}

// Require answers 401 with a Basic challenge unless the request carries valid
// credentials, and otherwise puts the account in the request's context.
func (b *BasicAuth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := b.authenticate(r.Context(), r)
		switch {
		case err == nil:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, a)))
		case errors.Is(err, ErrThrottled):
			http.Error(w, err.Error(), http.StatusTooManyRequests)
		case errors.Is(err, ErrNoSession), errors.Is(err, ErrBadCredentials):
			w.Header().Set("WWW-Authenticate", `Basic realm="`+b.realm+`", charset="UTF-8"`)
			http.Error(w, "sign in with your Cloud OS account", http.StatusUnauthorized)
		default:
			slog.Error("basic auth", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})
}
