// Package boxstatus gathers the state of the box for its administrators
// (#102, docs/architecture/status.md): the space of its disks, whether each
// service answers, the backup's last runs, the SMB writes the box kept, and
// the alerts all that calls for. Every probe only reads: nothing here writes
// a file or a row.
package boxstatus

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// State says whether a service answers.
type State string

const (
	OK   State = "ok"
	Down State = "down"
	Off  State = "off" // not turned on for this box
)

// The services the status lists, in this order.
const (
	Core      = "core"
	Worker    = "worker"
	SMB       = "smb"
	Tailscale = "tailscale"
)

// ProbeTimeout bounds each probe of a service: past it, the service is down.
const ProbeTimeout = 2 * time.Second

// Status is the body of GET /api/box/status (shared/api-types.ts, BoxStatus).
type Status struct {
	Version   string    `json:"version"`
	CheckedAt time.Time `json:"checked_at"`
	// DataDisk is the disk of the files; nil when it could not be measured.
	DataDisk *Disk `json:"data_disk,omitempty"`
	// BackupDisk is the backup's disk as the newest run measured it (the
	// core does not mount it); nil until a run has.
	BackupDisk *Disk     `json:"backup_disk,omitempty"`
	Services   []Service `json:"services"`
	Access     Access    `json:"access"`
	Backup     Backup    `json:"backup"`
	SMBKept    Kept      `json:"smb_kept"`
	Alerts     []Alert   `json:"alerts"`
}

// Disk is the size and the free space of a disk, in bytes.
type Disk struct {
	Total uint64     `json:"total"`
	Free  uint64     `json:"free"`
	At    *time.Time `json:"at,omitempty"` // when it was measured, for the backup disk
}

// Service is one of the box's services and whether it answers.
type Service struct {
	Name  string `json:"name"`
	State State  `json:"state"`
	// The worker only: its newest heartbeat, which says the analysis runs,
	// and its query endpoint (GET /health), which the searches by meaning need.
	SeenAt   *time.Time `json:"seen_at,omitempty"`
	Analysis State      `json:"analysis,omitempty"`
	Search   State      `json:"search,omitempty"`
}

// Access is how the box is reached from outside the home.
type Access struct {
	PublicURL string `json:"public_url"` // CLOUDOS_PUBLIC_URL, "" without Tailscale
	Funnel    bool   `json:"funnel"`     // the share links are open to the internet
}

// Backup is where the nightly backup stands, from backup_runs.
type Backup struct {
	On     bool       `json:"on"` // the backup service runs (CLOUDOS_BACKUP=1)
	Last   LastRuns   `json:"last"`
	LastOK *time.Time `json:"last_ok,omitempty"` // the end of the newest backup that succeeded
	// FirstAt is the start of the first backup run: a box that has backed up
	// for over a week has had a restore test.
	FirstAt *time.Time `json:"first_at,omitempty"`
}

// LastRuns is the newest run of each kind; nil when none ran.
type LastRuns struct {
	Backup      *Run `json:"backup,omitempty"`
	Copy        *Run `json:"copy,omitempty"`
	Check       *Run `json:"check,omitempty"`
	RestoreTest *Run `json:"restore_test,omitempty"`
}

// RunState is where a run stands.
type RunState string

const (
	Running     RunState = "running"
	Succeeded   RunState = "ok"
	Failed      RunState = "failed"
	Interrupted RunState = "interrupted" // unfinished after RunCutOff: the service stopped during it
)

