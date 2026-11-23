package boxstatus

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStore answers what the database would.
type fakeStore struct {
	Seen      *time.Time
	Runs      Backup
	Disk      *Disk
	Kept      int
	KeptLast  *time.Time
	Err       error
	KeptSince time.Time // what KeptInTrash was asked
}

func (f *fakeStore) WorkerSeen(context.Context) (*time.Time, error) { return f.Seen, f.Err }

func (f *fakeStore) Backups(context.Context) (Backup, *Disk, error) { return f.Runs, f.Disk, f.Err }

func (f *fakeStore) KeptInTrash(_ context.Context, since time.Time) (int, *time.Time, error) {
	f.KeptSince = since
	return f.Kept, f.KeptLast, f.Err
}

// fakeFiles is the data disk.
type fakeFiles struct {
	Total, Free uint64
	DiskErr     error
	Rescued     int
	RescueErr   error
}

func (f fakeFiles) DiskUsage() (uint64, uint64, error) { return f.Total, f.Free, f.DiskErr }

func (f fakeFiles) RescuedCount() (int, error) { return f.Rescued, f.RescueErr }

// fakeProbe answers with Err, after Wait.
type fakeProbe struct {
	Err  error
	Wait time.Duration
}

func (p fakeProbe) Check(ctx context.Context) error {
	select {
	case <-time.After(p.Wait):
		return p.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestCollect(t *testing.T) {
	store := &fakeStore{
		Seen: ago(5 * time.Second),
		Runs: Backup{Last: LastRuns{
			Backup: &Run{StartedAt: now.Add(-time.Hour)}, // still running
			Check:  &Run{StartedAt: now.Add(-9 * time.Hour)},
		}},
		Disk:     &Disk{Total: 2000, Free: 100, At: ago(time.Hour)},
		Kept:     2,
		KeptLast: ago(24 * time.Hour),
	}
	c := &Collector{
		Version:   "0.5.0",
		DB:        store,
		Files:     fakeFiles{Total: 1000, Free: 50, Rescued: 1},
		Worker:    fakeProbe{},
		SMB:       fakeProbe{Err: errors.New("connection refused")},
		Tailscale: nil, // off
		Backup:    true,
		Access:    Access{PublicURL: "https://cloudos.example.ts.net", Funnel: true},
		Now:       func() time.Time { return now },
	}
	st, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "0.5.0" || !st.CheckedAt.Equal(now) || st.Access != c.Access || !st.Backup.On {
		t.Errorf("status = %+v", st)
	}
	if st.DataDisk == nil || st.DataDisk.Total != 1000 || st.DataDisk.Free != 50 {
		t.Errorf("data disk = %+v", st.DataDisk)
	}
	if st.BackupDisk != store.Disk {
		t.Errorf("backup disk = %+v", st.BackupDisk)
	}
	want := map[string]State{Core: OK, Worker: OK, SMB: Down, Tailscale: Off}
	if len(st.Services) != 4 {
		t.Fatalf("services = %+v", st.Services)
	}
	for i, name := range []string{Core, Worker, SMB, Tailscale} {
		if s := st.Services[i]; s.Name != name || s.State != want[name] {
			t.Errorf("service %d = %+v, want %s %s", i, s, name, want[name])
		}
	}
	if w := st.Services[1]; w.Analysis != OK || w.Search != OK || w.SeenAt == nil {
		t.Errorf("worker = %+v", w)
	}
	if st.Backup.Last.Backup.State != Running || st.Backup.Last.Check.State != Interrupted {
		t.Errorf("run states: backup %s, check %s", st.Backup.Last.Backup.State, st.Backup.Last.Check.State)
	}
	if st.SMBKept.Waiting == nil || *st.SMBKept.Waiting != 1 || st.SMBKept.InTrash != 2 {
		t.Errorf("SMB kept = %+v", st.SMBKept)
	}
	if !store.KeptSince.Equal(now.Add(-30 * 24 * time.Hour)) {
		t.Errorf("kept counted since %v, want 30 days", store.KeptSince)
	}
	if got := kinds(st.Alerts); len(got) != 5 || got[0] != DataDiskLow || got[1] != BackupDiskLow ||
		got[2] != "service_down:smb" || got[3] != BackupStale || got[4] != SMBKept {
		t.Errorf("alerts = %v", got)
	}
}

// The worker is down when its heartbeat is old, or when its query endpoint
// does not answer within ProbeTimeout; a disk or folder that cannot be read
// is left out, not an error.
func TestCollectWorkerDown(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the probe's timeout")
	}
	c := &Collector{
		DB:     &fakeStore{Seen: ago(time.Minute)},
		Files:  fakeFiles{DiskErr: errors.New("statfs: no such file"), RescueErr: errors.New("permission denied")},
		Worker: fakeProbe{Wait: time.Minute}, // never answers
		Now:    func() time.Time { return now },
	}
	start := time.Now()
	st, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > ProbeTimeout+time.Second {
		t.Errorf("Collect took %v: the probe outlived its timeout", took)
	}
	if w := st.Services[1]; w.State != Down || w.Analysis != Down || w.Search != Down {
		t.Errorf("worker = %+v", w)
	}
	if st.DataDisk != nil || st.SMBKept.Waiting != nil {
		t.Errorf("unreadable disk or folder shown: %+v %+v", st.DataDisk, st.SMBKept)
	}
	if got := kinds(st.Alerts); len(got) != 1 || got[0] != "service_down:worker" {
		t.Errorf("alerts = %v (backup off: no backup alert)", got)
	}
}

func TestCollectDatabaseError(t *testing.T) {
	c := &Collector{DB: &fakeStore{Err: errors.New("connection refused")}, Worker: fakeProbe{}}
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatal("a database error gave a status")
	}
}
