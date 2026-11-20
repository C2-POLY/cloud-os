package db_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/db"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// throwawayRole creates a role of its own for the test and drops it at the
// end: a role belongs to the whole server, and cloudos_smb may be in use by a
// running stack.
func throwawayRole(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck // crypto/rand.Read never returns an error since Go 1.24
	role := "cloudos_test_role_" + hex.EncodeToString(b)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DROP ROLE "+role); err != nil {
			t.Errorf("drop role: %v", err)
		}
	})
	return role
}

// connectAs opens a connection to the database at dbURL as role with password.
func connectAs(t *testing.T, dbURL, role, password string) error {
	t.Helper()
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, password)
	conn, err := pgx.Connect(context.Background(), u.String())
	if err != nil {
		return err
	}
	return conn.Close(context.Background())
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// The verifier is the one Postgres computes itself for the same password and salt.
func TestSCRAMVerifierMatchesPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // after the role is dropped
	role := throwawayRole(t, pool)
	const password = `p4ss "word" with 'quotes' \ and spaces`
	// Postgres hashes a plain password itself with password_encryption
	if _, err := pool.Exec(ctx, "SET password_encryption = 'scram-sha-256'"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ALTER ROLE "+role+" PASSWORD "+pgQuote(password)); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := pool.QueryRow(ctx, "SELECT rolpassword FROM pg_authid WHERE rolname = $1", role).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	// SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>
	head, _, ok := strings.Cut(strings.TrimPrefix(stored, "SCRAM-SHA-256$"), "$")
	iter, saltB64, ok2 := strings.Cut(head, ":")
	if !ok || !ok2 {
		t.Fatalf("unexpected verifier format %q", stored)
	}
	n, err := strconv.Atoi(iter)
	if err != nil {
		t.Fatal(err)
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.ScramVerifier(password, salt, n)
	if err != nil {
		t.Fatal(err)
	}
	if got != stored {
		t.Errorf("verifier\n got %s\nwant %s", got, stored)
	}
}

func pgQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func TestSetLoginPassword(t *testing.T) {
	ctx := context.Background()
	dbURL := testdb.URL(t)
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // after the role is dropped
	role := throwawayRole(t, pool)
	const password = "a-test-password-0123456789"

	if err := db.SetLoginPassword(ctx, pool, role, password); err != nil {
		t.Fatal(err)
	}
	var stored string
	var canLogin bool
	if err := pool.QueryRow(ctx, "SELECT rolpassword, rolcanlogin FROM pg_authid WHERE rolname = $1", role).Scan(&stored, &canLogin); err != nil {
		t.Fatal(err)
	}
	if !canLogin || !strings.HasPrefix(stored, "SCRAM-SHA-256$4096:") || strings.Contains(stored, password) {
		t.Fatalf("stored %q, can log in %v: want a SCRAM verifier, never the password", stored, canLogin)
	}
	if err := connectAs(t, dbURL, role, password); err != nil {
		t.Fatalf("log in with the password: %v", err)
	}
	if err := connectAs(t, dbURL, role, "a wrong password"); err == nil {
		t.Skip("this server does not check passwords from the test's address (trust): the rest cannot be tested here")
	} else if code := pgCode(err); code != "28P01" {
		t.Errorf("a wrong password: %v (code %s), want 28P01", err, code)
	}

	// A new password replaces the old one
	if err := db.SetLoginPassword(ctx, pool, role, password+"-new"); err != nil {
		t.Fatal(err)
	}
	if err := connectAs(t, dbURL, role, password); err == nil {
		t.Error("the old password still logs in")
	}
	if err := connectAs(t, dbURL, role, password+"-new"); err != nil {
		t.Errorf("log in with the new password: %v", err)
	}

	// No password: the role cannot log in at all
	if err := db.SetLoginPassword(ctx, pool, role, ""); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT rolcanlogin FROM pg_authid WHERE rolname = $1", role).Scan(&canLogin); err != nil {
		t.Fatal(err)
	}
	if canLogin {
		t.Error("an empty password left the role able to log in")
	}
	if err := connectAs(t, dbURL, role, password+"-new"); err == nil {
		t.Error("the role logs in after its password was removed")
	}
}

// A password that is not printable ASCII is refused, without being quoted
// in the error, and without changing the role.
func TestSetLoginPasswordRefusesNonASCII(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // after the role is dropped
	role := throwawayRole(t, pool)
	for _, pw := range []string{"mot-de-passe-été", "line\nbreak", "tab\there", "nul\x00byte"} {
		err := db.SetLoginPassword(ctx, pool, role, pw)
		if !errors.Is(err, db.ErrBadRolePassword) {
			t.Errorf("%q: %v, want db.ErrBadRolePassword", pw, err)
		}
		if err != nil && strings.Contains(err.Error(), pw) {
			t.Errorf("the error quotes the password: %v", err)
		}
	}
	var canLogin bool
	if err := pool.QueryRow(ctx, "SELECT rolcanlogin FROM pg_authid WHERE rolname = $1", role).Scan(&canLogin); err != nil {
		t.Fatal(err)
	}
	if canLogin {
		t.Error("a refused password let the role log in")
	}
}
