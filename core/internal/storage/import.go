package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ImportReport counts what Import did with each file of the source folder.
type ImportReport struct {
	Imported []string // copied and indexed
	Adopted  []string // already on disk with the same content, now indexed
	Present  []string // already indexed with the same content
	Skipped  []Problem
}

// Import copies every regular file under src into owner's space, keeping the
// folder layout and modification times. The source is only read: nothing in it
// is moved, changed or deleted. A file already indexed with the same content
// is left alone; one with other content, a name conflict, a symbolic link or a
// special file is skipped and reported.
func (s *Store) Import(ctx context.Context, owner, src string) (ImportReport, error) {
	var rep ImportReport
	owner, err := CleanOwner(owner)
	if err != nil {
		return rep, err
	}
	err = filepath.WalkDir(src, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		skip := func(why string) {
			rep.Skipped = append(rep.Skipped, Problem{Kind: "skipped", Owner: owner, Path: rel, Note: why})
		}

		if !d.Type().IsRegular() {
			skip("not a regular file (link, device or socket)")
			return nil
		}
		p, err := CleanPath(rel)
		if err != nil {
			skip(err.Error())
			return nil
		}
		return s.importOne(ctx, owner, p, name, &rep, skip)
	})
	return rep, err
}

func (s *Store) importOne(ctx context.Context, owner, p, name string, rep *ImportReport, skip func(string)) error {
	sum, _, err := hashFile(name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}

	if f, err := s.Lookup(ctx, owner, p); err == nil {
		if bytes.Equal(f.SHA256, sum) {
			rep.Present = append(rep.Present, p)
		} else {
			skip("already in the drive with other content")
		}
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	if _, err := os.Lstat(s.diskPath(owner, p)); err == nil {
		if _, err := s.adopt(ctx, owner, p, sum); err != nil {
			if errors.Is(err, ErrExists) {
				skip(err.Error())
				return nil
			}
			return err
		}
		rep.Adopted = append(rep.Adopted, p)
		return nil
	}

	info, err := os.Stat(name)
	if err != nil {
		return err
	}
	in, err := os.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = s.Put(ctx, owner, p, in, PutOptions{ModTime: info.ModTime(), ExpectSize: info.Size(), ExpectSHA256: sum})
	switch {
	case err == nil:
		rep.Imported = append(rep.Imported, p)
	case errors.Is(err, ErrExists):
		skip(err.Error())
	case errors.Is(err, ErrMismatch):
		skip("changed while being read: " + err.Error())
	default:
		return err
	}
	return nil
}
