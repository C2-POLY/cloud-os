package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/boxstatus"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

// statusDB answers what the database would for the box's status.
type statusDB struct {
	seen *time.Time
	runs boxstatus.Backup
	err  error
}

func (f *statusDB) WorkerSeen(context.Context) (*time.Time, error) { return f.seen, f.err }

func (f *statusDB) Backups(context.Context) (boxstatus.Backup, *boxstatus.Disk, error) {
	return f.runs, nil, f.err
}

func (f *statusDB) KeptInTrash(context.Context, time.Time) (int, *time.Time, error) {
	return 0, nil, f.err
}

// probe answers with err.
type probe struct{ err error }

func (p probe) Check(context.Context) error { return p.err }

// newStatusAPI is an API with an admin (enzo) and a member (johan), whose
// box status comes from the given collector.
func newStatusAPI(t *testing.T, c *boxstatus.Collector) (enzo, johan, anon *client) {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	files := &storage.Store{Root: t.TempDir(), Pool: pool}
	h := NewHandler(Config{Version: "test", Health: &fakeStore{}, Auth: auth.NewService(pool), Files: files, Status: c})
	enzo = &client{t: t, h: h}
	enzo.do("POST", "/api/setup", `{"name":"enzo","password":"`+pw+`"}`)
	enzo.do("POST", "/api/accounts", `{"name":"johan","password":"`+pw+`"}`)
	johan = &client{t: t, h: h}
	johan.do("POST", "/api/login", `{"name":"johan","password":"`+pw+`"}`)
	return enzo, johan, &client{t: t, h: h}
}

// The admin gets the box's status with its alerts; a member and a visitor
// get nothing of it.
func TestBoxStatus(t *testing.T) {
	seen := time.Now().Add(-2 * time.Minute) // the worker stopped
	failed := false
	finished := time.Now().Add(-time.Hour)
	c := &boxstatus.Collector{
		Version: "0.5.0",
		DB: &statusDB{seen: &seen, runs: boxstatus.Backup{Last: boxstatus.LastRuns{
			Backup: &boxstatus.Run{StartedAt: finished.Add(-time.Minute), FinishedAt: &finished, OK: &failed, Error: "no restic repository"},
		}}},
		Worker: probe{},
		SMB:    probe{err: errors.New("connection refused")},
		Backup: true,
		Access: boxstatus.Access{PublicURL: "https://cloudos.example.ts.net"},
	}
	enzo, johan, anon := newStatusAPI(t, c)

	rec := enzo.do("GET", "/api/box/status", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body)
	}
	st := decode[boxstatus.Status](t, rec.Body.String())
	if st.Version != "0.5.0" || st.Access.PublicURL != "https://cloudos.example.ts.net" || !st.Backup.On {
		t.Errorf("status = %+v", st)
	}
	var got []string
	for _, a := range st.Alerts {
		got = append(got, a.Kind+":"+a.Service)
	}
	want := "service_down:worker service_down:smb backup_failed: backup_stale:"
	if strings.Join(got, " ") != want {
		t.Errorf("alerts = %v, want %s", got, want)
	}
	if b := st.Backup.Last.Backup; b == nil || b.State != boxstatus.Failed || b.Error != "no restic repository" {
		t.Errorf("last backup = %+v", b)
	}
	// Nothing measured is left out rather than sent as zero; the list of
	// alerts is a list even when empty.
	if strings.Contains(rec.Body.String(), `"backup_disk"`) || !strings.Contains(rec.Body.String(), `"alerts":[`) {
		t.Errorf("body = %s", rec.Body)
	}

	if rec := johan.do("GET", "/api/box/status", ""); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "0.5.0") {
		t.Errorf("member: %d %s", rec.Code, rec.Body)
	}
	if rec := anon.do("GET", "/api/box/status", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session: %d %s", rec.Code, rec.Body)
	}
}

// Without its source, or when the database fails, the route says 503.
func TestBoxStatusUnavailable(t *testing.T) {
	enzo, _, _ := newStatusAPI(t, nil)
	if rec := enzo.do("GET", "/api/box/status", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no collector: %d %s", rec.Code, rec.Body)
	}
	enzo, _, _ = newStatusAPI(t, &boxstatus.Collector{DB: &statusDB{err: errors.New("database down")}})
	rec := enzo.do("GET", "/api/box/status", "")
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "database down") {
		t.Errorf("database error: %d %s", rec.Code, rec.Body)
	}
}
