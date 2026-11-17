// Package db opens the PostgreSQL pool and answers the health queries.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Worker is a worker that wrote a heartbeat.
type Worker struct {
	ID     string    `json:"id"`
	SeenAt time.Time `json:"seen_at"`
}

// Store reads the database state for the health endpoint.
type Store struct {
	Pool       *pgxpool.Pool
	Migrations *goose.Provider
}

// Open connects to url and checks the connection.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// SQL returns a database/sql handle sharing the pool, for goose.
func SQL(pool *pgxpool.Pool) *sql.DB {
	return stdlib.OpenDBFromPool(pool)
}

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error {
	return s.Pool.Ping(ctx)
}

// MigrationVersion returns the last applied migration.
func (s *Store) MigrationVersion(ctx context.Context) (int64, error) {
	return s.Migrations.GetDBVersion(ctx)
}

// RecentWorkers returns the workers seen within the last window, newest first.
func (s *Store) RecentWorkers(ctx context.Context, window time.Duration) ([]Worker, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT worker_id, seen_at FROM worker_heartbeat
		 WHERE seen_at > now() - make_interval(secs => $1)
		 ORDER BY seen_at DESC`, window.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workers := []Worker{}
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.ID, &w.SeenAt); err != nil {
			return nil, err
		}
		workers = append(workers, w)
	}
	return workers, rows.Err()
}
