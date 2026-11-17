// Package share gives a guest without an account read access to one file or
// folder of an account, through a link with an expiry and an optional password.
//
// A link points at the id of the item's row in files, not at its path: it
// follows the item when it is renamed or moved, gives nothing while the item is
// in the trash, and is deleted with the item when the trash is purged. A new
// file put at the old path never inherits a link. Only the SHA-256 of a link's
// token is stored, as for sessions.
package share

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// Password length limits of a link, in characters.
const (
	MinPasswordLen = 6
	MaxPasswordLen = auth.MaxPasswordLen
)

// tokenLen is the length of a token: 32 random bytes in unpadded base64url.
const tokenLen = 43

// DefaultGrantTTL is how long the password of a link stays accepted in a guest's browser.
const DefaultGrantTTL = 24 * time.Hour

// KeepExpired is how long an expired link stays in its owner's list before it is deleted.
const KeepExpired = 30 * 24 * time.Hour

var (
	// ErrNotFound is the single answer to an unknown, expired or revoked link,
	// a link to an item in the trash, or a link of a disabled account, so a
	// guest learns nothing from it.
	ErrNotFound = errors.New("no such link")
	// ErrPasswordRequired is returned when a link has a password the guest has not given.
	ErrPasswordRequired = errors.New("password required")
	// ErrWrongPassword is returned for a wrong link password.
	ErrWrongPassword = errors.New("wrong password")
	// ErrInvalid is returned for an expiry in the past or a password outside the limits.
	ErrInvalid = errors.New("invalid link")
)

// Share is one link.
type Share struct {
	ID          int64     `json:"id"`
	Owner       string    `json:"-"`
	FileID      int64     `json:"-"`
	HasPassword bool      `json:"has_password"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`

	passwordHash string
}

// Service creates, lists, revokes and resolves links.
type Service struct {
	Pool  *pgxpool.Pool
	Files *storage.Store
	// Limiter counts wrong passwords per link and client.
	Limiter *auth.Limiter
	// GrantTTL is how long a right password opens a link in a guest's browser.
	GrantTTL time.Duration
}

// NewService returns a service with 5 password attempts per minute per link and client.
func NewService(pool *pgxpool.Pool, files *storage.Store) *Service {
	return &Service{Pool: pool, Files: files, Limiter: auth.NewLimiter(5, time.Minute), GrantTTL: DefaultGrantTTL}
}

func newToken() (token string, sum []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(token))
	return token, h[:], nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

const shareColumns = `s.id, s.owner, s.file_id, s.password_hash, s.expires_at, s.created_at`

func scanShare(row pgx.Row, extra ...any) (Share, error) {
	var s Share
	var hash *string
	err := row.Scan(append([]any{&s.ID, &s.Owner, &s.FileID, &hash, &s.ExpiresAt, &s.CreatedAt}, extra...)...)
	if hash != nil {
		s.HasPassword = true
		s.passwordHash = *hash
	}
	return s, err
}

