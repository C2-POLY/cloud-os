package boxstatus

import "time"

// The thresholds of the alerts (docs/architecture/status.md).
const (
	// LowDiskPercent: a disk with less free space than this share of its size is low.
	LowDiskPercent = 10
	// WorkerWindow: a worker whose newest heartbeat is this old has stopped
	// (as for GET /api/health).
	WorkerWindow = 30 * time.Second
	// BackupMaxAge: with no backup that succeeded for this long, the backups
	// have stopped: a service down, a disk unplugged, a night missed.
	BackupMaxAge = 36 * time.Hour
	// RunCutOff: a run unfinished after this long was cut off.
	RunCutOff = 6 * time.Hour
	// RestoreTestMaxAge: the restore test runs every Sunday; older than this, it has stopped.
	RestoreTestMaxAge = 8 * 24 * time.Hour
	// KeptRecent: an SMB write kept in the trash this recently still calls for a look.
	KeptRecent = 7 * 24 * time.Hour
	// KeptWindow: the SMB writes kept in the trash are counted over this long.
	KeptWindow = 30 * 24 * time.Hour
)

// The kinds of alert (shared/api-types.ts, AlertKind).
const (
	DataDiskLow       = "data_disk_low"
	BackupDiskLow     = "backup_disk_low"
	ServiceDown       = "service_down"
	BackupFailed      = "backup_failed"
	BackupStale       = "backup_stale"
	RestoreTestFailed = "restore_test_failed"
	RestoreTestStale  = "restore_test_stale"
	SMBKept           = "smb_kept"
)

// Alert is something the administrator should look at.
type Alert struct {
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"` // service_down: which one
}

// Alerts lists what in s calls for the administrator, at now, in a fixed
// order. The backup's alerts only count when the backup service runs.
func Alerts(s Status, now time.Time) []Alert {
	out := []Alert{}
	if low(s.DataDisk) {
		out = append(out, Alert{Kind: DataDiskLow})
	}
	if s.Backup.On && low(s.BackupDisk) {
		out = append(out, Alert{Kind: BackupDiskLow})
	}
	for _, svc := range s.Services {
		if svc.down(now) {
			out = append(out, Alert{Kind: ServiceDown, Service: svc.Name})
		}
	}
	if s.Backup.On {
		if s.Backup.Last.Backup.failed(now) {
			out = append(out, Alert{Kind: BackupFailed})
		}
		if s.Backup.LastOK == nil || now.Sub(*s.Backup.LastOK) >= BackupMaxAge {
			out = append(out, Alert{Kind: BackupStale})
		}
		rt := s.Backup.Last.RestoreTest
		if rt.failed(now) {
			out = append(out, Alert{Kind: RestoreTestFailed})
		}
		if rt != nil && now.Sub(rt.StartedAt) > RestoreTestMaxAge ||
			rt == nil && s.Backup.FirstAt != nil && now.Sub(*s.Backup.FirstAt) > RestoreTestMaxAge {
			out = append(out, Alert{Kind: RestoreTestStale})
		}
	}
	k := s.SMBKept
	if k.Waiting != nil && *k.Waiting > 0 || k.LastAt != nil && now.Sub(*k.LastAt) < KeptRecent {
		out = append(out, Alert{Kind: SMBKept})
	}
	return out
}

// low says a measured disk has less than LowDiskPercent of its size free.
func low(d *Disk) bool {
	return d != nil && d.Total > 0 && d.Free*100 < d.Total*LowDiskPercent
}

// alive says a heartbeat seen at seen is recent enough at now.
func alive(seen *time.Time, now time.Time) bool {
	return seen != nil && now.Sub(*seen) < WorkerWindow
}

// down says the service should answer and does not. The worker is judged on
// its heartbeat at now and on its query endpoint, whatever its State says.
func (s Service) down(now time.Time) bool {
	if s.Name == Worker {
		return !alive(s.SeenAt, now) || s.Search == Down
	}
	return s.State == Down
}

// state is where the run stands at now.
func (r *Run) state(now time.Time) RunState {
	switch {
	case r.FinishedAt == nil && now.Sub(r.StartedAt) >= RunCutOff:
		return Interrupted
	case r.FinishedAt == nil:
		return Running
	case r.OK != nil && *r.OK:
		return Succeeded
	default:
		return Failed
	}
}

// failed says the run failed or was cut off; false for none.
func (r *Run) failed(now time.Time) bool {
	if r == nil {
		return false
	}
	st := r.state(now)
	return st == Failed || st == Interrupted
}
