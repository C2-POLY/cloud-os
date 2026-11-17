package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidRequest is returned for a malformed hash or too many of them.
var ErrInvalidRequest = errors.New("invalid request")

// MaxKnown caps how many hashes one Known call checks.
const MaxKnown = 1000

// Known returns, among sums, the contents the owner already has in a live file.
// It looks at the owner's files only: an unknown hash says nothing of other accounts.
func (s *Store) Known(ctx context.Context, owner string, sums [][]byte) ([][]byte, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	if len(sums) == 0 {
		return [][]byte{}, nil
	}
	if len(sums) > MaxKnown {
		return nil, fmt.Errorf("%w: at most %d hashes at once", ErrInvalidRequest, MaxKnown)
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT DISTINCT sha256 FROM files
		 WHERE owner = $1 AND kind = 'file' AND trash_id IS NULL AND sha256 = ANY($2)`, owner, sums)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]byte{}
	for rows.Next() {
		var sum []byte
		if err := rows.Scan(&sum); err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

// DedupeResult says what Dedupe did.
type DedupeResult int

const (
	// DedupeCopied: the content was copied to the path from another of the owner's files.
	DedupeCopied DedupeResult = iota
	// DedupePresent: the path already holds exactly this content; nothing was done.
	DedupePresent
)

// Dedupe stores at owner/p a content the owner already has, without the
// client sending it: the bytes are copied from one of the owner's live files
// with that hash, read again and checked against it while copying. A source
// changed on disk behind the product's back is skipped for the next one. It
// returns ErrNotFound when no live file of the owner has the content (the
// client then sends it), and ErrExists when p holds another content.
func (s *Store) Dedupe(ctx context.Context, owner, p string, sum []byte, modTime time.Time) (File, DedupeResult, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return File{}, 0, err
	}
	if p, err = CleanPath(p); err != nil {
		return File{}, 0, err
	}
	if len(sum) != 32 {
		return File{}, 0, fmt.Errorf("%w: sha256 must be 64 hex characters", ErrInvalidRequest)
	}
	if cur, err := s.Lookup(ctx, owner, p); err == nil {
		if !cur.IsDir() && bytes.Equal(cur.SHA256, sum) {
			return cur, DedupePresent, nil
		}
		return File{}, 0, fmt.Errorf("%w: %s/%s", ErrExists, owner, p)
	} else if !errors.Is(err, ErrNotFound) {
		return File{}, 0, err
	}

	rows, err := s.Pool.Query(ctx,
		`SELECT path, size FROM files
		 WHERE owner = $1 AND kind = 'file' AND trash_id IS NULL AND sha256 = $2
		 ORDER BY id LIMIT 5`, owner, sum)
	if err != nil {
		return File{}, 0, err
	}
	type source struct {
		path string
		size int64
	}
	var sources []source
	for rows.Next() {
		var src source
		if err := rows.Scan(&src.path, &src.size); err != nil {
			rows.Close()
			return File{}, 0, err
		}
		sources = append(sources, src)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return File{}, 0, err
	}

	for _, src := range sources {
		r, _, err := s.Open(ctx, owner, src.path)
		if err != nil {
			continue // moved or trashed meanwhile
		}
		f, err := s.Put(ctx, owner, p, r, PutOptions{ModTime: modTime, ExpectSize: src.size, ExpectSHA256: sum})
		r.Close()
		if errors.Is(err, ErrMismatch) {
			continue // the source no longer holds what the index says
		}
		if err != nil {
			return File{}, 0, err
		}
		return f, DedupeCopied, nil
	}
	return File{}, 0, fmt.Errorf("%w: no file of %s has this content", ErrNotFound, owner)
}
