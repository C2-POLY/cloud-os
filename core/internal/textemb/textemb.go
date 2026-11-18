// Package textemb searches documents by meaning, with the vectors of their
// text (#70). The workers write them (worker/src/worker/textemb.py): a
// document's text cut into windows of 512 tokens, one vector per window, in
// the table of the model, keyed by content like the text. The workers also
// queue them, when they write a text, and re-index a new model version on
// their own. This package queues the catch-up and reads the vectors; it never
// touches the data root.
package textemb

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/encoder"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
)

// The task, the model and its table: worker.textemb.KIND, worker.models.TEXT_MODEL
// and TEXT_TABLE. Another model is another table, and a migration.
const (
	Kind  = "textemb"
	Model = "arctic-embed-m-v2.0"
	Table = "text_vectors_arctic_m_v2"
	Dims  = 768
)

// Search bounds.
const (
	DefaultLimit = 20
	MaxLimit     = 100
	// MaxQueryChars is the longest query, as worker.encoder.MAX_TEXT.
	MaxQueryChars = 1000
	// candidates is how many windows near the query are read before keeping
	// the best one per content.
	candidates = 200
	// excerptChars is how much of the best window a hit shows.
	excerptChars = 240
)

// ErrInvalid is a vector that is not the model's.
var ErrInvalid = errors.New("not a vector of the text model")

// Payload is what a textemb task carries: the content whose text to encode.
type Payload struct {
	SHA256 string `json:"sha256"`
}

// Backfill queues, at backfill priority, the vectors of every live content
// with text that has none: its task was lost or failed. The vectors of an
// older model version are queued by the workers when they start, since only
// they know the current one.
func Backfill(ctx context.Context, q *tasks.Queue) (int, error) {
	rows, err := q.Pool.Query(ctx,
		`SELECT d.sha256 FROM document_text d
		 WHERE d.has_text
		   AND EXISTS (SELECT 1 FROM files f WHERE f.kind = 'file' AND f.sha256 = d.sha256 AND f.trash_id IS NULL)
		   AND NOT EXISTS (SELECT 1 FROM `+Table+` v WHERE v.sha256 = d.sha256)`)
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

// Hit is a document of the account near the query: its best window, in
// characters of its text, and the start of that window.
type Hit struct {
	File    storage.File
	Score   float64 // cosine similarity of the best window, 1 at most
	Start   int
	End     int
	Excerpt string
}

// Search returns the owner's live documents nearest to the query vector,
// best first, at most limit (DefaultLimit, MaxLimit). The owner is the
// caller's session, never a parameter: the vectors are shared by contents,
// and only the owner's live files make them visible.
func Search(ctx context.Context, pool *pgxpool.Pool, owner string, v encoder.Vector, limit int) ([]Hit, error) {
	owner, err := storage.CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	if v.Model != Model || len(v.Values) != Dims {
		return nil, fmt.Errorf("%w: %s, %d values", ErrInvalid, v.Model, len(v.Values))
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The account's filter applies while the HNSW index is walked: with an
	// iterative scan (pgvector 0.8), Postgres walks on until enough windows
	// pass it, instead of filtering a fixed number of nearest ones.
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.ef_search = 100`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		WITH near AS MATERIALIZED (
		    SELECT v.sha256, v.start_char, v.end_char, v.embedding <=> $1::halfvec AS distance
		    FROM `+Table+` v
		    WHERE EXISTS (SELECT 1 FROM files f WHERE f.sha256 = v.sha256 AND f.owner = $2
		                  AND f.kind = 'file' AND f.trash_id IS NULL)
		    ORDER BY v.embedding <=> $1::halfvec
		    LIMIT $4
		), best AS (
		    SELECT DISTINCT ON (sha256) sha256, start_char, end_char, distance
		    FROM near ORDER BY sha256, distance
		)
		SELECT `+storage.FileColumns("f")+`, 1 - b.distance, b.start_char, b.end_char,
		       -- d.text || '': a whole copy, sliced. PostgreSQL 17.8 can cut an
		       -- accented character when it slices a compressed value in place.
		       substr(d.text || '', b.start_char + 1, LEAST(b.end_char - b.start_char, $5))
		FROM best b
		JOIN files f ON f.sha256 = b.sha256 AND f.owner = $2 AND f.kind = 'file' AND f.trash_id IS NULL
		JOIN document_text d ON d.sha256 = b.sha256
		ORDER BY b.distance, f.modified_at DESC, f.path
		LIMIT $3`, v.Literal(), owner, limit, candidates, excerptChars)
	if err != nil {
		return nil, err
	}
	hits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Hit, error) {
		var h Hit
		var err error
		h.File, err = storage.ScanFile(row, &h.Score, &h.Start, &h.End, &h.Excerpt)
		h.Excerpt = strings.TrimSpace(h.Excerpt)
		return h, err
	})
	if err != nil {
		return nil, err
	}
	return hits, tx.Commit(ctx)
}
