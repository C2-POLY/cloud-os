package boxstatus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// PGStore reads the status from the database: the workers' heartbeat,
// backup_runs (which the core owns and only reads; the backup service writes
// it) and the trash.
type PGStore struct {
	Pool *pgxpool.Pool
}

// WorkerSeen is the newest heartbeat of any worker.
func (s *PGStore) WorkerSeen(ctx context.Context) (*time.Time, error) {
	var seen *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT max(seen_at) FROM worker_heartbeat`).Scan(&seen)
	return seen, err
}

// Backups reads the newest run of each kind, the newest backup that
// succeeded, the first backup run, and the newest measure of the backup disk.
func (s *PGStore) Backups(ctx context.Context) (Backup, *Disk, error) {
	var b Backup
	rows, err := s.Pool.Query(ctx,
		`SELECT DISTINCT ON (kind) kind, started_at, finished_at, ok, detail, error
		   FROM backup_runs ORDER BY kind, id DESC`)
	if err != nil {
		return b, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			kind   string
			r      Run
			detail map[string]any
			msg    *string
		)
		if err := rows.Scan(&kind, &r.StartedAt, &r.FinishedAt, &r.OK, &detail, &msg); err != nil {
			return b, nil, err
		}
		if msg != nil {
			r.Error = *msg
		}
		switch kind {
		case "backup":
			r.Files, r.Bytes = number(detail, "files"), number(detail, "bytes")
			r.DataAdded, r.DurationMS = number(detail, "data_added"), number(detail, "duration_ms")
			b.Last.Backup = &r
		case "copy":
			b.Last.Copy = &r
		case "check":
			b.Last.Check = &r
		case "restore_test":
			r.Sample = number(detail, "sample")
			// The paths that differ stay in backup_runs: the panel shows how many.
			if list, ok := detail["mismatches"].([]any); ok {
				n := int64(len(list))
				r.Mismatches = &n
			}
			b.Last.RestoreTest = &r
		}
	}
	if err := rows.Err(); err != nil {
		return b, nil, err
	}

	err = s.Pool.QueryRow(ctx,
		`SELECT max(finished_at) FILTER (WHERE kind = 'backup' AND ok),
		        min(started_at) FILTER (WHERE kind = 'backup')
		   FROM backup_runs`).Scan(&b.LastOK, &b.FirstAt)
	if err != nil {
		return b, nil, err
	}

	// The backup service measures its disk at the end of each run that found
	// the repository (disk_free, disk_total).
	var (
		free, total int64
		at          time.Time
	)
	err = s.Pool.QueryRow(ctx,
		`SELECT (detail ->> 'disk_free')::bigint, (detail ->> 'disk_total')::bigint, coalesce(finished_at, started_at)
		   FROM backup_runs
		  WHERE jsonb_typeof(detail -> 'disk_free') = 'number' AND jsonb_typeof(detail -> 'disk_total') = 'number'
		  ORDER BY id DESC LIMIT 1`).Scan(&free, &total, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, nil, nil
	}
	if err != nil {
		return b, nil, fmt.Errorf("the backup disk: %w", err)
	}
	if free < 0 || total <= 0 {
		return b, nil, nil
	}
	return b, &Disk{Total: uint64(total), Free: uint64(free), At: &at}, nil
}

// KeptInTrash counts the trash items that are SMB writes the box could not
// store (storage.RescueReason), put there since then.
func (s *PGStore) KeptInTrash(ctx context.Context, since time.Time) (int, *time.Time, error) {
	var (
		n    int
		last *time.Time
	)
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*), max(deleted_at) FROM trash WHERE reason = $1 AND deleted_at > $2`,
		storage.RescueReason, since).Scan(&n, &last)
	return n, last, err
}

// number reads a whole number of a run's detail; nil when absent.
func number(detail map[string]any, key string) *int64 {
	f, ok := detail[key].(float64)
	if !ok || f != math.Trunc(f) {
		return nil
	}
	n := int64(f)
	return &n
}
