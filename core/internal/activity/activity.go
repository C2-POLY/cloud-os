// Package activity keeps what happened in each account (files added,
// replaced, trashed, restored, share links opened) for the desktop. It holds
// rows only, never files, and forgets them after Keep.
package activity

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Kinds of events.
const (
	Added       = "added"
	Replaced    = "replaced"
	Trashed     = "trashed"
	Restored    = "restored"
	ShareOpened = "share_opened"
)

// Keep is how long an event is kept.
const Keep = 90 * 24 * time.Hour

// Group is a run of events of one kind, on one day, in one place: "46 files
// added to Photos today", "the link Vacances opened 3 times yesterday".
type Group struct {
	Day   string    `json:"day"` // YYYY-MM-DD, in the asked time zone
	Kind  string    `json:"kind"`
	Place string    `json:"place"`          // the folder ("" is the root), or the shared item's path
	Name  string    `json:"name,omitempty"` // the one file's path when Count is 1
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// MaxGroups caps one listing.
const MaxGroups = 50

// Log is the journal in Postgres.
type Log struct {
	Pool *pgxpool.Pool
}

// Record adds an event. The journal is a convenience: a failure is logged and
// never fails what the event describes.
func (l *Log) Record(ctx context.Context, owner, kind, path string) {
	if l == nil || l.Pool == nil {
		return
	}
	if _, err := l.Pool.Exec(context.WithoutCancel(ctx),
		`INSERT INTO activity (owner, kind, path) VALUES ($1, $2, $3)`, owner, kind, path); err != nil {
		slog.Error("record activity", "owner", owner, "kind", kind, "err", err)
	}
}

// Groups returns owner's events of the last days days, grouped by day (in the
// IANA time zone tz), kind and place, the most recent group first.
func (l *Log) Groups(ctx context.Context, owner string, days int, tz string, limit int) ([]Group, error) {
	if limit <= 0 || limit > MaxGroups {
		limit = MaxGroups
	}
	rows, err := l.Pool.Query(ctx, `
		SELECT to_char((created_at AT TIME ZONE $3)::date, 'YYYY-MM-DD') AS day, kind,
		       CASE WHEN kind = 'share_opened' THEN path
		            WHEN position('/' in path) > 0 THEN regexp_replace(path, '/[^/]*$', '')
		            ELSE '' END AS place,
		       CASE WHEN count(*) = 1 THEN min(path) ELSE '' END,
		       count(*), max(created_at)
		FROM activity
		WHERE owner = $1 AND created_at >= now() - make_interval(days => $2)
		GROUP BY 1, 2, 3
		ORDER BY max(created_at) DESC
		LIMIT $4`, owner, days, tz, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.Day, &g.Kind, &g.Place, &g.Name, &g.Count, &g.Last); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Prune forgets the events older than Keep.
func (l *Log) Prune(ctx context.Context) (int64, error) {
	tag, err := l.Pool.Exec(ctx, `DELETE FROM activity WHERE created_at < now() - make_interval(secs => $1)`, Keep.Seconds())
	return tag.RowsAffected(), err
}
