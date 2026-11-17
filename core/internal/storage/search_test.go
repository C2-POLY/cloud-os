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

func TestNotifyTellsWhatHappened(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	var got []string
	s.Notify = func(_ context.Context, owner, kind, path string) { got = append(got, owner+" "+kind+" "+path) }
	put(t, s, "enzo", "a.txt", "1")
	opt := NoExpectations()
	opt.Replace = true
	if _, err := s.Put(ctx, "enzo", "a.txt", strings.NewReader("2"), opt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "enzo", "b.txt", strings.NewReader("3"), opt); err != nil {
		t.Fatal(err) // Replace on a new path stores it: "added"
	}
	item, err := s.Trash(ctx, "enzo", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "enzo", "b.txt", strings.NewReader("x"), NoExpectations()); err == nil {
		t.Fatal("expected ErrExists") // a failed write tells nothing
	}
	want := "[enzo added a.txt enzo replaced a.txt enzo added b.txt enzo trashed a.txt enzo restored a.txt]"
	if fmt.Sprint(got) != want {
		t.Errorf("events %v\nwant %s", got, want)
	}
}

func TestRecentListsTheNewestFiles(t *testing.T) {
	s := newStore(t)
	put(t, s, "enzo", "Photos/a.jpg", "a")
	put(t, s, "enzo", "b.txt", "b")
	put(t, s, "enzo", "c.txt", "c")
	got, err := s.Recent(context.Background(), "enzo", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "c.txt" || got[1].Path != "b.txt" {
		t.Errorf("recent %+v", got)
	}
}
