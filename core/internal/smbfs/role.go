package smbfs

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBRole is the database role the smb service connects as (migration 0026,
// docs/architecture/smb.md): it reads accounts (name, disabled_at), SMB
// passwords (account, nt_hash), the index and the migrations, and writes only
// smb_passwords.last_used_at. The core sets its password at each start.
const DBRole = "cloudos_smb"

// CheckRole refuses to serve with a database role that can do more than the
// smb service needs: a superuser, a role that may create roles or databases
// or bypass row security, a role that may write any table or sequence as a
// whole, or read the accounts' password hashes. Wiring the smb service with
// the core's role again stops it from starting, instead of quietly giving a
// fault in SMB the whole database. Column grants (last_used_at) are not table
// writes and pass.
func CheckRole(ctx context.Context, pool *pgxpool.Pool) error {
	var (
		user                                   string
		super, createRole, createDB, bypassRLS bool
		passwordHash                           bool
	)
	err := pool.QueryRow(ctx,
		`SELECT rolname, rolsuper, rolcreaterole, rolcreatedb, rolbypassrls,
		        has_column_privilege('accounts', 'password_hash', 'SELECT')
		   FROM pg_roles WHERE rolname = current_user`).
		Scan(&user, &super, &createRole, &createDB, &bypassRLS, &passwordHash)
	if err != nil {
		return fmt.Errorf("smb: check the database role: %w", err)
	}
	var too []string
	for _, attr := range []struct {
		name string
		on   bool
	}{{"SUPERUSER", super}, {"CREATEROLE", createRole}, {"CREATEDB", createDB}, {"BYPASSRLS", bypassRLS}} {
		if attr.on {
			too = append(too, attr.name)
		}
	}
	if passwordHash {
		too = append(too, "SELECT on accounts.password_hash")
	}
	rows, err := pool.Query(ctx,
		`SELECT c.relname
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'
		    AND ((c.relkind IN ('r', 'p', 'v', 'm', 'f')
		          AND has_table_privilege(c.oid, 'INSERT,UPDATE,DELETE,TRUNCATE'))
		      OR (c.relkind = 'S' AND has_sequence_privilege(c.oid, 'UPDATE')))
		  ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("smb: check the database role: %w", err)
	}
	writable, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("smb: check the database role: %w", err)
	}
	if len(writable) > 0 {
		too = append(too, "writes to "+strings.Join(writable, ", "))
	}
	if len(too) > 0 {
		return fmt.Errorf("smb: the database role %q can do more than SMB needs (%s): connect the smb service as %s",
			user, strings.Join(too, "; "), DBRole)
	}
	return nil
}
