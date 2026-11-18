// Package albums groups an account's photos and videos into named albums.
//
// An album only refers to rows of files, by id, and never holds a copy: a
// file keeps its id when it is moved, put in the trash or restored. Every
// query reads live files only, so a photo in the trash leaves its albums and
// comes back with its restore; the purge removes it from them through the
// foreign key. Deleting an album never touches a file: nothing here goes
// through storage's writes.
package albums

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// MaxNameLen is the longest album name, in characters.
const MaxNameLen = 200

// MaxBatch is how many paths one add or remove takes.
const MaxBatch = 1000

var (
	// ErrNotFound is an album that does not exist or belongs to another account.
	ErrNotFound = errors.New("no such album")
	// ErrExists is a name the account already gives another album (case ignored).
	ErrExists = errors.New("an album already has this name")
	// ErrInvalid is an empty or too long name, or too many paths at once.
	ErrInvalid = errors.New("invalid album request")
)

// Album is one album with what its list shows.
type Album struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// Count is how many live files the album holds.
	Count int `json:"count"`
	// Cover is the newest live file of the album, for its thumbnail; nil when empty.
	Cover *storage.File `json:"-"`
}

// Service reads and writes the albums of the accounts.
type Service struct {
	Pool  *pgxpool.Pool
	Files *storage.Store
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen || strings.ContainsAny(name, "\x00\r\n") {
		return "", ErrInvalid
	}
	return name, nil
}

func uniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// Create makes an empty album.
func (s *Service) Create(ctx context.Context, owner, name string) (Album, error) {
	name, err := cleanName(name)
	if err != nil {
		return Album{}, err
	}
	a := Album{Name: name}
	err = s.Pool.QueryRow(ctx, `INSERT INTO albums (owner, name) VALUES ($1, $2) RETURNING id, created_at`, owner, name).Scan(&a.ID, &a.CreatedAt)
	if uniqueViolation(err) {
		return Album{}, ErrExists
	}
	return a, err
}

// Rename gives an album a new name.
func (s *Service) Rename(ctx context.Context, owner string, id int64, name string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE albums SET name = $3 WHERE owner = $1 AND id = $2`, owner, id, name)
	if uniqueViolation(err) {
		return ErrExists
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Delete removes an album and its references; its files stay where they are.
func (s *Service) Delete(ctx context.Context, owner string, id int64) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM albums WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// List returns the owner's albums, by name, each with its count of live files and its cover.
func (s *Service) List(ctx context.Context, owner string) ([]Album, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id, a.name, a.created_at,
		       (SELECT count(*) FROM album_items i JOIN files f ON f.id = i.file_id
		         WHERE i.album_id = a.id AND f.trash_id IS NULL),
		       (SELECT f.id FROM album_items i JOIN files f ON f.id = i.file_id
		         LEFT JOIN media_meta m ON m.sha256 = f.sha256
		         WHERE i.album_id = a.id AND f.trash_id IS NULL
		         ORDER BY COALESCE(m.taken_at, f.modified_at AT TIME ZONE 'UTC') DESC, f.path DESC LIMIT 1)
		FROM albums a WHERE a.owner = $1 ORDER BY lower(a.name), a.id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Album{}
	covers := map[int]int64{}
	for rows.Next() {
		var a Album
		var cover *int64
		if err := rows.Scan(&a.ID, &a.Name, &a.CreatedAt, &a.Count, &cover); err != nil {
			return nil, err
		}
		if cover != nil {
			covers[len(out)] = *cover
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range covers {
		f, err := s.Files.LookupID(ctx, owner, id)
		if err == nil {
			out[i].Cover = &f
		} else if !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}
	}
	return out, nil
}

// Get returns one album of the owner.
func (s *Service) Get(ctx context.Context, owner string, id int64) (Album, error) {
	all, err := s.List(ctx, owner)
	if err != nil {
		return Album{}, err
	}
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
	}
	return Album{}, ErrNotFound
}

// ids resolves paths to the ids of the owner's live files: a path of another
// account cannot be reached, a folder or a missing path is ErrNotFound.
func (s *Service) ids(ctx context.Context, owner string, paths []string) ([]int64, error) {
	if len(paths) == 0 || len(paths) > MaxBatch {
		return nil, ErrInvalid
	}
	out := make([]int64, 0, len(paths))
	for _, p := range paths {
		f, err := s.Files.Lookup(ctx, owner, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if f.Kind != "file" {
			return nil, fmt.Errorf("%s: %w", p, storage.ErrNotFound)
		}
		out = append(out, f.ID)
	}
	return out, nil
}

func (s *Service) owned(ctx context.Context, tx pgx.Tx, owner string, id int64) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM albums WHERE owner = $1 AND id = $2 FOR UPDATE`, owner, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Add puts the files at paths in the album; a file already there stays once.
func (s *Service) Add(ctx context.Context, owner string, id int64, paths []string) error {
	files, err := s.ids(ctx, owner, paths)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.owned(ctx, tx, owner, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO album_items (album_id, file_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING`, id, files)
		return err
	})
}

// Remove takes the files at paths out of the album; the files themselves stay.
func (s *Service) Remove(ctx context.Context, owner string, id int64, paths []string) error {
	files, err := s.ids(ctx, owner, paths)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.owned(ctx, tx, owner, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM album_items WHERE album_id = $1 AND file_id = ANY($2::bigint[])`, id, files)
		return err
	})
}

// Items returns a page of the album's live files, as the timeline does.
func (s *Service) Items(ctx context.Context, owner, tz string, id int64, after *storage.TimelineCursor, limit int) ([]storage.TimelineItem, error) {
	var one int
	err := s.Pool.QueryRow(ctx, `SELECT 1 FROM albums WHERE owner = $1 AND id = $2`, owner, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.Files.AlbumTimeline(ctx, owner, tz, id, after, limit)
}
