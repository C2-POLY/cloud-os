package storage

import (
	"context"
	"fmt"
	"slices"
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

// withText stores a file and the text the workers would have extracted from it
// (document_text, keyed by the file's content).
func withText(t *testing.T, s *Store, owner, p, text string) File {
	t.Helper()
	f := put(t, s, owner, p, text)
	if _, err := s.Pool.Exec(context.Background(),
		`INSERT INTO document_text (sha256, source, text, chars, truncated, pages, has_text)
		 VALUES ($1, 'text', $2, length($2), false, NULL, true) ON CONFLICT DO NOTHING`, f.SHA256, text); err != nil {
		t.Fatal(err)
	}
	return f
}

func searchContent(t *testing.T, s *Store, owner, q string, limit int) []ContentHit {
	t.Helper()
	hits, err := s.SearchContent(context.Background(), owner, q, limit, Filter{})
	if err != nil {
		t.Fatalf("search content %q: %v", q, err)
	}
	return hits
}

func hitPaths(hits []ContentHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Path
	}
	return out
}

// marked writes an extract with its words found in brackets: "le [contrat] de".
func marked(parts []SnippetPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Hit {
			b.WriteString("[" + p.Text + "]")
		} else {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func TestSearchContentFollowsFrenchRulesAccentsAndCase(t *testing.T) {
	s := newStore(t)
	withText(t, s, "enzo", "Bail.txt", "Le contrat de location de l'appartement est conclu pour trois ans.")
	withText(t, s, "enzo", "EDF.txt", "Facture d'ÉLECTRICITÉ du mois de mars.\nMontant facturé : 42 euros.")
	withText(t, s, "enzo", "Courses.txt", "Pain, lait, œufs.")
	put(t, s, "enzo", "contrat.txt", "") // a name is not a content

	cases := map[string][]string{
		"contrats":               {"Bail.txt"}, // French stem
		"CONTRAT":                {"Bail.txt"},
		"electricite":            {"EDF.txt"}, // accents ignored
		"Électricité":            {"EDF.txt"},
		"factures":               {"EDF.txt"}, // "Facture" and "facturé"
		"oeufs":                  {"Courses.txt"},
		"appartements location":  {"Bail.txt"}, // every word, in any order
		"contrat electricite":    {},
		`"contrat de location"`:  {"Bail.txt"}, // a phrase
		`"location de contrat"`:  {},
		"contrat -appartement":   {},
		"contrat -vente":         {"Bail.txt"},
		"-vente":                 {}, // nothing to look for
		"le la de":               {}, // only stop words
		"":                       {},
		"   ":                    {},
		"introuvable":            {},
		"contrat or electricite": {"Bail.txt", "EDF.txt"},
	}
	for q, want := range cases {
		got := hitPaths(searchContent(t, s, "enzo", q, 0))
		slices.Sort(got)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("search content %q = %q, want %q", q, got, want)
		}
	}
}

func TestSearchContentRanksDenseContentsFirst(t *testing.T) {
	s := newStore(t)
	filler := strings.Repeat("une phrase sans rapport avec la recherche. ", 300)
	withText(t, s, "enzo", "Relevé.txt", filler+"Virement au notaire. "+filler)
	withText(t, s, "enzo", "Acte.txt", "Acte de vente signé chez le notaire. Le notaire a lu l'acte.")
	withText(t, s, "enzo", "Lettre.txt", "Lettre du notaire, puis "+strings.Repeat("des nouvelles. ", 40))
	if got := hitPaths(searchContent(t, s, "enzo", "notaire", 0)); fmt.Sprint(got) != "[Acte.txt Lettre.txt Relevé.txt]" {
		t.Errorf("order %q", got)
	}
	if got := searchContent(t, s, "enzo", "notaire", 2); len(got) != 2 {
		t.Errorf("limit 2: %d hits", len(got))
	}
}

func TestSearchContentMarksTheWordsInTheExtract(t *testing.T) {
	s := newStore(t)
	filler := strings.Repeat("lorem ipsum dolor sit amet ", 15000) // 405 000 characters
	withText(t, s, "enzo", "EDF.txt", "Facture d'ÉLECTRICITÉ\n\f\ndu mois de mars.")
	withText(t, s, "enzo", "Manuel.txt", filler+"Le notaire a signé l'acte de vente. "+filler)
	withText(t, s, "enzo", "Haras.txt", "Visite du haras : les chevaux dorment.")

	for q, want := range map[string]string{
		// Original case and accents, page break folded into a space.
		"electricite": "Facture d'[ÉLECTRICITÉ] du mois de mars",
		// Far into a long text: the window around the first word found.
		"notaire vente": "dolor sit amet Le [notaire] a signé l'acte de [vente]. lorem ipsum dolor",
		// 'cheval' does not start "chevaux": the whole text is read instead.
		"cheval": "Visite du haras : les [chevaux] dorment",
	} {
		hits := searchContent(t, s, "enzo", q, 0)
		if len(hits) != 1 {
			t.Fatalf("%q: %d hits", q, len(hits))
		}
		if got := marked(hits[0].Snippet); got != want {
			t.Errorf("%q extract\n got %q\nwant %q", q, got, want)
		}
	}
}

func TestSearchContentSkipsTheTrashAndOtherAccounts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	withText(t, s, "enzo", "Plombier.txt", "Facture du plombier")
	withText(t, s, "johan", "Copie.txt", "Facture du plombier") // the same content, one row
	withText(t, s, "johan", "Garage.txt", "Facture du garagiste")

	check := func(owner string, want ...string) {
		t.Helper()
		got := hitPaths(searchContent(t, s, owner, "facture", 0))
		slices.Sort(got)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s finds %q, want %q", owner, got, want)
		}
	}
	check("enzo", "Plombier.txt")
	check("johan", "Copie.txt", "Garage.txt")

	item, err := s.Trash(ctx, "enzo", "Plombier.txt")
	if err != nil {
		t.Fatal(err)
	}
	check("enzo")
	check("johan", "Copie.txt", "Garage.txt")
	if _, err := s.Restore(ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	check("enzo", "Plombier.txt")
}

