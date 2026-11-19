// Package analysis tells where the background analysis of an account's files
// stands (#82, docs/architecture/tasks.md): how many files still wait for the
// workers or failed, and, for one file, each step that applies to its type
// and what became of it.
//
// It only reads the task queue and the workers' results. The queue is keyed
// by content, and a content is shared by every account that has it, so
// everything here starts from the requesting account's own file rows: another
// account's files, names and paths never appear, even when the content is the
// same.
package analysis

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/doccat"
	"github.com/C2-POLY/cloud-os/core/internal/doctext"
	"github.com/C2-POLY/cloud-os/core/internal/meta"
	"github.com/C2-POLY/cloud-os/core/internal/ocr"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
	"github.com/C2-POLY/cloud-os/core/internal/thumbs"
)

// Kinds are the task kinds of the analysis, in the order of a file's steps.
var Kinds = []string{thumbs.Kind, meta.Kind, doctext.Kind, ocr.Kind, photovec.Kind, textemb.Kind, doccat.Kind}

// The state of a step.
const (
	Waiting = "waiting" // queued (maybe for a new try, see Step.Error), or waiting for the step before it
	Running = "running"
	Done    = "done"
	Failed  = "failed"  // the workers gave up: Step.Error says why
	Skipped = "skipped" // nothing to do: a document without text has nothing to index
	Idle    = "idle"    // not queued and not done: nothing is going to run it by itself
)

// The state of a whole file.
const (
	None    = "none"    // no step applies to this type of file
	Pending = "pending" // a step waits or runs
	Ready   = "ready"   // every step is done or skipped
	Problem = "failed"  // no step is pending and at least one failed
)

// MaxError is the longest reason kept, in characters.
const MaxError = 300

