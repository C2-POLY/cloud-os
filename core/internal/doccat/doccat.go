// Package doccat reads and corrects the category of documents (#73,
// docs/architecture/documents.md).
//
// The workers classify each content when they write the vectors of its text
// (worker/src/worker/doccat.py): one row of document_categories per content,
// compared with sentences that describe each category, no language model. A
// member's choice for one of their files is kept apart, in file_categories,
// and wins over it: a new classification never overwrites it, and it never
// reaches another account that has the same content. This package queues the
// classification again when the workers declare a new version of it (Sync),
// lists an account's documents by category and records the choices. It
// never touches the data root.
package doccat

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

// Kind is the task that classifies a content again from its stored vectors
// (worker.doccat.KIND); Name is the classifier in embedding_models
// (worker.doccat.NAME), whose version the workers declare.
const (
	Kind = "doccat"
	Name = "doccat"
)

// Categories are the ids of the categories, in the order the web shows them:
// those of worker.doccat.CATEGORIES and of the CHECK of
// core/migrations/0022_document_categories.sql. Other is the last one, for
// documents near none of the others.
var Categories = []string{"factures", "contrats", "impots", "sante", "banque", "travail", "administratif", Other}

// Other is the category of a document that matches none of the others.
const Other = "autre"

// Bounds of a page of documents.
const (
	DefaultLimit = 60
	MaxLimit     = 200
)

// ErrInvalid is a category that is not one of Categories.
var ErrInvalid = errors.New("unknown category")

// Valid reports whether c is one of Categories.
func Valid(c string) bool { return slices.Contains(Categories, c) }

// Payload is what a doccat task carries: the content to classify again.
type Payload struct {
	SHA256 string `json:"sha256"`
}