// Run is a row of backup_runs, with what the panel shows of its detail.
type Run struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	OK         *bool      `json:"ok,omitempty"`
	State      RunState   `json:"state"`
	Error      string     `json:"error,omitempty"`
	// A backup: the files and bytes read, the bytes added to the repository,
	// how long it took.
	Files      *int64 `json:"files,omitempty"`
	Bytes      *int64 `json:"bytes,omitempty"`
	DataAdded  *int64 `json:"data_added,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
	// A restore test: the files compared with the index, and how many differ.
	Sample     *int64 `json:"sample,omitempty"`
	Mismatches *int64 `json:"mismatches,omitempty"`
}

// Kept is what the box kept of SMB writes it could not store (#111).
type Kept struct {
	// Waiting is how many wait in .cloudos/rescue for the database; nil when
	// the folder could not be read.
	Waiting *int `json:"waiting,omitempty"`
	// InTrash is how many were put in the trash over the last KeptWindow
	// (reason smb_not_saved), and are still there; LastAt the newest.
	InTrash int        `json:"in_trash"`
	LastAt  *time.Time `json:"last_at,omitempty"`
}

// Store is what Collect reads from the database.
type Store interface {
	// WorkerSeen is the newest heartbeat of any worker; nil when none ever beat.
	WorkerSeen(ctx context.Context) (*time.Time, error)
	// Backups is the newest run of each kind, the newest success and the
	// first run, without State nor On; and the backup disk as the newest run
	// that measured it saw it, nil when none did.
	Backups(ctx context.Context) (Backup, *Disk, error)
	// KeptInTrash counts the SMB writes put in the trash since then, still
	// there, and gives the newest.
	KeptInTrash(ctx context.Context, since time.Time) (int, *time.Time, error)
}

// Files is the data disk and its rescue folder (DataFiles).
type Files interface {
	DiskUsage() (total, free uint64, err error)
	RescuedCount() (int, error)
}

// Probe asks a service whether it answers (HTTPProbe, TCPProbe).
type Probe interface {
	Check(ctx context.Context) error
}

// Collector gathers the box's status. Each source is a small interface, so
// that the tests give fake ones.
type Collector struct {
	Version string
	DB      Store
	Files   Files
	// Worker is the worker's GET /health; nil when the core has no worker URL.
	Worker Probe
	// SMB and Tailscale are nil when the service is off (CLOUDOS_SMB,
	// CLOUDOS_TAILSCALE).
	SMB, Tailscale Probe
	// Backup says the backup service runs (CLOUDOS_BACKUP=1).
	Backup bool
	Access Access
	Now    func() time.Time // nil: time.Now
	Log    *slog.Logger     // nil: slog.Default()
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// Collect reads the box's state and the alerts it calls for. The services
// are asked in parallel, each within ProbeTimeout; a disk or a folder that
// cannot be read is left out; only a database error fails it.
func (c *Collector) Collect(ctx context.Context) (Status, error) {
	now := c.now()
	var wg sync.WaitGroup
	defer wg.Wait() // no probe outlives the request
	ask := func(name string, p Probe, out *State) {
		if p == nil {
			*out = Off
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
			defer cancel()
			if err := p.Check(pctx); err != nil {
				// Every poll of the panel would repeat it: the panel says it.
				c.log().Debug("box status: no answer", "service", name, "err", err)
				*out = Down
				return
			}
			*out = OK
		}()
	}
	var search, smb, ts State
	ask(Worker, c.Worker, &search)
	ask(SMB, c.SMB, &smb)
	ask(Tailscale, c.Tailscale, &ts)

	st := Status{Version: c.Version, CheckedAt: now, Access: c.Access}
	seen, err := c.DB.WorkerSeen(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("box status: the workers' heartbeat: %w", err)
	}
	if st.Backup, st.BackupDisk, err = c.DB.Backups(ctx); err != nil {
		return Status{}, fmt.Errorf("box status: the backup runs: %w", err)
	}
	st.Backup.On = c.Backup
	for _, r := range []*Run{st.Backup.Last.Backup, st.Backup.Last.Copy, st.Backup.Last.Check, st.Backup.Last.RestoreTest} {
		if r != nil {
			r.State = r.state(now)
		}
	}
	if st.SMBKept.InTrash, st.SMBKept.LastAt, err = c.DB.KeptInTrash(ctx, now.Add(-KeptWindow)); err != nil {
		return Status{}, fmt.Errorf("box status: the SMB writes kept in the trash: %w", err)
	}
	if c.Files != nil {
		if total, free, err := c.Files.DiskUsage(); err != nil || total == 0 {
			c.log().Warn("box status: measure the data disk", "err", err)
		} else {
			st.DataDisk = &Disk{Total: total, Free: free}
		}
		if n, err := c.Files.RescuedCount(); err != nil {
			c.log().Warn("box status: read the SMB rescue folder", "err", err)
		} else {
			st.SMBKept.Waiting = &n
		}
	}

	wg.Wait()
	worker := Service{Name: Worker, SeenAt: seen, Analysis: Down, Search: search}
	if alive(seen, now) {
		worker.Analysis = OK
	}
	worker.State = OK
	if worker.down(now) {
		worker.State = Down
	}
	st.Services = []Service{
		{Name: Core, State: OK}, // it is answering
		worker,
		{Name: SMB, State: smb},
		{Name: Tailscale, State: ts},
	}
	st.Alerts = Alerts(st, now)
	return st, nil
}