// Step is one thing the workers do for a file.
type Step struct {
	Kind        string `json:"kind"`
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`        // last error of the task: why it failed, or why it is tried again
	Attempts    int    `json:"attempts,omitempty"`     // tries so far, when a task failed or is tried again
	MaxAttempts int    `json:"max_attempts,omitempty"` // as many as the workers will make
}

// FileStatus is where the analysis of one file stands.
type FileStatus struct {
	State string `json:"state"`
	Steps []Step `json:"steps"`
}

// Summary counts an account's live files by the analysis' state.
type Summary struct {
	Analysing int `json:"analysing"` // files with a step queued or running
	Failed    int `json:"failed"`    // files with a step the workers gave up
}

// Reader reads the analysis from the database.
type Reader struct {
	Pool *pgxpool.Pool
}

// Summary counts owner's files whose analysis is under way or failed. It
// starts from the tasks that are not over, which are few once the workers are
// caught up, then joins the owner's own file rows, so it never sees another
// account's files.
func (r *Reader) Summary(ctx context.Context, owner string) (Summary, error) {
	owner, err := storage.CleanOwner(owner)
	if err != nil {
		return Summary{}, err
	}
	var s Summary
	err = r.Pool.QueryRow(ctx, `
		SELECT
		  count(DISTINCT f.id) FILTER (WHERE t.state IN ('queued', 'running')),
		  count(DISTINCT f.id) FILTER (WHERE t.state = 'failed' AND NOT EXISTS (
		      SELECT 1 FROM tasks n WHERE n.kind = t.kind AND n.payload ->> 'sha256' = t.payload ->> 'sha256' AND n.id > t.id))
		FROM tasks t
		JOIN files f ON f.sha256 = decode(t.payload ->> 'sha256', 'hex')
		            AND f.owner = $1 AND f.kind = 'file' AND f.trash_id IS NULL
		WHERE t.kind = ANY($2) AND t.state IN ('queued', 'running', 'failed')
		  AND t.payload ->> 'sha256' ~ '^[0-9a-f]{64}$'`, owner, Kinds).Scan(&s.Analysing, &s.Failed)
	return s, err
}

// latest is the last task of a kind for a content.
type latest struct {
	state       string
	attempts    int
	maxAttempts int
	lastError   *string
}

// results is what the workers stored for a content.
type results struct {
	thumb, meta, photovec, textemb, category bool
	textRow, hasText, ocr                    bool
}

// File returns where the analysis of one file stands: the steps that apply
// to its type, and the ones a task or a result shows it has. f must be a file
// of the requesting account (storage.Store.Lookup).
func (r *Reader) File(ctx context.Context, f storage.File) (FileStatus, error) {
	if f.IsDir() || f.SHA256 == nil {
		return FileStatus{State: None, Steps: []Step{}}, nil
	}
	rows, err := r.Pool.Query(ctx,
		`SELECT DISTINCT ON (kind) kind, state, attempts, max_attempts, last_error FROM tasks
		 WHERE kind = ANY($1) AND payload ->> 'sha256' = $2 ORDER BY kind, id DESC`,
		Kinds, hex.EncodeToString(f.SHA256))
	if err != nil {
		return FileStatus{}, err
	}
	tasks := map[string]latest{}
	for rows.Next() {
		var kind string
		var t latest
		if err := rows.Scan(&kind, &t.state, &t.attempts, &t.maxAttempts, &t.lastError); err != nil {
			return FileStatus{}, err
		}
		tasks[kind] = t
	}
	if err := rows.Err(); err != nil {
		return FileStatus{}, err
	}
	var res results
	var hasText, hasOCR *bool
	err = r.Pool.QueryRow(ctx, `
		SELECT
		  EXISTS (SELECT 1 FROM thumbnails WHERE sha256 = $1 AND size = $2),
		  EXISTS (SELECT 1 FROM media_meta WHERE sha256 = $1),
		  EXISTS (SELECT 1 FROM `+photovec.Table+` WHERE sha256 = $1),
		  EXISTS (SELECT 1 FROM `+textemb.Table+` WHERE sha256 = $1),
		  EXISTS (SELECT 1 FROM document_categories WHERE sha256 = $1),
		  (SELECT has_text FROM document_text WHERE sha256 = $1),
		  (SELECT ocr_model IS NOT NULL FROM document_text WHERE sha256 = $1)`,
		f.SHA256, thumbs.Size).Scan(&res.thumb, &res.meta, &res.photovec, &res.textemb, &res.category, &hasText, &hasOCR)
	if err != nil && err != pgx.ErrNoRows {
		return FileStatus{}, err
	}
	res.textRow = hasText != nil
	res.hasText = hasText != nil && *hasText
	res.ocr = hasOCR != nil && *hasOCR
	return statusOf(f.MIME, tasks, res), nil
}

// statusOf lays out the steps. A step is queued, running or failed by its
// last task, done by its result, waiting when the step before it is, and
// otherwise idle.
func statusOf(mime string, tasks map[string]latest, res results) FileStatus {
	media := thumbs.IsMedia(mime, nil)
	image := media && strings.HasPrefix(mime, "image/")
	document := doctext.IsDocument(mime)
	pdf := mime == "application/pdf"
	_, ocrTask := tasks[ocr.Kind]

	var steps []Step
	byKind := map[string]string{}
	add := func(kind string, done bool, upstream ...string) {
		s := stepOf(kind, tasks, done, upstream, byKind)
		byKind[kind] = s.State
		steps = append(steps, s)
	}
	// What the text steps do once the text is known: a document without any
	// (a scan nobody read yet excepted) has nothing to index.
	textSteps := func(upstream ...string) {
		noText := res.textRow && !res.hasText && !pending(byKind, upstream...)
		for _, kind := range []string{textemb.Kind, doccat.Kind} {
			up := append([]string{}, upstream...)
			if kind == doccat.Kind {
				up = append(up, textemb.Kind)
			}
			if noText && tasks[kind].state == "" {
				byKind[kind] = Skipped
				steps = append(steps, Step{Kind: kind, State: Skipped})
				continue
			}
			done := res.textemb
			if kind == doccat.Kind {
				done = res.category
			}
			add(kind, done, up...)
		}
	}

	switch {
	case document:
		add(doctext.Kind, res.textRow)
		up := []string{doctext.Kind}
		if pdf && (res.textRow && !res.hasText || ocrTask) {
			add(ocr.Kind, res.ocr, doctext.Kind)
			up = append(up, ocr.Kind)
		}
		textSteps(up...)
	case media:
		add(thumbs.Kind, res.thumb)
		add(meta.Kind, res.meta)
		if image {
			add(ocr.Kind, res.ocr)
		}
		add(photovec.Kind, res.photovec)
		// A photo's text is only worth a step once the OCR found some.
		if image && (res.hasText || tasks[textemb.Kind].state != "" || tasks[doccat.Kind].state != "") {
			textSteps(ocr.Kind)
		}
	}
	if steps == nil {
		return FileStatus{State: None, Steps: []Step{}}
	}
	return FileStatus{State: overall(steps), Steps: steps}
}

func pending(byKind map[string]string, kinds ...string) bool {
	for _, k := range kinds {
		if s := byKind[k]; s == Waiting || s == Running {
			return true
		}
	}
	return false
}

func stepOf(kind string, tasks map[string]latest, done bool, upstream []string, byKind map[string]string) Step {
	s := Step{Kind: kind}
	t, ok := tasks[kind]
	switch {
	case ok && t.state == "running":
		s.State = Running
	case ok && t.state == "queued":
		s.State = Waiting
	case ok && t.state == "failed":
		s.State = Failed
	case ok && t.state == "done", done:
		s.State = Done
	case pending(byKind, upstream...):
		s.State = Waiting
	default:
		s.State = Idle
	}
	if ok && (s.State == Failed || s.State == Waiting && t.attempts > 0) {
		s.Attempts, s.MaxAttempts = t.attempts, t.maxAttempts
		if t.lastError != nil {
			s.Error = truncate(*t.lastError)
		}
	}
	return s
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxError {
		return string(r[:MaxError]) + "…"
	}
	return s
}

func overall(steps []Step) string {
	failed := false
	for _, s := range steps {
		switch s.State {
		case Waiting, Running:
			return Pending
		case Failed:
			failed = true
		}
	}
	if failed {
		return Problem
	}
	return Ready
}