// Backfill queues, at backfill priority, the classification of every live
// content that has vectors of the version of Arctic the workers declared, and
// no category of the version of the classifier they declared: a new sentence
// or threshold, documents indexed before the categories, a lost task. A
// content whose vectors are not written yet is classified by the workers when
// they write them.
func Backfill(ctx context.Context, q *tasks.Queue) (int, error) {
	rows, err := q.Pool.Query(ctx,
		`SELECT v.sha256 FROM `+textemb.Table+` v
		 WHERE v.chunk = 0
		   AND v.version = (SELECT version FROM embedding_models WHERE name = $1)
		   AND EXISTS (SELECT 1 FROM files f WHERE f.kind = 'file' AND f.sha256 = v.sha256 AND f.trash_id IS NULL)
		   AND NOT EXISTS (SELECT 1 FROM document_categories c WHERE c.sha256 = v.sha256
		       AND c.version = (SELECT version FROM embedding_models WHERE name = $2))`,
		textemb.Model, Name)
	if err != nil {
		return 0, err
	}
	shas, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sha := range shas {
		sum := hex.EncodeToString(sha)
		_, ok, err := q.Enqueue(ctx, Kind, Payload{SHA256: sum}, tasks.EnqueueOptions{Priority: tasks.PriorityBackfill, DedupeKey: Kind + ":" + sum})
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// Sync runs Backfill once for each version of the classifier the workers
// declare: at the first start, and whenever its sentences, its threshold or
// the vectors' model change. The core calls it on its periodic tick; it
// costs one query when there is nothing to do.
func Sync(ctx context.Context, q *tasks.Queue) (int, error) {
	version, due, err := embed.Due(ctx, q.Pool, Name)
	if err != nil || !due {
		return 0, err
	}
	n, err := Backfill(ctx, q)
	if err != nil {
		return n, err
	}
	return n, embed.MarkRequeued(ctx, q.Pool, Name, version)
}

// Document is a live document of an account with its category: the
// member's choice when there is one, else the automatic one.
type Document struct {
	File     storage.File
	Category string
	// Auto is the workers' category; nil until they classify the content.
	Auto *string
	// Corrected is set when the member chose the category.
	Corrected bool
}

// Count is how many documents of an account are in a category.
type Count struct {
	Category string `json:"id"`
	Count    int    `json:"count"`
}

// Cursor is where the next page of a list starts: after the document
// modified at At, at Path.
type Cursor struct {
	At   time.Time
	Path string
}

// Store reads and writes the categories of the accounts' documents.
type Store struct {
	Pool *pgxpool.Pool
}

// documents are an account's live files whose content has text ($1, the
// owner), with their automatic category (dc) and the member's choice (fc).
const documents = `
	FROM files f
	JOIN document_text d ON d.sha256 = f.sha256 AND d.has_text
	LEFT JOIN document_categories dc ON dc.sha256 = f.sha256
	LEFT JOIN file_categories fc ON fc.file_id = f.id
	WHERE f.owner = $1 AND f.kind = 'file' AND f.trash_id IS NULL`

func scanDocument(row pgx.Row) (Document, error) {
	var d Document
	var auto, chosen *string
	f, err := storage.ScanFile(row, &auto, &chosen)
	if err != nil {
		return Document{}, err
	}
	d.File, d.Auto = f, auto
	switch {
	case chosen != nil:
		d.Category, d.Corrected = *chosen, true
	case auto != nil:
		d.Category = *auto
	}
	return d, nil
}

// Summary counts the owner's documents in each category, in the order of
// Categories (0 for an empty one), and the documents with text the workers
// have not classified yet (pending).
func (s *Store) Summary(ctx context.Context, owner string) (counts []Count, pending int, err error) {
	if owner, err = storage.CleanOwner(owner); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT COALESCE(fc.category, dc.category), count(*)`+documents+` GROUP BY 1`, owner)
	if err != nil {
		return nil, 0, err
	}
	by := map[string]int{}
	var c *string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&c, &n}, func() error {
		if c == nil {
			pending = n
		} else {
			by[*c] = n
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}
	counts = make([]Count, len(Categories))
	for i, c := range Categories {
		counts[i] = Count{Category: c, Count: by[c]}
	}
	return counts, pending, nil
}

// List returns the owner's documents in category, the most recently modified
// first, from after (nil: the first page), at most limit (DefaultLimit,
// MaxLimit).
func (s *Store) List(ctx context.Context, owner, category string, after *Cursor, limit int) ([]Document, error) {
	owner, err := storage.CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	if !Valid(category) {
		return nil, ErrInvalid
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	var at *time.Time
	path := ""
	if after != nil {
		at, path = &after.At, after.Path
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+storage.FileColumns("f")+`, dc.category, fc.category`+documents+`
		  AND COALESCE(fc.category, dc.category) = $2
		  AND ($3::timestamptz IS NULL OR (f.modified_at, f.path) < ($3, $4))
		ORDER BY f.modified_at DESC, f.path DESC
		LIMIT $5`, owner, category, at, path, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Document, error) { return scanDocument(row) })
}

// Get returns the owner's live document at path: storage.ErrNotFound when
// there is none, or when its content has no text (a photo, a scan before its
// OCR).
func (s *Store) Get(ctx context.Context, owner, path string) (Document, error) {
	owner, err := storage.CleanOwner(owner)
	if err != nil {
		return Document{}, err
	}
	if path, err = storage.CleanPath(path); err != nil {
		return Document{}, err
	}
	d, err := scanDocument(s.Pool.QueryRow(ctx,
		`SELECT `+storage.FileColumns("f")+`, dc.category, fc.category`+documents+` AND f.path = $2`, owner, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, fmt.Errorf("%w: no document at %s", storage.ErrNotFound, path)
	}
	return d, err
}

// Set records the owner's choice of category for the document at path, or
// forgets it when category is nil: the document goes back to its automatic
// category. The choice is kept by file: it follows the file when it is moved,
// put in the trash, restored or replaced, and goes with its purge.
func (s *Store) Set(ctx context.Context, owner, path string, category *string) (Document, error) {
	if category != nil && !Valid(*category) {
		return Document{}, ErrInvalid
	}
	d, err := s.Get(ctx, owner, path)
	if err != nil {
		return Document{}, err
	}
	if category == nil {
		_, err = s.Pool.Exec(ctx, `DELETE FROM file_categories WHERE file_id = $1`, d.File.ID)
	} else {
		_, err = s.Pool.Exec(ctx,
			`INSERT INTO file_categories (file_id, category) VALUES ($1, $2)
			 ON CONFLICT (file_id) DO UPDATE SET category = EXCLUDED.category, set_at = now()`,
			d.File.ID, *category)
	}
	if err != nil {
		return Document{}, err
	}
	return s.Get(ctx, owner, path)
}
