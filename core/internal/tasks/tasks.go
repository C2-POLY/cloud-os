// Package tasks is the core's side of the task queue shared with the workers.
// The SQL here is the contract: worker/src/worker/tasks.py runs the same
// statements, and both sides have the same tests.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Priorities: new files go before catch-up work.
const (
	PriorityBackfill = 0
	PriorityNew      = 10
)

// Defaults of the contract, the same on both sides.
const (
	DefaultLease       = 60 * time.Second
	DefaultMaxAttempts = 5
	BaseBackoff        = 10 * time.Second
	MaxBackoff         = time.Hour
	KeepDone           = 7 * 24 * time.Hour
)

// Task is one row of the queue.
type Task struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	Priority    int
	State       string
	Attempts    int
	MaxAttempts int
	RunAfter    time.Time
	LockedBy    *string
	LockedUntil *time.Time
	LastError   *string
	DedupeKey   *string
}

// Stats counts the tasks by state.
type Stats struct {
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Failed  int `json:"failed"`
}

// ErrLost is returned when a worker acts on a task it no longer holds (its
// lease expired and another worker took it).
var ErrLost = errors.New("the task is no longer held by this worker")

// Queue is the task queue in Postgres.
type Queue struct {
	Pool  *pgxpool.Pool
	Lease time.Duration
}

func (q *Queue) lease() time.Duration {
	if q.Lease > 0 {
		return q.Lease
	}
	return DefaultLease
}

const taskColumns = `id, kind, payload, priority, state, attempts, max_attempts, run_after, locked_by, locked_until, last_error, dedupe_key`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.Kind, &t.Payload, &t.Priority, &t.State, &t.Attempts, &t.MaxAttempts, &t.RunAfter, &t.LockedBy, &t.LockedUntil, &t.LastError, &t.DedupeKey)
	return t, err
}

// EnqueueOptions are the optional fields of a new task.
type EnqueueOptions struct {
	Priority    int
	MaxAttempts int    // 0 means DefaultMaxAttempts
	DedupeKey   string // when set, a pending task with this key makes Enqueue a no-op
}

// Enqueue adds a task. With a DedupeKey already queued or running, it adds
// nothing and returns (0, false, nil).
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, opt EnqueueOptions) (int64, bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, false, err
	}
	max := opt.MaxAttempts
	if max <= 0 {
		max = DefaultMaxAttempts
	}
	var dedupe *string
	if opt.DedupeKey != "" {
		dedupe = &opt.DedupeKey
	}
	var id int64
	err = q.Pool.QueryRow(ctx,
		`INSERT INTO tasks (kind, payload, priority, max_attempts, dedupe_key) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (dedupe_key) WHERE state IN ('queued', 'running') AND dedupe_key IS NOT NULL DO NOTHING
		 RETURNING id`, kind, body, opt.Priority, max, dedupe).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Claim takes the next ready task of one of kinds for worker, or returns
// (nil, nil) when there is none. FOR UPDATE SKIP LOCKED makes two workers
// never take the same task.
func (q *Queue) Claim(ctx context.Context, worker string, kinds []string) (*Task, error) {
	t, err := scanTask(q.Pool.QueryRow(ctx,
		`UPDATE tasks SET state = 'running', attempts = attempts + 1, locked_by = $1,
		     locked_until = now() + make_interval(secs => $3), updated_at = now()
		 WHERE id = (
		     SELECT id FROM tasks
		     WHERE state = 'queued' AND run_after <= now() AND kind = ANY($2)
		     ORDER BY priority DESC, run_after, id
		     FOR UPDATE SKIP LOCKED
		     LIMIT 1)
		 RETURNING `+taskColumns, worker, kinds, q.lease().Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Extend pushes back the lease of a task the worker still holds.
func (q *Queue) Extend(ctx context.Context, id int64, worker string) error {
	return q.held(q.Pool.Exec(ctx,
		`UPDATE tasks SET locked_until = now() + make_interval(secs => $3), updated_at = now()
		 WHERE id = $1 AND state = 'running' AND locked_by = $2`, id, worker, q.lease().Seconds()))
}

// Complete marks a held task done.
func (q *Queue) Complete(ctx context.Context, id int64, worker string) error {
	return q.held(q.Pool.Exec(ctx,
		`UPDATE tasks SET state = 'done', locked_by = NULL, locked_until = NULL, last_error = NULL,
		     finished_at = now(), updated_at = now()
		 WHERE id = $1 AND state = 'running' AND locked_by = $2`, id, worker))
}

// Fail records an error on a held task: it is queued again after a growing
// delay, or marked failed once it has used max_attempts.
func (q *Queue) Fail(ctx context.Context, id int64, worker, reason string) error {
	return q.fail(ctx, id, worker, reason, false)
}

// GiveUp records an error that another attempt cannot fix (an unreadable
// file): the task is failed at once, whatever its attempts left.
func (q *Queue) GiveUp(ctx context.Context, id int64, worker, reason string) error {
	return q.fail(ctx, id, worker, reason, true)
}

func (q *Queue) fail(ctx context.Context, id int64, worker, reason string, final bool) error {
	return q.held(q.Pool.Exec(ctx,
		`UPDATE tasks SET last_error = $3, locked_by = NULL, locked_until = NULL, updated_at = now(),
		     state = CASE WHEN attempts >= max_attempts OR $6 THEN 'failed' ELSE 'queued' END,
		     finished_at = CASE WHEN attempts >= max_attempts OR $6 THEN now() END,
		     run_after = now() + make_interval(secs => least($4 * power(2, attempts - 1), $5))
		 WHERE id = $1 AND state = 'running' AND locked_by = $2`,
		id, worker, reason, BaseBackoff.Seconds(), MaxBackoff.Seconds(), final))
}

func (q *Queue) held(tag interface{ RowsAffected() int64 }, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLost
	}
	return nil
}

// Reap returns to the queue the running tasks whose lease expired (their
// worker died), or marks them failed when they used max_attempts.
func (q *Queue) Reap(ctx context.Context) (int64, error) {
	tag, err := q.Pool.Exec(ctx,
		`UPDATE tasks SET locked_by = NULL, locked_until = NULL, updated_at = now(),
		     last_error = coalesce(last_error, 'the worker stopped answering'),
		     state = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
		     finished_at = CASE WHEN attempts >= max_attempts THEN now() END
		 WHERE state = 'running' AND locked_until < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Prune deletes done tasks older than KeepDone. These are queue rows only.
func (q *Queue) Prune(ctx context.Context) (int64, error) {
	tag, err := q.Pool.Exec(ctx, `DELETE FROM tasks WHERE state = 'done' AND finished_at < now() - make_interval(secs => $1)`, KeepDone.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Stats counts the tasks by state.
func (q *Queue) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	err := q.Pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE state = 'queued'), count(*) FILTER (WHERE state = 'running'), count(*) FILTER (WHERE state = 'failed') FROM tasks`).
		Scan(&s.Queued, &s.Running, &s.Failed)
	return s, err
}

// Get returns one task.
func (q *Queue) Get(ctx context.Context, id int64) (Task, error) {
	return scanTask(q.Pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, id))
}
