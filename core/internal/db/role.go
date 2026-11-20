package db

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scramIterations is Postgres's own default (scram_iterations).
const scramIterations = 4096

// ErrBadRolePassword is a password SetLoginPassword refuses. It never quotes
// the password.
var ErrBadRolePassword = errors.New("db: a role password must be printable ASCII")

// SetLoginPassword lets role log in with password, or, when password is empty,
// stops it logging in (NOLOGIN, no password). Postgres receives only the
// password's SCRAM-SHA-256 verifier, computed here: the password itself never
// reaches the server, its statement log nor its error log. The password must
// be printable ASCII, so that no SASLprep normalisation can make the client
// and the server disagree on it.
func SetLoginPassword(ctx context.Context, pool *pgxpool.Pool, role, password string) error {
	ident := pgx.Identifier{role}.Sanitize()
	if password == "" {
		_, err := pool.Exec(ctx, "ALTER ROLE "+ident+" NOLOGIN PASSWORD NULL")
		return err
	}
	for i := 0; i < len(password); i++ {
		if password[i] < 0x20 || password[i] > 0x7e {
			return ErrBadRolePassword
		}
	}
	salt := make([]byte, 16)
	rand.Read(salt) //nolint:errcheck // crypto/rand.Read never returns an error since Go 1.24
	verifier, err := scramVerifier(password, salt, scramIterations)
	if err != nil {
		return err
	}
	// The verifier holds only base64, digits, '$' and ':': nothing to escape
	_, err = pool.Exec(ctx, "ALTER ROLE "+ident+" LOGIN PASSWORD '"+verifier+"'")
	return err
}

// scramVerifier is what Postgres stores for a SCRAM-SHA-256 password
// (RFC 5802, RFC 7677): SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>.
func scramVerifier(password string, salt []byte, iterations int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("db: derive the role password: %w", err)
	}
	mac := func(msg string) []byte {
		h := hmac.New(sha256.New, salted)
		h.Write([]byte(msg)) //nolint:errcheck // a hash never fails to write
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac("Client Key"))
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s",
		iterations, b64(salt), b64(stored[:]), b64(mac("Server Key"))), nil
}
