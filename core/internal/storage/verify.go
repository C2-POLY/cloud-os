package storage

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Problem is one difference between the index and the disk.
type Problem struct {
	Kind  string // "modified", "missing" or "unindexed"
	Owner string
	Path  string
	Note  string
}

// Report is the result of Verify. It only describes; Verify never repairs.
type Report struct {
	OK       int
	Problems []Problem
}

// Clean reports whether the index and the disk agree.
func (r Report) Clean() bool { return len(r.Problems) == 0 }

// Verify recomputes the hash of every indexed file and walks the data root.
// Each file is reported as ok, modified (hash or size differs), missing (no
// longer on disk) or unindexed (on disk, not in the index). It changes nothing.
func (s *Store) Verify(ctx context.Context) (Report, error) {
	var rep Report
	indexed := map[string]bool{}

	rows, err := s.Pool.Query(ctx, `SELECT `+fileColumns+` FROM files ORDER BY owner, path`)
	if err != nil {
		return rep, err
	}
	var files []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			rows.Close()
			return rep, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		indexed[f.Owner+"/"+f.Path] = true
		disk := s.diskPath(f.Owner, f.Path)
		info, err := os.Lstat(disk)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			rep.Problems = append(rep.Problems, Problem{Kind: "missing", Owner: f.Owner, Path: f.Path})
			continue
		case err != nil:
			return rep, err
		case f.IsDir() && info.IsDir():
			rep.OK++
			continue
		case f.IsDir():
			rep.Problems = append(rep.Problems, Problem{Kind: "modified", Owner: f.Owner, Path: f.Path, Note: "no longer a folder"})
			continue
		case !info.Mode().IsRegular():
			rep.Problems = append(rep.Problems, Problem{Kind: "modified", Owner: f.Owner, Path: f.Path, Note: "no longer a regular file"})
			continue
		}
		sum, _, err := hashFile(disk)
		if err != nil {
			return rep, err
		}
		if info.Size() != f.Size || !bytes.Equal(sum, f.SHA256) {
			rep.Problems = append(rep.Problems, Problem{Kind: "modified", Owner: f.Owner, Path: f.Path, Note: "content differs from the index"})
			continue
		}
		rep.OK++
	}

	err = filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.Root, p)
		rel = norm.NFC.String(filepath.ToSlash(rel))
		if d.IsDir() {
			if rel == ".cloudos" {
				return filepath.SkipDir
			}
			return nil
		}
		if indexed[rel] {
			return nil
		}
		owner, path, _ := strings.Cut(rel, "/")
		if path == "" {
			owner, path = "", rel
		}
		rep.Problems = append(rep.Problems, Problem{Kind: "unindexed", Owner: owner, Path: path})
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rep, err
	}

	sort.Slice(rep.Problems, func(i, j int) bool {
		a, b := rep.Problems[i], rep.Problems[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.Path < b.Path
	})
	return rep, nil
}
