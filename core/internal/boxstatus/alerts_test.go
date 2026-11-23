package boxstatus

import (
	"slices"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) *time.Time {
	t := now.Add(-d)
	return &t
}

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }
func count(n int) *int {
	return &n
}

// healthy is a box with nothing to say: every service answers, the backup
// ran last night and its test last Sunday, nothing kept.
func healthy() Status {
	return Status{
		DataDisk:   &Disk{Total: 1000, Free: 500},
		BackupDisk: &Disk{Total: 1000, Free: 500, At: ago(9 * time.Hour)},
		Services: []Service{
			{Name: Core, State: OK},
			{Name: Worker, State: OK, SeenAt: ago(time.Second), Analysis: OK, Search: OK},
			{Name: SMB, State: OK},
			{Name: Tailscale, State: Off},
		},
		Backup: Backup{
			On: true,
			Last: LastRuns{
				Backup:      &Run{StartedAt: now.Add(-9 * time.Hour), FinishedAt: ago(8 * time.Hour), OK: yes()},
				RestoreTest: &Run{StartedAt: now.Add(-3 * 24 * time.Hour), FinishedAt: ago(3 * 24 * time.Hour), OK: yes()},
			},
			LastOK:  ago(8 * time.Hour),
			FirstAt: ago(60 * 24 * time.Hour),
		},
		SMBKept: Kept{Waiting: count(0)},
	}
}

func kinds(alerts []Alert) []string {
	out := []string{}
	for _, a := range alerts {
		k := a.Kind
		if a.Service != "" {
			k += ":" + a.Service
		}
		out = append(out, k)
	}
	return out
}

// Each rule at its threshold: on one side nothing, on the other the alert.
func TestAlerts(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Status)
		want   []string
	}{
		{"a healthy box", func(*Status) {}, nil},

		{"data disk at 10 % free", func(s *Status) { s.DataDisk.Free = 100 }, nil},
		{"data disk under 10 % free", func(s *Status) { s.DataDisk.Free = 99 }, []string{DataDiskLow}},
		{"data disk not measured", func(s *Status) { s.DataDisk = nil }, nil},
		{"backup disk at 10 % free", func(s *Status) { s.BackupDisk.Free = 100 }, nil},
		{"backup disk under 10 % free", func(s *Status) { s.BackupDisk.Free = 99 }, []string{BackupDiskLow}},
		{"backup disk low, backup off", func(s *Status) {
			s.BackupDisk.Free = 1
			s.Backup.On = false
		}, nil},

		{"worker seen 29 s ago", func(s *Status) { s.Services[1].SeenAt = ago(29 * time.Second) }, nil},
		{"worker seen 30 s ago", func(s *Status) { s.Services[1].SeenAt = ago(30 * time.Second) }, []string{"service_down:worker"}},
		{"worker never seen", func(s *Status) { s.Services[1].SeenAt = nil }, []string{"service_down:worker"}},
		{"worker search down", func(s *Status) { s.Services[1].Search = Down }, []string{"service_down:worker"}},
		{"worker search off", func(s *Status) { s.Services[1].Search = Off }, nil},
		{"smb down", func(s *Status) { s.Services[2].State = Down }, []string{"service_down:smb"}},
		{"smb off", func(s *Status) { s.Services[2].State = Off }, nil},
		{"tailscale down", func(s *Status) { s.Services[3].State = Down }, []string{"service_down:tailscale"}},

		{"last backup failed", func(s *Status) { s.Backup.Last.Backup.OK = no() }, []string{BackupFailed}},
		{"backup running for 5 h 59", func(s *Status) {
			s.Backup.Last.Backup = &Run{StartedAt: now.Add(-6*time.Hour + time.Minute)}
		}, nil},
		{"backup unfinished after 6 h", func(s *Status) {
			s.Backup.Last.Backup = &Run{StartedAt: now.Add(-6 * time.Hour)}
		}, []string{BackupFailed}},
		{"last success 35 h 59 ago", func(s *Status) { s.Backup.LastOK = ago(36*time.Hour - time.Minute) }, nil},
		{"last success 36 h ago", func(s *Status) { s.Backup.LastOK = ago(36 * time.Hour) }, []string{BackupStale}},
		{"no backup ever succeeded", func(s *Status) {
			s.Backup.Last = LastRuns{}
			s.Backup.LastOK, s.Backup.FirstAt = nil, nil
		}, []string{BackupStale}},
		{"backup off: no backup alert", func(s *Status) {
			s.Backup.On = false
			s.Backup.Last.Backup.OK = no()
			s.Backup.LastOK = nil
		}, nil},

		{"restore test failed", func(s *Status) { s.Backup.Last.RestoreTest.OK = no() }, []string{RestoreTestFailed}},
		{"restore test 8 days old", func(s *Status) {
			s.Backup.Last.RestoreTest.StartedAt = now.Add(-8 * 24 * time.Hour)
		}, nil},
		{"restore test 8 days and a minute old", func(s *Status) {
			s.Backup.Last.RestoreTest.StartedAt = now.Add(-8*24*time.Hour - time.Minute)
		}, []string{RestoreTestStale}},
		{"no restore test, first backup 8 days ago", func(s *Status) {
			s.Backup.Last.RestoreTest = nil
			s.Backup.FirstAt = ago(8 * 24 * time.Hour)
		}, nil},
		{"no restore test, first backup over 8 days ago", func(s *Status) {
			s.Backup.Last.RestoreTest = nil
			s.Backup.FirstAt = ago(8*24*time.Hour + time.Minute)
		}, []string{RestoreTestStale}},

		{"an SMB write waits in the rescue folder", func(s *Status) { s.SMBKept.Waiting = count(1) }, []string{SMBKept}},
		{"the rescue folder unreadable", func(s *Status) { s.SMBKept.Waiting = nil }, nil},
		{"an SMB write kept in the trash 6 days ago", func(s *Status) {
			s.SMBKept.InTrash, s.SMBKept.LastAt = 1, ago(7*24*time.Hour-time.Minute)
		}, []string{SMBKept}},
		{"an SMB write kept in the trash 7 days ago", func(s *Status) {
			s.SMBKept.InTrash, s.SMBKept.LastAt = 1, ago(7*24*time.Hour)
		}, nil},

		{"several at once, in order", func(s *Status) {
			s.SMBKept.Waiting = count(2)
			s.Services[2].State = Down
			s.DataDisk.Free = 0
			s.Backup.Last.Backup.OK = no()
		}, []string{DataDiskLow, "service_down:smb", BackupFailed, SMBKept}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := healthy()
			tt.change(&s)
			got := Alerts(s, now)
			if got == nil {
				t.Fatal("Alerts returned nil: the JSON would say null, not []")
			}
			if want := tt.want; !slices.Equal(kinds(got), append([]string{}, want...)) {
				t.Errorf("alerts = %v, want %v", kinds(got), want)
			}
		})
	}
}

func TestRunState(t *testing.T) {
	tests := []struct {
		run  Run
		want RunState
	}{
		{Run{StartedAt: now.Add(-time.Hour)}, Running},
		{Run{StartedAt: now.Add(-RunCutOff)}, Interrupted},
		{Run{StartedAt: now.Add(-time.Hour), FinishedAt: ago(time.Minute), OK: yes()}, Succeeded},
		{Run{StartedAt: now.Add(-time.Hour), FinishedAt: ago(time.Minute), OK: no()}, Failed},
	}
	for _, tt := range tests {
		if got := tt.run.state(now); got != tt.want {
			t.Errorf("state of %+v = %s, want %s", tt.run, got, tt.want)
		}
	}
}
