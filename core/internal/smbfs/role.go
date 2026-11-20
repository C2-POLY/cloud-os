package smbfs

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBRole is the database role the smb service connects as (migrations 0026
// and 0027, docs/architecture/smb.md): it reads accounts (name, disabled_at),
// SMB passwords (account, nt_hash), the index and the migrations, writes
// smb_passwords.last_used_at, and writes exactly what storage needs for the
// SMB writes (writeGrants). The core sets its password at each start.
const DBRole = "cloudos_smb"

// writeGrants is everything the smb service's role may write, by table: the
// columns it may insert, the columns it may update, and whether it may delete
// rows. Migration 0027 grants the storage part, migration 0026 last_used_at.
// A column that is not here, or a table, must not be writable.
var writeGrants = map[string]struct {
	insert, update []string
	delete         bool
}{
	"files": {
		insert: []string{"owner", "path", "kind", "size", "mime", "sha256", "modified_at"},
		update: []string{"path", "size", "mime", "sha256", "modified_at", "indexed_at", "trash_id"},
		delete: true,
	},
	"file_versions": {insert: []string{"file_id", "owner", "size", "mime", "sha256", "modified_at"}, delete: true},
	"moves":         {insert: []string{"owner", "from_path", "to_path", "op", "trash_id", "version_id"}, delete: true},
	"trash":         {insert: []string{"owner", "path", "kind", "size", "expires_at"}, delete: true},
	"tasks":         {insert: []string{"kind", "payload", "priority", "max_attempts", "dedupe_key"}},
	"activity":      {insert: []string{"owner", "kind", "path"}},
	"smb_passwords": {update: []string{"last_used_at"}},
}

func granted(table, priv, column string) bool {
	g, ok := writeGrants[table]
	if !ok {
		return false
	}
	switch priv {
	case "INSERT":
		return slices.Contains(g.insert, column)
	case "UPDATE":
		return slices.Contains(g.update, column)
	case "DELETE":
		return g.delete
	}
	return false
}

// CheckRole refuses to serve with a database role that can do more than the
// smb service needs: a superuser, a role that may create roles or databases
// or bypass row security, a member of another role (which it could SET ROLE
// to), one that may read the accounts' password hashes,
// insert or update any column outside writeGrants, delete rows of any other
// table, truncate a table, or modify a sequence. Wiring the smb service with
// the core's role again stops it from starting, instead of quietly giving a
// fault in SMB the whole database: the accounts, the web sessions, the SMB
// passwords beyond last_used_at stay out of its reach.
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
	// A member of another role may SET ROLE to it, whatever its own grants
	var memberOf []string
	rows, err := pool.Query(ctx,
		`SELECT r.rolname FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid
		  WHERE m.member = (SELECT oid FROM pg_roles WHERE rolname = current_user) ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("smb: check the database role: %w", err)
	}
	if memberOf, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return fmt.Errorf("smb: check the database role: %w", err)
	}
	if len(memberOf) > 0 {
		too = append(too, "member of "+strings.Join(memberOf, ", "))
	}
	// Every write the role may make: per column for INSERT and UPDATE (a
	// grant on the whole table shows on each column), per table for DELETE
	// and TRUNCATE, and UPDATE on a sequence.
	// Each privilege function is only asked about the kind of relation it
	// takes (CASE: Postgres may evaluate a WHERE's conditions in any order).
	rows, err = pool.Query(ctx,
		`WITH rel AS (
		     SELECT c.oid, c.relname, c.relkind IN ('r', 'p', 'v', 'm', 'f') AS tab, c.relkind = 'S' AS seq
		       FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		      WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'
		 )
		 SELECT r.relname, p.priv, a.attname::text
		   FROM rel r
		   JOIN pg_attribute a ON a.attrelid = r.oid AND a.attnum > 0 AND NOT a.attisdropped
		   CROSS JOIN (VALUES ('INSERT'), ('UPDATE')) p(priv)
		  WHERE CASE WHEN r.tab THEN has_column_privilege(r.oid, a.attnum, p.priv) ELSE false END
		 UNION ALL
		 SELECT r.relname, p.priv, ''
		   FROM rel r CROSS JOIN (VALUES ('DELETE'), ('TRUNCATE')) p(priv)
		  WHERE CASE WHEN r.tab THEN has_table_privilege(r.oid, p.priv) ELSE false END
		 UNION ALL
		 SELECT r.relname, 'UPDATE', ''
		   FROM rel r
		  WHERE CASE WHEN r.seq THEN has_sequence_privilege(r.oid, 'UPDATE') ELSE false END
		 ORDER BY 1, 2, 3`)
	if err != nil {
		return fmt.Errorf("smb: check the database role: %w", err)
	}
	type write struct{ table, priv, column string }
	writes, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (write, error) {
		var w write
		return w, r.Scan(&w.table, &w.priv, &w.column)
	})
	if err != nil {
		return fmt.Errorf("smb: check the database role: %w", err)
	}
	var extra []string
	for _, w := range writes {
		if granted(w.table, w.priv, w.column) {
			continue
		}
		if w.column != "" {
			extra = append(extra, w.priv+" "+w.table+"."+w.column)
		} else {
			extra = append(extra, w.priv+" "+w.table)
		}
	}
	if len(extra) > 0 {
		too = append(too, "writes "+strings.Join(extra, ", "))
	}
	if len(too) > 0 {
		return fmt.Errorf("smb: the database role %q can do more than SMB needs (%s): connect the smb service as %s",
			user, strings.Join(too, "; "), DBRole)
	}
	return nil
}
