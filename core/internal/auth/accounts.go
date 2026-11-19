// Package auth holds the accounts, their passwords and their sessions.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// Roles.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

var (
	// ErrSetupDone is returned by Setup once any account exists.
	ErrSetupDone = errors.New("the box is already set up")
	// ErrAccountExists is returned when the name is taken.
	ErrAccountExists = errors.New("an account already has this name")
	// ErrNoAccount is returned when no account has the name.
	ErrNoAccount = errors.New("no such account")
	// ErrLastAdmin is returned when a change would leave no active admin.
	ErrLastAdmin = errors.New("the last active admin cannot be demoted or disabled")
	// ErrInvalid is returned for a bad name, display name or role.
	ErrInvalid = errors.New("invalid account")
)

// Account is a person who can sign in.
type Account struct {
	Name        string     `json:"name"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// IsAdmin reports whether the account is an admin.
func (a Account) IsAdmin() bool { return a.Role == RoleAdmin }

// Service manages accounts and sessions.
type Service struct {
	Pool    *pgxpool.Pool
	Limiter *Limiter
	// SessionTTL is how long a session lives without being used.
	SessionTTL time.Duration
	// OnSMBRevoke, when set by the SMB server, closes the SMB sessions of an
	// account. It runs after its SMB password is revoked or replaced, and
	// after the account is disabled.
	OnSMBRevoke func(name string)
}

// NewService returns a service with the default session lifetime and login limits.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{Pool: pool, Limiter: NewLimiter(5, time.Minute), SessionTTL: 30 * 24 * time.Hour}
}

const accountColumns = `name, display_name, role, disabled_at, created_at`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.Name, &a.DisplayName, &a.Role, &a.DisabledAt, &a.CreatedAt)
	return a, err
}

// lockAccounts serialises setup and admin changes, so the "no account yet" and
// "last admin" checks cannot race.
func lockAccounts(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('cloudos.accounts', 0))`)
	return err
}

func validate(name, display, role string) (string, string, error) {
	name, err := storage.CleanOwner(name)
	if err != nil {
		return "", "", fmt.Errorf("%w: the name must be 1 to 64 lower-case letters, digits, '-' or '_'", ErrInvalid)
	}
	display = strings.TrimSpace(display)
	if display == "" {
		display = name
	}
	if len(display) > 100 {
		return "", "", fmt.Errorf("%w: the display name is too long", ErrInvalid)
	}
	if role != RoleAdmin && role != RoleMember {
		return "", "", fmt.Errorf("%w: role must be admin or member", ErrInvalid)
	}
	return name, display, nil
}

// SetupNeeded reports whether no account exists yet.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts)`).Scan(&exists)
	return !exists, err
}

// Setup creates the first admin. It fails with ErrSetupDone once any account exists.
func (s *Service) Setup(ctx context.Context, name, display, password string) (Account, error) {
	return s.create(ctx, name, display, password, RoleAdmin, true)
}

// Create adds an account. Only an admin may call it (see RequireAdmin).
func (s *Service) Create(ctx context.Context, name, display, password, role string) (Account, error) {
	return s.create(ctx, name, display, password, role, false)
}

func (s *Service) create(ctx context.Context, name, display, password, role string, setup bool) (Account, error) {
	name, display, err := validate(name, display, role)
	if err != nil {
		return Account{}, err
	}
	if err := CheckPassword(password); err != nil {
		return Account{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Account{}, err
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if err := lockAccounts(ctx, tx); err != nil {
		return Account{}, err
	}
	if setup {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts)`).Scan(&exists); err != nil {
			return Account{}, err
		}
		if exists {
			return Account{}, ErrSetupDone
		}
	}
	a, err := scanAccount(tx.QueryRow(ctx,
		`INSERT INTO accounts (name, display_name, password_hash, role) VALUES ($1, $2, $3, $4)
		 RETURNING `+accountColumns, name, display, hash, role))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Account{}, ErrAccountExists
	}
	if err != nil {
		return Account{}, err
	}
	return a, tx.Commit(ctx)
}

// Get returns one account.
func (s *Service) Get(ctx context.Context, name string) (Account, error) {
	a, err := scanAccount(s.Pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE name = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNoAccount
	}
	return a, err
}

// List returns every account, by name.
func (s *Service) List(ctx context.Context) ([]Account, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Account, error) { return scanAccount(r) })
}

// Update changes an account's role and whether it is disabled. Disabling ends
// its sessions. It refuses to leave the box without an active admin.
func (s *Service) Update(ctx context.Context, name string, role *string, disabled *bool) (Account, error) {
	if role != nil && *role != RoleAdmin && *role != RoleMember {
		return Account{}, fmt.Errorf("%w: role must be admin or member", ErrInvalid)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if err := lockAccounts(ctx, tx); err != nil {
		return Account{}, err
	}
	cur, err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE name = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNoAccount
	}
	if err != nil {
		return Account{}, err
	}

	staysActiveAdmin := (role == nil && cur.IsAdmin() || role != nil && *role == RoleAdmin) &&
		(disabled == nil && cur.DisabledAt == nil || disabled != nil && !*disabled)
	if cur.IsAdmin() && cur.DisabledAt == nil && !staysActiveAdmin {
		var others int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM accounts WHERE role = 'admin' AND disabled_at IS NULL AND name <> $1`, name).Scan(&others); err != nil {
			return Account{}, err
		}
		if others == 0 {
			return Account{}, ErrLastAdmin
		}
	}

	if role != nil {
		if _, err := tx.Exec(ctx, `UPDATE accounts SET role = $2 WHERE name = $1`, name, *role); err != nil {
			return Account{}, err
		}
	}
	if disabled != nil {
		if *disabled {
			if _, err := tx.Exec(ctx, `UPDATE accounts SET disabled_at = coalesce(disabled_at, now()) WHERE name = $1`, name); err != nil {
				return Account{}, err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE account = $1`, name); err != nil {
				return Account{}, err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE accounts SET disabled_at = NULL WHERE name = $1`, name); err != nil {
			return Account{}, err
		}
	}
	out, err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE name = $1`, name))
	if err != nil {
		return Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	if disabled != nil && *disabled {
		s.smbRevoked(name) // its SMB sessions end with its web sessions
	}
	return out, nil
}

// ResetPassword sets a new password and ends every session of the account.
// It is for the box's terminal (`core reset-password`), not the API.
func (s *Service) ResetPassword(ctx context.Context, name, password string) error {
	if err := CheckPassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	tag, err := tx.Exec(ctx, `UPDATE accounts SET password_hash = $2 WHERE name = $1`, name, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoAccount
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE account = $1`, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
