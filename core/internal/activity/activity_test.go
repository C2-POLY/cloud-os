package activity_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/C2-POLY/cloud-os/core/internal/activity"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

func TestGroupsByDayKindAndPlace(t *testing.T) {
	pool, _ := testdb.Migrated(t)
	l := &activity.Log{Pool: pool}
	ctx := context.Background()
	for i := range 3 {
		l.Record(ctx, "enzo", activity.Added, fmt.Sprintf("Photos/img%d.jpg", i))
	}
	l.Record(ctx, "enzo", activity.Added, "notes.txt")
	l.Record(ctx, "enzo", activity.ShareOpened, "Photos/Vacances")
	l.Record(ctx, "enzo", activity.ShareOpened, "Photos/Vacances")
	l.Record(ctx, "johan", activity.Added, "Photos/autre.jpg")
	// Yesterday 23:30 in Paris is today in UTC+14 but yesterday in Paris.
	if _, err := pool.Exec(ctx, `INSERT INTO activity (owner, kind, path, created_at) VALUES ('enzo', 'trashed', 'vieux.txt', now() - interval '2 days')`); err != nil {
		t.Fatal(err)
	}

	groups, err := l.Groups(ctx, "enzo", 7, "Europe/Paris", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]activity.Group{}
	for _, g := range groups {
		got[g.Kind+" "+g.Place] = g
	}
	if len(groups) != 4 {
		t.Fatalf("groups: %+v", groups)
	}
	if g := got["added Photos"]; g.Count != 3 || g.Name != "" {
		t.Errorf("photos: %+v", g)
	}
	if g := got["added "]; g.Count != 1 || g.Name != "notes.txt" {
		t.Errorf("root: %+v", g)
	}
	if g := got["share_opened Photos/Vacances"]; g.Count != 2 {
		t.Errorf("share: %+v", g)
	}
	if g := got["trashed "]; g.Count != 1 || g.Day == got["added Photos"].Day {
		t.Errorf("trashed two days ago: %+v", g)
	}
	// One day back only: the trash event is out.
	if groups, _ := l.Groups(ctx, "enzo", 1, "UTC", 0); len(groups) != 3 {
		t.Errorf("last day: %+v", groups)
	}
	if groups, _ := l.Groups(ctx, "enzo", 7, "UTC", 2); len(groups) != 2 {
		t.Errorf("limit: %+v", groups)
	}
}

func TestPruneForgetsOldEvents(t *testing.T) {
	pool, _ := testdb.Migrated(t)
	l := &activity.Log{Pool: pool}
	ctx := context.Background()
	l.Record(ctx, "enzo", activity.Added, "new.txt")
	if _, err := pool.Exec(ctx, `INSERT INTO activity (owner, kind, path, created_at) VALUES ('enzo', 'added', 'old.txt', now() - interval '91 days')`); err != nil {
		t.Fatal(err)
	}
	if n, err := l.Prune(ctx); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM activity`).Scan(&left)
	if left != 1 {
		t.Errorf("left %d", left)
	}
}

func TestRecordWithoutAJournalDoesNothing(t *testing.T) {
	var l *activity.Log
	l.Record(context.Background(), "enzo", activity.Added, "x") // must not panic
}
