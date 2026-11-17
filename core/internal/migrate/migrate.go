// Package migrate applies the embedded SQL migrations with goose.
package migrate

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/C2-POLY/cloud-os/core/migrations"
)

// NewProvider returns a goose provider over the embedded migrations. It takes a
// Postgres advisory lock for the whole run, so two cores starting together do
// not apply the same migration twice.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("migration provider: %w", err)
	}
	return p, nil
}
