// Package photovec queues the vectors of what photos and videos show, and
// searches them by the meaning of a sentence (#71,
// docs/architecture/embeddings.md). The workers encode each content with
// SigLIP 2 (worker/src/worker/photovec.py) into one table per model, keyed by
// content; this package never touches the data root.
package photovec

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/thumbs"
)

// Kind is the task kind the workers run.
const Kind = "photovec"

// Model is the name the workers give the model, in embedding_models and in
// each row; the table is its own (core/migrations/0020_photo_vectors.sql).
const Model = "siglip2-base-patch16-224"

// table holds the vectors of Model. A new model is a new table, by migration.
const table = "photo_vectors_siglip2_b16_224"

// Table is the table of the vectors of Model, for the packages that only check a content has one.
const Table = table

// Search limits.
const (
	DefaultLimit = 30
	MaxLimit     = 100
	maxQuery     = 200 // characters; SigLIP 2 reads 64 tokens
	efSearch     = 100 // candidates the HNSW index keeps per search
)

// MinScore is the cosine similarity under which a photo is not returned,
// measured on the benchmark corpus (worker/tests/fixtures/bench): every
// expected photo scores 0.077 or more, while 90% of the unrelated photos and
// 78% of the photos a document query finds score less. It only removes clear
// misses: the order, not the score, says which photos match best.
const MinScore = 0.06

// Enqueue queues the vector of a stored file, unless it is not a photo or a
// video, its content already has a vector of the version the workers
// declared, or a task is already queued for it.
func Enqueue(ctx context.Context, q *tasks.Queue, f storage.File, head []byte, priority int) (bool, error) {
	if f.IsDir() || !thumbs.IsMedia(f.MIME, head) {
		return false, nil
	}
	var exists bool
	if err := q.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM `+table+` v WHERE v.sha256 = $1
		     AND v.version = (SELECT version FROM embedding_models WHERE name = $2))`,
		f.SHA256, Model).Scan(&exists); err != nil || exists {
		return false, err
	}
	sum := hex.EncodeToString(f.SHA256)
	_, ok, err := q.Enqueue(ctx, Kind, map[string]string{"sha256": sum}, tasks.EnqueueOptions{Priority: priority, DedupeKey: Kind + ":" + sum})
	return ok, err
}

// Backfill queues, at backfill priority, the vector of every live photo and
// video content without one of the version the workers declared: files
// stored before the workers made vectors, a new model version, or a task
// that was lost or failed.
func Backfill(ctx context.Context, q *tasks.Queue) (int, error) {
	rows, err := q.Pool.Query(ctx,
		`SELECT DISTINCT f.sha256, f.mime FROM files f
		 WHERE f.kind = 'file' AND f.trash_id IS NULL AND f.mime = ANY($1)
		   AND NOT EXISTS (SELECT 1 FROM `+table+` v WHERE v.sha256 = f.sha256
		       AND v.version = (SELECT version FROM embedding_models WHERE name = $2))`,
		thumbs.MediaMIMEs(), Model)
	if err != nil {
		return 0, err
	}
	files, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (storage.File, error) {
		var f storage.File
		return f, row.Scan(&f.SHA256, &f.MIME)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		ok, err := Enqueue(ctx, q, f, nil, tasks.PriorityBackfill)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// Sync runs Backfill once for each model version the workers declare: at the
// first start, and whenever the model changes. The core calls it on its
// periodic tick; it costs one query when there is nothing to do.
func Sync(ctx context.Context, q *tasks.Queue) (int, error) {
	version, due, err := embed.Due(ctx, q.Pool, Model)
	if err != nil || !due {
		return 0, err
	}
	n, err := Backfill(ctx, q)
	if err != nil {
		return n, err
	}
	return n, embed.MarkRequeued(ctx, q.Pool, Model, version)
}

// Prune deletes the vectors whose content no file has any more, in the trash
// or not. Rows only: a vector is never a file.
func Prune(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx,
		`DELETE FROM `+table+` v WHERE NOT EXISTS (
		     SELECT 1 FROM files f WHERE f.kind = 'file' AND f.sha256 = v.sha256)`)
	return tag.RowsAffected(), err
}

// Hit is a photo or video of the account and its cosine similarity to the
// query, from -1 to 1.
type Hit struct {
	storage.File
	Score float64
}

// Searcher finds photos by the meaning of a sentence.
type Searcher struct {
	Pool  *pgxpool.Pool
	Embed *embed.Client
}

// Search returns the owner's live photos and videos closest to q, best
// first, at most limit, none under MinScore. The workers encode q (an
// embed.ErrUnavailable error when they cannot); only vectors of the same
// model version are compared. filter narrows the files, while the index is
// walked. An empty q finds nothing.
func (s *Searcher) Search(ctx context.Context, owner, q string, limit int, filter storage.Filter) ([]Hit, error) {
	owner, err := storage.CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	q = strings.Join(strings.Fields(q), " ")
	if q == "" {
		return []Hit{}, nil
	}
	if utf8.RuneCountInString(q) > maxQuery {
		q = string([]rune(q)[:maxQuery])
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	vec, err := s.Embed.Query(ctx, Model, q)
	if err != nil {
		return nil, err
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The filters (version, owner, trash) apply after the index: let it go on
	// scanning until enough rows pass them, so another account's photos
	// cannot crowd this one's out.
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.ef_search', $1, true)`, strconv.Itoa(max(efSearch, limit))); err != nil {
		return nil, err
	}
	args := []any{vec.Literal(), vec.Version, owner, limit, MinScore}
	narrow, args := filter.SQL("f", args)
	rows, err := tx.Query(ctx,
		`WITH near AS MATERIALIZED (
		     SELECT v.sha256, v.embedding <=> $1::vector AS distance FROM `+table+` v
		     WHERE v.version = $2 AND EXISTS (SELECT 1 FROM files f WHERE f.sha256 = v.sha256
		           AND f.owner = $3 AND f.kind = 'file' AND f.trash_id IS NULL`+narrow+`)
		     ORDER BY v.embedding <=> $1::vector
		     LIMIT $4)
		 SELECT f.id, f.owner, f.path, f.kind, f.size, f.mime, f.sha256, f.created_at, f.modified_at, 1 - near.distance
		 FROM near JOIN files f ON f.sha256 = near.sha256
		 WHERE f.owner = $3 AND f.kind = 'file' AND f.trash_id IS NULL AND 1 - near.distance >= $5`+narrow+`
		 ORDER BY near.distance, f.path
		 LIMIT $4`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Hit, error) {
		var h Hit
		return h, row.Scan(&h.ID, &h.Owner, &h.Path, &h.Kind, &h.Size, &h.MIME, &h.SHA256, &h.CreatedAt, &h.ModifiedAt, &h.Score)
	})
}
