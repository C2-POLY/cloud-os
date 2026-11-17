package storage

import (
	"context"
	"fmt"
	"strings"
)

// Search limits.
const (
	DefaultSearchLimit = 30
	MaxSearchLimit     = 100
	maxSearchWords     = 8
)

// Search finds the owner's live files and folders whose path holds every word
// of q, ignoring accents and case (cloudos_fold, migration 0012). Names that
// start with the first word come first, then names that hold it, then the
// rest; within each, the most recently modified first. An empty q finds nothing.
func (s *Store) Search(ctx context.Context, owner, q string, limit int) ([]File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(q)
	if len(words) == 0 {
		return []File{}, nil
	}
	if len(words) > maxSearchWords {
		words = words[:maxSearchWords]
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	limit = min(limit, MaxSearchLimit)

	// One condition per word, so that each can use the trigram index.
	args := []any{owner, limit}
	var where strings.Builder
	for _, w := range words {
		args = append(args, escapeLike(w))
		fmt.Fprintf(&where, ` AND cloudos_fold(path) LIKE '%%' || cloudos_fold($%d) || '%%' ESCAPE '\'`, len(args))
	}
	first := `cloudos_fold($3)`
	name := `cloudos_fold(regexp_replace(path, '^.*/', ''))`
	rows, err := s.Pool.Query(ctx, `SELECT `+fileColumns+` FROM files
		WHERE owner = $1 AND trash_id IS NULL`+where.String()+`
		ORDER BY CASE WHEN `+name+` LIKE `+first+` || '%' ESCAPE '\' THEN 0
		              WHEN `+name+` LIKE '%' || `+first+` || '%' ESCAPE '\' THEN 1
		              ELSE 2 END,
		         modified_at DESC, path
		LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
