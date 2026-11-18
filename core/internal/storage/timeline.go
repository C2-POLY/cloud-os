package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TimelineItem is a live file with the date it belongs to on the timeline.
type TimelineItem struct {
	File
	// At is the wall-clock date of the file in the asked time zone: when the
	// photo was taken (media_meta.taken_at, as the camera wrote it), else
	// when the file was last modified.
	At time.Time
	// Taken tells At comes from the photo's metadata.
	Taken bool
	// DurationMS is a video's length, when the workers have read it.
	DurationMS *int64
}

// TimelineFilter narrows the timeline.
type TimelineFilter string

const (
	TimelineAll   TimelineFilter = ""
	TimelineMedia TimelineFilter = "media" // images and videos
	TimelineDocs  TimelineFilter = "docs"  // everything else
)

// TimelineCursor is where a page ends: the next page starts strictly after it
// (older, or the same date with a lower path).
type TimelineCursor struct {
	At   time.Time
	Path string
}

// Timeline limits.
const (
	DefaultTimelinePage = 60
	MaxTimelinePage     = 200
)

// timelineBase selects the owner's live files with their timeline date ($1
// owner, $2 IANA time zone). It reads media_meta, the metadata the workers
// write; a file without a row there is dated by its modification.
func timelineBase(filter TimelineFilter) string {
	cols := make([]string, 0, 11)
	for _, c := range strings.Split(fileColumns, ", ") {
		cols = append(cols, "f."+c)
	}
	where := ""
	switch filter {
	case TimelineMedia:
		where = ` AND (f.mime LIKE 'image/%' OR f.mime LIKE 'video/%')`
	case TimelineDocs:
		where = ` AND NOT (f.mime LIKE 'image/%' OR f.mime LIKE 'video/%')`
	}
	return `SELECT ` + strings.Join(cols, ", ") + `,
	        COALESCE(m.taken_at, f.modified_at AT TIME ZONE $2) AS at, m.taken_at IS NOT NULL AS taken, m.duration_ms
	        FROM files f LEFT JOIN media_meta m ON m.sha256 = f.sha256
	        WHERE f.owner = $1 AND f.trash_id IS NULL AND f.kind = 'file'` + where
}

// Timeline returns a page of the owner's live files, newest first, after the
// cursor when one is given. tz is an IANA time zone name the caller checked.
func (s *Store) Timeline(ctx context.Context, owner, tz string, filter TimelineFilter, after *TimelineCursor, limit int) ([]TimelineItem, error) {
	return s.timeline(ctx, owner, tz, filter, after, limit, 0)
}

// AlbumTimeline is Timeline narrowed to the live files of an album, which the
// caller checked belongs to owner (internal/albums).
func (s *Store) AlbumTimeline(ctx context.Context, owner, tz string, album int64, after *TimelineCursor, limit int) ([]TimelineItem, error) {
	return s.timeline(ctx, owner, tz, TimelineAll, after, limit, album)
}

func (s *Store) timeline(ctx context.Context, owner, tz string, filter TimelineFilter, after *TimelineCursor, limit int, album int64) ([]TimelineItem, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultTimelinePage
	}
	limit = min(limit, MaxTimelinePage)
	args := []any{owner, tz, limit}
	base := timelineBase(filter)
	if album != 0 {
		args = append(args, album)
		base += ` AND f.id IN (SELECT file_id FROM album_items WHERE album_id = $4)`
	}
	cursor := ""
	if after != nil {
		n := len(args)
		args = append(args, after.At.Format("2006-01-02T15:04:05.999999"), after.Path)
		cursor = fmt.Sprintf(` WHERE (t.at, t.path) < ($%d::timestamp, $%d)`, n+1, n+2)
	}
	rows, err := s.Pool.Query(ctx, `SELECT * FROM (`+base+`) t`+cursor+`
		ORDER BY t.at DESC, t.path DESC LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelineItem{}
	for rows.Next() {
		var it TimelineItem
		f := &it.File
		if err := rows.Scan(&f.ID, &f.Owner, &f.Path, &f.Kind, &f.Size, &f.MIME, &f.SHA256, &f.CreatedAt, &f.ModifiedAt, &f.IndexedAt, &f.TrashID, &it.At, &it.Taken, &it.DurationMS); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MonthCount is how many files of the timeline fall in a month ("2026-11").
type MonthCount struct {
	Month string `json:"month"`
	Count int    `json:"count"`
}

// TimelineMonths counts the owner's live files per month of their timeline
// date, oldest month first.
func (s *Store) TimelineMonths(ctx context.Context, owner, tz string, filter TimelineFilter) ([]MonthCount, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT to_char(t.at, 'YYYY-MM'), count(*) FROM (`+timelineBase(filter)+`) t
		GROUP BY 1 ORDER BY 1`, owner, tz)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonthCount{}
	for rows.Next() {
		var m MonthCount
		if err := rows.Scan(&m.Month, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
