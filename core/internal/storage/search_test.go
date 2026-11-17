package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func searchPaths(t *testing.T, s *Store, owner, q string, limit int) []string {
	t.Helper()
	files, err := s.Search(context.Background(), owner, q, limit)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

func TestSearchIgnoresAccentsCaseAndOrderOfWords(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "Photos/Été 2024/plage.jpg", "a")
	put(t, s, "enzo", "Documents/Bail appartement.pdf", "b")
	put(t, s, "enzo", "Documents/Quittance été.pdf", "c")
	put(t, s, "johan", "Été/secret.txt", "d")
	if _, err := s.Mkdir(ctx, "enzo", "Vide"); err != nil {
		t.Fatal(err)
	}

	cases := map[string][]string{
		"ete":         {"Photos/Été 2024", "Documents/Quittance été.pdf", "Photos/Été 2024/plage.jpg"},
		"PLAGE":       {"Photos/Été 2024/plage.jpg"},
		"2024 plage":  {"Photos/Été 2024/plage.jpg"},
		"bail appart": {"Documents/Bail appartement.pdf"},
		"vide":        {"Vide"},
		"":            {},
		"   ":         {},
		"introuvable": {},
		"secret":      {}, // johan's
	}
	for q, want := range cases {
		if got := searchPaths(t, s, "enzo", q, 0); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("search %q = %q, want %q", q, got, want)
		}
	}
}

func TestSearchRanksNamesFirstThenRecent(t *testing.T) {
	s := newStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	putAt := func(p string, at time.Time) {
		t.Helper()
		opt := NoExpectations()
		opt.ModTime = at
		if _, err := s.Put(context.Background(), "enzo", p, strings.NewReader(p), opt); err != nil {
			t.Fatal(err)
		}
	}
	putAt("Archives/final-rapport.pdf", old)          // name holds the word
	putAt("Rapports/notes.txt", old.AddDate(1, 0, 0)) // only the path does
	putAt("rapport.pdf", old)                         // name starts with it, older
	putAt("Rapport 2026.pdf", old.AddDate(6, 0, 0))   // name starts with it, newer
	// The folder Rapports starts with the word too, and was created just now.
	want := []string{"Rapports", "Rapport 2026.pdf", "rapport.pdf", "Archives/final-rapport.pdf", "Rapports/notes.txt"}
	got := searchPaths(t, s, "enzo", "rapport", 0)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order %q, want %q", got, want)
	}
	if got := searchPaths(t, s, "enzo", "rapport", 2); len(got) != 2 {
		t.Errorf("limit 2: %q", got)
	}
}

func TestSearchSkipsTheTrashAndTreatsLikeCharactersLiterally(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "100% fini.txt", "a")
	put(t, s, "enzo", "1000 fini.txt", "b")
	put(t, s, "enzo", "mon_fichier.txt", "c")
	put(t, s, "enzo", "monXfichier.txt", "d")
	put(t, s, "enzo", "jeté.txt", "e")
	if _, err := s.Trash(ctx, "enzo", "jeté.txt"); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{"100%": {"100% fini.txt"}, "mon_": {"mon_fichier.txt"}, "jete": {}} {
		if got := searchPaths(t, s, "enzo", q, 0); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("search %q = %q, want %q", q, got, want)
		}
	}
}
