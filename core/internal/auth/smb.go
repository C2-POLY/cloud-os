package auth

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/md4" //nolint:staticcheck // NTLM is defined on MD4; no other use
)

// An SMB password is drawn by the box, never chosen: SMBPasswordLen
// characters of smbAlphabet, which leaves out the look-alikes (I, O, l, o, 0,
// 1). 56 symbols give 5.8 bits each, about 139 bits in all: far beyond what
// the unsalted MD4 of an NT hash can be searched for.
const (
	SMBPasswordLen = 24
	smbAlphabet    = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
)

// SMBPasswordStatus says whether the account has an SMB password, never the
// password itself.
type SMBPasswordStatus struct {
	Exists     bool       `json:"exists"`
	CreatedAt  *time.Time `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// NTHash is the NT hash of a password, the MD4 of its UTF-16LE: the secret
// NTLMv2 (MS-NLMP, NTOWFv2) derives everything else from.
func NTHash(password string) [16]byte {
	units := utf16.Encode([]rune(password))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[2*i:], u)
	}
	h := md4.New()
	h.Write(b) //nolint:errcheck // a hash never fails to write
	var out [16]byte
	copy(out[:], h.Sum(nil))
	return out
}

// newSMBPassword draws SMBPasswordLen characters of smbAlphabet with
// crypto/rand, rejecting the bytes that would favour some characters.
func newSMBPassword() string {
	const limit = 256 - 256%len(smbAlphabet) // 224: four whole rounds of 56
	out := make([]byte, 0, SMBPasswordLen)
	buf := make([]byte, 2*SMBPasswordLen)
	for len(out) < SMBPasswordLen {
		rand.Read(buf) //nolint:errcheck // crypto/rand.Read never returns an error since Go 1.24
		for _, b := range buf {
			if int(b) < limit && len(out) < SMBPasswordLen {
				out = append(out, smbAlphabet[int(b)%len(smbAlphabet)])
			}
		}
	}
	return string(out)
}

// NewSMBPassword draws a new SMB password for an active account, stores only
// its NT hash and returns the password: it cannot be read again. A password
// the account had is replaced, and OnSMBRevoke closes the SMB sessions it opened.
func (s *Service) NewSMBPassword(ctx context.Context, name string) (string, SMBPasswordStatus, error) {
	password := newSMBPassword()
	nt := NTHash(password)
	var created time.Time
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO smb_passwords (account, nt_hash)
		 SELECT name, $2 FROM accounts WHERE name = $1 AND disabled_at IS NULL
		 ON CONFLICT (account) DO UPDATE SET nt_hash = EXCLUDED.nt_hash, created_at = now(), last_used_at = NULL
		 RETURNING created_at`, name, nt[:]).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", SMBPasswordStatus{}, ErrNoAccount
	}
	if err != nil {
		return "", SMBPasswordStatus{}, err
	}
	s.smbRevoked(name)
	return password, SMBPasswordStatus{Exists: true, CreatedAt: &created}, nil
}

// RevokeSMBPassword forgets the account's SMB password, if it has one, and
// OnSMBRevoke closes the SMB sessions it opened.
func (s *Service) RevokeSMBPassword(ctx context.Context, name string) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM smb_passwords WHERE account = $1`, name); err != nil {
		return err
	}
	s.smbRevoked(name)
	return nil
}

// SMBPasswordStatus tells whether the account has an SMB password, when it was
// drawn and when an SMB client last signed in with it.
func (s *Service) SMBPasswordStatus(ctx context.Context, name string) (SMBPasswordStatus, error) {
	var st SMBPasswordStatus
	err := s.Pool.QueryRow(ctx, `SELECT created_at, last_used_at FROM smb_passwords WHERE account = $1`, name).
		Scan(&st.CreatedAt, &st.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SMBPasswordStatus{}, nil
	}
	st.Exists = err == nil
	return st, err
}

// smbRevoked runs the OnSMBRevoke hook, if the SMB server set one.
func (s *Service) smbRevoked(name string) {
	if s.OnSMBRevoke != nil {
		s.OnSMBRevoke(name)
	}
}

// SMBCredential is what the SMB server checks an NTLMv2 response against.
type SMBCredential struct {
	// Account is the account's name, in lower case.
	Account string
	// NTHash is the NT hash of the account's SMB password: it opens SMB, so
	// it is never logged nor written out (see String).
	NTHash [16]byte `json:"-"`

	svc *Service
	key string
}

// String names the credential without its hash, for any log that prints it.
func (c SMBCredential) String() string { return "SMB credential of " + c.Account }

// GoString hides the hash from %#v too.
func (c SMBCredential) GoString() string { return c.String() }

// SMBHash gives the SMB server the NT hash of an account's SMB password, to
// check a client's NTLMv2 response. It refuses, with the single
// ErrBadCredentials, an unknown or disabled account and an account without an
// SMB password; it never reads the account's own password, which has no NT hash.
//
// The attempt counts as a failure for the Limiter, under the key
// "smb:<account>|<from>", apart from the web login's, until the server
// reports a valid response with Succeeded: a server that drops the connection
// half-way still spends an attempt. from identifies the client (its IP).
func (s *Service) SMBHash(ctx context.Context, name, from string) (SMBCredential, error) {
	name = strings.ToLower(name) // SMB clients may send the name as typed
	key := "smb:" + name + "|" + from
	if !s.Limiter.Allow(key) {
		return SMBCredential{}, ErrThrottled
	}
	s.Limiter.Fail(key)

	var nt []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT p.nt_hash FROM smb_passwords p JOIN accounts a ON a.name = p.account
		 WHERE p.account = $1 AND a.disabled_at IS NULL`, name).Scan(&nt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SMBCredential{}, ErrBadCredentials
	}
	if err != nil {
		return SMBCredential{}, err
	}
	c := SMBCredential{Account: name, svc: s, key: key}
	copy(c.NTHash[:], nt)
	return c, nil
}

// Succeeded tells the core that the client's NTLMv2 response matched: the
// failed attempts of its key are forgotten and the password's last use is
// noted. The server calls it only after checking the response.
func (c SMBCredential) Succeeded(ctx context.Context) {
	if c.svc == nil {
		return
	}
	c.svc.Limiter.Reset(c.key)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// Only the password that was checked: one drawn since then stays unused.
	if _, err := c.svc.Pool.Exec(ctx,
		`UPDATE smb_passwords SET last_used_at = now() WHERE account = $1 AND nt_hash = $2`,
		c.Account, c.NTHash[:]); err != nil {
		slog.Warn("smb: note the last use", "account", c.Account, "err", err)
	}
}
