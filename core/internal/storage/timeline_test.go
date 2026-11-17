package storage

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func putModified(t *testing.T, s *Store, p, content string, at time.Time) {
	t.Helper()
	opt := NoExpectations()
	opt.ModTime = at
	if _, err := s.Put(context.Background(), "enzo", p, strings.NewReader(content), opt); err != nil {
		t.Fatal(err)
	}
}

// taken writes what a worker writes for a photo: when it was taken.
func taken(t *testing.T, s *Store, content string, at string) {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	if _, err := s.Pool.Exec(context.Background(), `INSERT INTO media_meta (sha256, taken_at) VALUES ($1, $2::timestamp)`, sum[:], at); err != nil {
		t.Fatal(err)
	}
}

func timelinePaths(items []TimelineItem) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Path + "@" + it.At.Format("2006-01-02T15:04")
	}
	return strings.Join(out, " ")
}

func TestTimelineDatesPhotosByWhenTheyWereTaken(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	modified := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC) // 10:30 in Paris
	putModified(t, s, "Photos/plage.jpg", "plage", modified)
	taken(t, s, "plage", "2024-07-14 18:03:22")
	putModified(t, s, "Photos/sans-exif.png", "png", modified.Add(-time.Hour))
	putModified(t, s, "Documents/bail.pdf", "bail", time.Date(2023, 3, 1, 23, 30, 0, 0, time.UTC)) // 2 March in Paris
	putModified(t, s, "jeté.txt", "x", modified)
	if _, err := s.Trash(ctx, "enzo", "jeté.txt"); err != nil {
		t.Fatal(err)
	}

	items, err := s.Timeline(ctx, "enzo", "Europe/Paris", TimelineAll, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := "Photos/sans-exif.png@2026-11-17T09:30 Photos/plage.jpg@2024-07-14T18:03 Documents/bail.pdf@2023-03-02T00:30"
	if got := timelinePaths(items); got != want {
		t.Errorf("timeline\n got %s\nwant %s", got, want)
	}
	if !items[1].Taken || items[0].Taken {
		t.Errorf("taken flags: %+v", items)
	}

	media, _ := s.Timeline(ctx, "enzo", "UTC", TimelineMedia, nil, 0)
	docs, _ := s.Timeline(ctx, "enzo", "UTC", TimelineDocs, nil, 0)
	if len(media) != 2 || len(docs) != 1 || docs[0].Path != "Documents/bail.pdf" {
		t.Errorf("filters: media %s, docs %s", timelinePaths(media), timelinePaths(docs))
	}

	months, err := s.TimelineMonths(ctx, "enzo", "Europe/Paris", TimelineAll)
	if err != nil || fmt.Sprint(months) != "[{2023-03 1} {2024-07 1} {2026-11 1}]" {
		t.Errorf("months %v, %v", months, err)
	}
}

func TestTimelinePagesNeverRepeatNorSkip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	same := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 7 {
		putModified(t, s, fmt.Sprintf("f%d.txt", i), fmt.Sprint(i), same) // all at the same instant
	}
	putModified(t, s, "older.txt", "o", same.Add(-time.Hour))
	var seen []string
	var after *TimelineCursor
	for range 10 {
		page, err := s.Timeline(ctx, "enzo", "UTC", TimelineAll, after, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page {
			seen = append(seen, it.Path)
		}
		if len(page) < 3 {
			break
		}
		last := page[len(page)-1]
		after = &TimelineCursor{At: last.At, Path: last.Path}
	}
	want := "f6.txt f5.txt f4.txt f3.txt f2.txt f1.txt f0.txt older.txt"
	if got := strings.Join(seen, " "); got != want {
		t.Errorf("pages %s, want %s", got, want)
	}
}