func TestSearchContentListsEveryFileOfAContent(t *testing.T) {
	s := newStore(t)
	withText(t, s, "enzo", "Contrats/bail.txt", "Le bail de l'appartement")
	withText(t, s, "enzo", "Archives/bail copie.txt", "Le bail de l'appartement")
	hits := searchContent(t, s, "enzo", "bail", 0)
	got := hitPaths(hits)
	slices.Sort(got)
	if fmt.Sprint(got) != "[Archives/bail copie.txt Contrats/bail.txt]" {
		t.Fatalf("hits %q", got)
	}
	for _, h := range hits {
		if marked(h.Snippet) != "[bail] de l'appartement" { // a short word drops at the edge
			t.Errorf("%s extract %q", h.Path, marked(h.Snippet))
		}
	}
}

func TestSearchContentIndexesAHugeSheet(t *testing.T) {
	s := newStore(t)
	f := put(t, s, "enzo", "Relevés.csv", "x")
	// 1 000 000 characters of distinct numbers: their tsvector would exceed
	// 1 MB, so only the start is indexed, and the worker's INSERT still works.
	if _, err := s.Pool.Exec(context.Background(),
		`INSERT INTO document_text (sha256, source, text, chars, truncated, pages, has_text)
		 SELECT $1, 'text', t, length(t), false, NULL, true
		 FROM (SELECT string_agg(g::text, E'\t') AS t FROM generate_series(100000, 242856) g) x`, f.SHA256); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := hitPaths(searchContent(t, s, "enzo", "100042", 0)); fmt.Sprint(got) != "[Relevés.csv]" {
		t.Errorf("a number near the start: %q", got)
	}
	if got := searchContent(t, s, "enzo", "242000", 0); len(got) != 0 {
		t.Errorf("a number past the indexed start: %q", hitPaths(got))
	}
}

