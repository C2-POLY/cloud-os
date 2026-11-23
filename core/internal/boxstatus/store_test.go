package boxstatus

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// PGStore reads the newest run of each kind, the newest success, the first
// run and the newest measure of the backup disk, the workers' heartbeat and
// the SMB writes kept in the trash.
func TestPGStore(t *testing.T) {
	ctx := context.Background()
	pool, _ := testdb.Migrated(t)
	s := &PGStore{Pool: pool}

	// An empty box: nothing ran, no worker beat.
	seen, err := s.WorkerSeen(ctx)
	if err != nil || seen != nil {
		t.Fatalf("heartbeat of an empty box: %v %v", seen, err)
	}
	b, disk, err := s.Backups(ctx)
	if err != nil || b.Last != (LastRuns{}) || b.LastOK != nil || b.FirstAt != nil || disk != nil {
		t.Fatalf("backups of an empty box: %+v %+v %v", b, disk, err)
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO worker_heartbeat VALUES ('old', now() - interval '1 hour'), ('new', now() - interval '3 seconds')`)
	// Two nights: the first backup succeeded and measured its disk, the
	// second failed without its disk (nothing measured); a restore test with
	// two mismatches; a check that still runs.
	exec(`INSERT INTO backup_runs (kind, started_at, finished_at, ok, detail) VALUES
	        ('backup', now() - interval '2 days', now() - interval '47 hours', true,
	         '{"files": 12, "bytes": 3456, "data_added": 78, "duration_ms": 900, "disk_free": 1000, "disk_total": 8000}')`)
	exec(`INSERT INTO backup_runs (kind, started_at, finished_at, ok, detail, error) VALUES
	        ('backup', now() - interval '1 day', now() - interval '23 hours', false, '{}', 'no restic repository: is the backup disk plugged in?')`)
	exec(`INSERT INTO backup_runs (kind, started_at, finished_at, ok, detail, error) VALUES
	        ('restore_test', now() - interval '3 days', now() - interval '3 days', false,
	         '{"sample": 200, "mismatches": ["enzo/a.txt: sha256 …", "lea/b.txt: sha256 …"]}', '2 of 200 sampled files differ')`)
	exec(`INSERT INTO backup_runs (kind) VALUES ('check')`)
	// Kept SMB writes: one yesterday, one 40 days ago (out of the count), and
	// a file someone deleted.
	exec(`INSERT INTO trash (owner, path, kind, size, expires_at, reason, deleted_at) VALUES
	        ('enzo', 'a.txt', 'file', 1, now() + interval '29 days', $1, now() - interval '1 day'),
	        ('enzo', 'b.txt', 'file', 1, now() + interval '1 day', $1, now() - interval '40 days'),
	        ('lea', 'c.txt', 'file', 1, now() + interval '29 days', NULL, now())`, storage.RescueReason)

	seen, err = s.WorkerSeen(ctx)
	if err != nil || seen == nil || time.Since(*seen) > time.Minute {
		t.Errorf("newest heartbeat: %v %v", seen, err)
	}

	b, disk, err = s.Backups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	last := b.Last.Backup
	if last == nil || last.OK == nil || *last.OK || last.Error == "" || last.Files != nil {
		t.Errorf("newest backup: %+v", last)
	}
	if b.LastOK == nil || time.Since(*b.LastOK) < 46*time.Hour || b.FirstAt == nil || time.Since(*b.FirstAt) < 47*time.Hour {
		t.Errorf("last success %v, first run %v", b.LastOK, b.FirstAt)
	}
	if disk == nil || disk.Free != 1000 || disk.Total != 8000 || disk.At == nil || time.Since(*disk.At) < 46*time.Hour {
		t.Errorf("backup disk: %+v (the newest measure, the failed run having none)", disk)
	}
	rt := b.Last.RestoreTest
	if rt == nil || rt.Sample == nil || *rt.Sample != 200 || rt.Mismatches == nil || *rt.Mismatches != 2 {
		t.Errorf("restore test: %+v", rt)
	}
	if c := b.Last.Check; c == nil || c.FinishedAt != nil || c.OK != nil {
		t.Errorf("running check: %+v", c)
	}
	if b.Last.Copy != nil {
		t.Errorf("a copy that never ran: %+v", b.Last.Copy)
	}

	// The successful run's own detail reaches the panel.
	exec(`UPDATE backup_runs SET ok = true, error = NULL, detail = '{"files": 12, "bytes": 3456, "data_added": 78, "duration_ms": 900}'
	       WHERE id = (SELECT max(id) FROM backup_runs WHERE kind = 'backup')`)
	b, _, err = s.Backups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := b.Last.Backup; r.Files == nil || *r.Files != 12 || *r.Bytes != 3456 || *r.DataAdded != 78 || *r.DurationMS != 900 {
		t.Errorf("newest backup's detail: %+v", r)
	}

	n, lastKept, err := s.KeptInTrash(ctx, time.Now().Add(-KeptWindow))
	if err != nil || n != 1 || lastKept == nil || time.Since(*lastKept) < 23*time.Hour {
		t.Errorf("kept in the trash: %d, last %v, %v", n, lastKept, err)
	}
}

func TestHTTPProbe(t *testing.T) {
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := (HTTPProbe{URL: srv.URL + "/health"}).Check(ctx); err != nil {
		t.Errorf("a 200: %v", err)
	}
	code = http.StatusServiceUnavailable
	if err := (HTTPProbe{URL: srv.URL + "/health"}).Check(ctx); err == nil {
		t.Error("a 503 answered")
	}
	srv.Close()
	if err := (HTTPProbe{URL: srv.URL + "/health"}).Check(ctx); err == nil {
		t.Error("a closed server answered")
	}
}

func TestTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	ctx := context.Background()
	if err := (TCPProbe{Addr: addr}).Check(ctx); err != nil {
		t.Errorf("an open port: %v", err)
	}
	ln.Close()
	if err := (TCPProbe{Addr: addr}).Check(ctx); err == nil {
		t.Error("a closed port answered")
	}
}

// DataFiles measures the disk of the data root and counts its rescues.
func TestDataFiles(t *testing.T) {
	f := DataFiles{Store: &storage.Store{Root: t.TempDir()}}
	total, free, err := f.DiskUsage()
	if err != nil || total == 0 || free > total {
		t.Errorf("disk usage: %d of %d, %v", free, total, err)
	}
	if n, err := f.RescuedCount(); err != nil || n != 0 {
		t.Errorf("rescued: %d, %v", n, err)
	}
}