// Create makes a link to owner's live file or folder at path, valid until
// expires, with a password when password is not empty. It returns the token,
// which is never stored and cannot be read again.
func (s *Service) Create(ctx context.Context, owner, path string, expires time.Time, password string) (Share, string, error) {
	f, err := s.Files.Lookup(ctx, owner, path)
	if err != nil {
		return Share{}, "", err
	}
	if !expires.After(time.Now()) {
		return Share{}, "", fmt.Errorf("%w: the expiry must be in the future", ErrInvalid)
	}
	var hash *string
	if password != "" {
		if n := utf8.RuneCountInString(password); n < MinPasswordLen || n > MaxPasswordLen {
			return Share{}, "", fmt.Errorf("%w: the password must have %d to %d characters", ErrInvalid, MinPasswordLen, MaxPasswordLen)
		}
		h, err := auth.HashPassword(password)
		if err != nil {
			return Share{}, "", err
		}
		hash = &h
	}
	token, sum, err := newToken()
	if err != nil {
		return Share{}, "", err
	}
	sh, err := scanShare(s.Pool.QueryRow(ctx,
		`INSERT INTO shares AS s (token_hash, owner, file_id, password_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+shareColumns,
		sum, f.Owner, f.ID, hash, expires))
	if err != nil {
		return Share{}, "", err
	}
	return sh, token, nil
}

// Listed is a link as its owner sees it, with the item's current place.
type Listed struct {
	Share
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size"`
	Expired bool   `json:"expired"`
	Trashed bool   `json:"trashed"`
}

// List returns owner's links, the newest first, expired ones included until
// they are cleaned.
func (s *Service) List(ctx context.Context, owner string) ([]Listed, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT `+shareColumns+`, f.path, f.kind, f.size, s.expires_at <= now(), f.trash_id IS NOT NULL
		 FROM shares s JOIN files f ON f.id = s.file_id
		 WHERE s.owner = $1
		 ORDER BY s.created_at DESC, s.id DESC`, owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Listed, error) {
		var l Listed
		var err error
		l.Share, err = scanShare(r, &l.Path, &l.Kind, &l.Size, &l.Expired, &l.Trashed)
		return l, err
	})
}

// Revoke deletes owner's link id, and with it every access opened by its password.
func (s *Service) Revoke(ctx context.Context, owner string, id int64) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM shares WHERE id = $1 AND owner = $2`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolve returns the link of token and its live item. Every reason a link
// gives nothing (unknown, expired, revoked, item in the trash or purged,
// account disabled) is the same ErrNotFound.
func (s *Service) Resolve(ctx context.Context, token string) (Share, storage.File, error) {
	if len(token) != tokenLen {
		return Share{}, storage.File{}, ErrNotFound
	}
	sh, err := scanShare(s.Pool.QueryRow(ctx,
		`SELECT `+shareColumns+` FROM shares s JOIN accounts a ON a.name = s.owner
		 WHERE s.token_hash = $1 AND s.expires_at > now() AND a.disabled_at IS NULL`, hashToken(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Share{}, storage.File{}, ErrNotFound
	}
	if err != nil {
		return Share{}, storage.File{}, err
	}
	f, err := s.Files.LookupID(ctx, sh.Owner, sh.FileID)
	if errors.Is(err, storage.ErrNotFound) {
		return Share{}, storage.File{}, ErrNotFound
	}
	if err != nil {
		return Share{}, storage.File{}, err
	}
	return sh, f, nil
}

// Unlock checks the password of a link and opens an access for the guest: a
// token kept in the guest's browser, valid for GrantTTL and never past the
// link's expiry. from identifies the client (its IP) for the attempt limit.
func (s *Service) Unlock(ctx context.Context, sh Share, password, from string) (string, time.Time, error) {
	if !sh.HasPassword {
		return "", time.Time{}, fmt.Errorf("%w: this link has no password", ErrInvalid)
	}
	key := fmt.Sprintf("share:%d|%s", sh.ID, from)
	if !s.Limiter.Allow(key) {
		return "", time.Time{}, auth.ErrThrottled
	}
	ok, err := auth.VerifyPassword(password, sh.passwordHash)
	if err != nil {
		return "", time.Time{}, err
	}
	if !ok {
		s.Limiter.Fail(key)
		return "", time.Time{}, ErrWrongPassword
	}
	s.Limiter.Reset(key)
	token, sum, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	var expires time.Time
	err = s.Pool.QueryRow(ctx,
		`INSERT INTO share_grants (token_hash, share_id, expires_at)
		 SELECT $1, id, least(now() + make_interval(secs => $3), expires_at) FROM shares WHERE id = $2
		 RETURNING expires_at`, sum, sh.ID, s.GrantTTL.Seconds()).Scan(&expires)
	if errors.Is(err, pgx.ErrNoRows) { // revoked meanwhile
		return "", time.Time{}, ErrNotFound
	}
	return token, expires, err
}

// Check returns nil when the guest may read through the link: it has no
// password, or grant is a valid access opened by its password.
func (s *Service) Check(ctx context.Context, sh Share, grant string) error {
	if !sh.HasPassword {
		return nil
	}
	if len(grant) != tokenLen {
		return ErrPasswordRequired
	}
	var ok bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM share_grants WHERE token_hash = $1 AND share_id = $2 AND expires_at > now())`,
		hashToken(grant), sh.ID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrPasswordRequired
	}
	return nil
}

// Within returns the stored path of rel, a path relative to the shared item
// ("" is the item itself). rel goes through storage.CleanPath, so it can never
// leave the item's subtree; inside a shared file, only "" exists.
func Within(item storage.File, rel string) (string, error) {
	if rel == "" {
		return item.Path, nil
	}
	if !item.IsDir() {
		return "", storage.ErrNotFound
	}
	clean, err := storage.CleanPath(rel)
	if err != nil {
		return "", err
	}
	return item.Path + "/" + clean, nil
}

// Relative returns the path of p as the guest sees it, relative to the shared item.
func Relative(item storage.File, p string) string {
	if p == item.Path {
		return ""
	}
	return strings.TrimPrefix(p, item.Path+"/")
}

// Clean deletes expired accesses, and links expired for longer than
// KeepExpired. It only deletes rows, never a file.
func (s *Service) Clean(ctx context.Context) (int, error) {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM share_grants WHERE expires_at <= now()`); err != nil {
		return 0, err
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM shares WHERE expires_at <= now() - make_interval(secs => $1)`, KeepExpired.Seconds())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