func TestFirstWordPatternFindsTheWordWithItsAccents(t *testing.T) {
	s := newStore(t)
	for _, c := range []struct {
		lexemes []string
		text    string
		at      int
	}{
		{[]string{"electricit"}, "La facture d'ÉLECTRICITÉ", 14},
		{[]string{"coeur"}, "Au cœur du sujet", 4},
		{[]string{"contrat", "locat"}, "Une location, un contrat", 5},
		{[]string{"port"}, "Les portefeuilles, la porte", 23}, // at most 8 letters after the stem
		{[]string{"a.b"}, "axb puis a.b", 10},                 // metacharacters taken literally
		{[]string{"rien"}, "aucun mot", 0},
	} {
		var at int
		if err := s.Pool.QueryRow(context.Background(), `SELECT regexp_instr($1, $2, 1, 1, 0, 'i')`,
			c.text, firstWordPattern(c.lexemes)).Scan(&at); err != nil {
			t.Fatalf("%v: %v", c.lexemes, err)
		}
		if at != c.at {
			t.Errorf("%v in %q at %d, want %d", c.lexemes, c.text, at, c.at)
		}
	}
}

func TestWantedWordsDropsExclusionsAndOr(t *testing.T) {
	got := wantedWords(strings.Fields(`or contrat -vente "bail loyer" or quittance -"acte de vente" fin or`))
	if want := `[or contrat "bail loyer" quittance fin or]`; fmt.Sprint(got) != want {
		t.Errorf("wanted %v, want %s", got, want)
	}
}

func TestSplitSnippetSetsTheWordsFoundApart(t *testing.T) {
	got := splitSnippet("\x01Le\x02 contrat\n\f\nde  \x01location\x02 … \x01bail\x02")
	want := []SnippetPart{{"Le", true}, {" contrat de ", false}, {"location", true}, {" … ", false}, {"bail", true}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("parts %v, want %v", got, want)
	}
	if got := splitSnippet(""); got == nil || len(got) != 0 {
		t.Errorf("empty: %#v", got)
	}
}

func TestSearchNamesTellsHowTheNameMatchedAndFilters(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	put(t, s, "enzo", "Plage/photo.jpg", "\xFF\xD8\xFF\xE0 une photo")
	put(t, s, "enzo", "Documents/la plage.txt", "un texte")
	put(t, s, "enzo", "Documents/plage.txt", "un autre texte")
	hits, err := s.SearchNames(ctx, "enzo", "plage", 0, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, h := range hits {
		got[h.Path] = h.Tier
	}
	want := map[string]int{"Plage": TierStart, "Documents/plage.txt": TierStart, "Documents/la plage.txt": TierName, "Plage/photo.jpg": TierPath}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tiers %v, want %v", got, want)
	}

	paths := func(f Filter) []string {
		hits, err := s.SearchNames(ctx, "enzo", "plage", 0, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.Path)
		}
		slices.Sort(out)
		return out
	}
	if got := paths(Filter{Type: TypePhotos}); fmt.Sprint(got) != "[Plage/photo.jpg]" {
		t.Errorf("photos: %v", got)
	}
	if got := paths(Filter{Type: TypeDocs}); fmt.Sprint(got) != "[Documents/la plage.txt Documents/plage.txt]" {
		t.Errorf("documents: %v", got)
	}
	// Today is in the period; folders are left out of a period too.
	today := time.Now().UTC()
	if got := paths(Filter{From: today.AddDate(0, 0, -1), To: today.AddDate(0, 0, 1)}); len(got) != 3 {
		t.Errorf("today: %v", got)
	}
	if got := paths(Filter{To: today.AddDate(-1, 0, 0)}); len(got) != 0 {
		t.Errorf("a year ago: %v", got)
	}

	content, err := s.SearchContent(ctx, "enzo", "plage", 0, Filter{Type: TypePhotos})
	if err != nil || len(content) != 0 {
		t.Errorf("content of photos: %v, %v", content, err)
	}
}
