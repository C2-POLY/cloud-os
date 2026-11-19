package doccat_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/C2-POLY/cloud-os/core/internal/doccat"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

type env struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	files *storage.Store
	docs  *doccat.Store
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	return env{
		ctx:   context.Background(),
		pool:  pool,
		files: &storage.Store{Root: t.TempDir(), Pool: pool},
		docs:  &doccat.Store{Pool: pool},
	}
}

func sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func (e env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

// put stores a file whose content is content, modified at the given time.
func (e env) put(t *testing.T, owner, path, content string, at time.Time) storage.File {
	t.Helper()
	opt := storage.NoExpectations()
	opt.ModTime = at
	f, err := e.files.Put(e.ctx, owner, path, strings.NewReader(content), opt)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// text writes what the text task writes for content: with words or not.
func (e env) text(t *testing.T, content string, hasText bool) {
	t.Helper()
	e.exec(t, `INSERT INTO document_text (sha256, source, text, chars, truncated, has_text)
		VALUES ($1, 'text', $2, $3, false, $4) ON CONFLICT DO NOTHING`, sum(content), content, len([]rune(content)), hasText)
}

// classify writes what the workers write for content: its category, of a version.
func (e env) classify(t *testing.T, content, category, version string) {
	t.Helper()
	e.exec(t, `INSERT INTO document_categories (sha256, category, score, margin, version) VALUES ($1, $2, 0.4, 0.1, $3)
		ON CONFLICT (sha256) DO UPDATE SET category = EXCLUDED.category, version = EXCLUDED.version`,
		sum(content), category, version)
}

// document stores a document of owner: its file, its text, and its
// automatic category unless category is "".
func (e env) document(t *testing.T, owner, path, content, category string, at time.Time) storage.File {
	t.Helper()
	f := e.put(t, owner, path, content, at)
	e.text(t, content, true)
	if category != "" {
		e.classify(t, content, category, "c1")
	}
	return f
}

var day = time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)

const (
	lease   = "Contrat de location de l'appartement"
	invoice = "Facture d'électricité de mars"
	payslip = "Bulletin de paie de mars"
)

func list(t *testing.T, e env, owner, category string) []string {
	t.Helper()
	docs, err := e.docs.List(e.ctx, owner, category, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.File.Path
	}
	return out
}

func counts(t *testing.T, e env, owner string) (map[string]int, int) {
	t.Helper()
	got, pending, err := e.docs.Summary(e.ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(doccat.Categories) {
		t.Fatalf("%d counts, want one per category", len(got))
	}
	out := map[string]int{}
	for i, c := range got {
		if c.Category != doccat.Categories[i] {
			t.Fatalf("count %d is %s, want %s: the order of Categories", i, c.Category, doccat.Categories[i])
		}
		if c.Count > 0 {
			out[c.Category] = c.Count
		}
	}
	return out, pending
}

func TestTheCategoriesAreThoseOfTheMigration(t *testing.T) {
	e := newEnv(t)
	e.put(t, "enzo", "a.txt", "a", day)
	e.text(t, "a", true)
	for _, c := range doccat.Categories {
		if !doccat.Valid(c) {
			t.Errorf("%s is not valid", c)
		}
		e.classify(t, "a", c, "c1")
		e.exec(t, `INSERT INTO file_categories (file_id, category) SELECT id, $1 FROM files WHERE path = 'a.txt'
			ON CONFLICT (file_id) DO UPDATE SET category = EXCLUDED.category`, c)
	}
	if doccat.Categories[len(doccat.Categories)-1] != doccat.Other || doccat.Valid("inconnue") {
		t.Error("autre comes last; an unknown category is not valid")
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE document_categories SET category = 'inconnue'`); err == nil {
		t.Error("the database took an unknown category")
	}
}

func TestDocumentsAreListedByCategory(t *testing.T) {
	e := newEnv(t)
	e.document(t, "enzo", "Logement/bail.pdf", lease, "contrats", day)
	e.document(t, "enzo", "Factures/edf.pdf", invoice, "factures", day.Add(time.Hour))
	e.document(t, "enzo", "Travail/paie.pdf", payslip, "travail", day)
	e.document(t, "enzo", "Nouveau/scan.pdf", "Un document pas encore classé", "", day) // pending
	// Neither a photo (its text row has no words) nor a file without text is a document.
	e.put(t, "enzo", "Photos/plage.jpg", "jpeg", day)
	e.text(t, "jpeg", false)
	e.put(t, "enzo", "archive.zip", "zip", day)

	got, pending := counts(t, e, "enzo")
	want := map[string]int{"contrats": 1, "factures": 1, "travail": 1}
	if !mapsEqual(got, want) || pending != 1 {
		t.Errorf("summary = %v, %d pending; want %v, 1 pending", got, pending, want)
	}
	if l := list(t, e, "enzo", "factures"); !slices.Equal(l, []string{"Factures/edf.pdf"}) {
		t.Errorf("factures = %v", l)
	}
	if l := list(t, e, "enzo", "banque"); len(l) != 0 {
		t.Errorf("banque = %v, want none", l)
	}
	if _, err := e.docs.List(e.ctx, "enzo", "inconnue", nil, 0); !errors.Is(err, doccat.ErrInvalid) {
		t.Errorf("unknown category: %v", err)
	}
	d, err := e.docs.Get(e.ctx, "enzo", "Factures/edf.pdf")
	if err != nil || d.Category != "factures" || d.Auto == nil || *d.Auto != "factures" || d.Corrected {
		t.Errorf("edf = %+v, %v", d, err)
	}
	if d, err := e.docs.Get(e.ctx, "enzo", "Nouveau/scan.pdf"); err != nil || d.Category != "" || d.Auto != nil {
		t.Errorf("pending document = %+v, %v", d, err)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestAListPagesFromTheNewest(t *testing.T) {
	e := newEnv(t)
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		at := day.Add(time.Duration(i) * time.Hour)
		if name == "e" {
			at = day.Add(3 * time.Hour) // the same date as d: the path breaks the tie
		}
		e.document(t, "enzo", "Factures/"+name+".pdf", "Facture "+name, "factures", at)
	}
	var seen []string
	var after *doccat.Cursor
	for range 4 {
		docs, err := e.docs.List(e.ctx, "enzo", "factures", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range docs {
			seen = append(seen, d.File.Path)
		}
		if len(docs) < 2 {
			break
		}
		last := docs[len(docs)-1].File
		after = &doccat.Cursor{At: last.ModifiedAt, Path: last.Path}
	}
	want := []string{"Factures/e.pdf", "Factures/d.pdf", "Factures/c.pdf", "Factures/b.pdf", "Factures/a.pdf"}
	if !slices.Equal(seen, want) {
		t.Errorf("pages = %v, want %v", seen, want)
	}
}

func TestACorrectionWinsAndOutlivesANewClassification(t *testing.T) {
	e := newEnv(t)
	e.document(t, "enzo", "Factures/edf.pdf", invoice, "factures", day)
	contrats := "contrats"
	d, err := e.docs.Set(e.ctx, "enzo", "Factures/edf.pdf", &contrats)
	if err != nil || d.Category != "contrats" || d.Auto == nil || *d.Auto != "factures" || !d.Corrected {
		t.Fatalf("set = %+v, %v", d, err)
	}
	if l := list(t, e, "enzo", "factures"); len(l) != 0 {
		t.Errorf("factures = %v: the corrected document left it", l)
	}
	if l := list(t, e, "enzo", "contrats"); !slices.Equal(l, []string{"Factures/edf.pdf"}) {
		t.Errorf("contrats = %v", l)
	}

	// The workers classify the content again (a new version of the sentences).
	e.classify(t, invoice, "banque", "c2")
	d, err = e.docs.Get(e.ctx, "enzo", "Factures/edf.pdf")
	if err != nil || d.Category != "contrats" || *d.Auto != "banque" || !d.Corrected {
		t.Errorf("after a new classification: %+v, %v", d, err)
	}
	// Choosing again replaces the choice; nil goes back to the automatic one.
	sante := "sante"
	if d, err := e.docs.Set(e.ctx, "enzo", "Factures/edf.pdf", &sante); err != nil || d.Category != "sante" {
		t.Errorf("second choice: %+v, %v", d, err)
	}
	if d, err := e.docs.Set(e.ctx, "enzo", "Factures/edf.pdf", nil); err != nil || d.Category != "banque" || d.Corrected {
		t.Errorf("back to automatic: %+v, %v", d, err)
	}
}

func TestACorrectionStaysWithItsAccount(t *testing.T) {
	e := newEnv(t)
	// The same content in two accounts: one automatic category, shared.
	e.document(t, "enzo", "Factures/edf.pdf", invoice, "factures", day)
	e.document(t, "johan", "edf.pdf", invoice, "", day)
	contrats := "contrats"
	if _, err := e.docs.Set(e.ctx, "enzo", "Factures/edf.pdf", &contrats); err != nil {
		t.Fatal(err)
	}
	if l := list(t, e, "johan", "factures"); !slices.Equal(l, []string{"edf.pdf"}) {
		t.Errorf("johan's factures = %v", l)
	}
	if got, _ := counts(t, e, "johan"); !mapsEqual(got, map[string]int{"factures": 1}) {
		t.Errorf("johan's summary = %v", got)
	}
	// Nor can an account correct another's document.
	if _, err := e.docs.Set(e.ctx, "johan", "Factures/edf.pdf", &contrats); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("johan on enzo's path: %v", err)
	}
}

func TestACorrectionFollowsItsFile(t *testing.T) {
	e := newEnv(t)
	e.document(t, "enzo", "Factures/edf.pdf", invoice, "factures", day)
	banque := "banque"
	if _, err := e.docs.Set(e.ctx, "enzo", "Factures/edf.pdf", &banque); err != nil {
		t.Fatal(err)
	}

	// Renamed: the same file, the same choice.
	if _, err := e.files.Move(e.ctx, "enzo", "Factures/edf.pdf", "Factures/edf-mars.pdf"); err != nil {
		t.Fatal(err)
	}
	if l := list(t, e, "enzo", "banque"); !slices.Equal(l, []string{"Factures/edf-mars.pdf"}) {
		t.Errorf("after the rename: %v", l)
	}

	// In the trash: left out of every list and count, and out of reach.
	item, err := e.files.Trash(e.ctx, "enzo", "Factures/edf-mars.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got, pending := counts(t, e, "enzo"); len(got) != 0 || pending != 0 {
		t.Errorf("summary with the document in the trash = %v, %d", got, pending)
	}
	if l := list(t, e, "enzo", "banque"); len(l) != 0 {
		t.Errorf("banque with the document in the trash = %v", l)
	}
	if _, err := e.docs.Set(e.ctx, "enzo", "Factures/edf-mars.pdf", &banque); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("set in the trash: %v", err)
	}

	// Restored: back with its choice.
	if _, err := e.files.Restore(e.ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	if l := list(t, e, "enzo", "banque"); !slices.Equal(l, []string{"Factures/edf-mars.pdf"}) {
		t.Errorf("after the restore: %v", l)
	}

	// Purged: the choice goes with the file.
	item, err = e.files.Trash(e.ctx, "enzo", "Factures/edf-mars.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.files.Purge(e.ctx, "enzo", item.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM file_categories`).Scan(&n); err != nil || n != 0 {
		t.Errorf("choices left after the purge: %d, %v", n, err)
	}
}

func TestSetRefusesWhatIsNotADocument(t *testing.T) {
	e := newEnv(t)
	e.document(t, "enzo", "Factures/edf.pdf", invoice, "factures", day)
	e.put(t, "enzo", "Photos/plage.jpg", "jpeg", day)
	e.text(t, "jpeg", false)
	e.put(t, "enzo", "archive.zip", "zip", day)

	unknown := "inconnue"
	if _, err := e.docs.Set(e.ctx, "enzo", "Factures/edf.pdf", &unknown); !errors.Is(err, doccat.ErrInvalid) {
		t.Errorf("unknown category: %v", err)
	}
	factures := "factures"
	for _, p := range []string{"Photos/plage.jpg", "archive.zip", "Factures", "absent.pdf"} {
		if _, err := e.docs.Set(e.ctx, "enzo", p, &factures); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s: %v, want not found", p, err)
		}
	}
	if _, err := e.docs.Set(e.ctx, "enzo", "../johan/x.pdf", &factures); !errors.Is(err, storage.ErrInvalidPath) {
		t.Errorf("a path out of the account: %v", err)
	}
}

// vectors writes the first window of content, at a version of Arctic.
func (e env) vectors(t *testing.T, content, version string) {
	t.Helper()
	e.exec(t, `INSERT INTO `+textemb.Table+` (sha256, chunk, start_char, end_char, model, version, embedding)
		VALUES ($1, 0, 0, 1, $2, $3, array_fill(0.036, ARRAY[768])::halfvec)`, sum(content), textemb.Model, version)
}

func (e env) declare(t *testing.T, name, version string) {
	t.Helper()
	e.exec(t, `INSERT INTO embedding_models (name, version, dims) VALUES ($1, $2, 768)
		ON CONFLICT (name) DO UPDATE SET version = EXCLUDED.version, requeued_at = NULL`, name, version)
}

func queued(t *testing.T, e env) []string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT payload->>'sha256' FROM tasks WHERE kind = $1 AND priority = $2 AND state = 'queued' ORDER BY 1`,
		doccat.Kind, tasks.PriorityBackfill)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func hexOf(content string) string { return hex.EncodeToString(sum(content)) }

func TestBackfillQueuesWhatTheClassifierHasNotSeen(t *testing.T) {
	e := newEnv(t)
	e.declare(t, textemb.Model, "v1")
	e.declare(t, doccat.Name, "c1")
	up := "à jour"
	e.document(t, "enzo", "a.pdf", up, "factures", day) // classified at c1
	e.vectors(t, up, "v1")
	old := "ancienne version"
	e.document(t, "enzo", "b.pdf", old, "", day)
	e.classify(t, old, "factures", "c0")
	e.vectors(t, old, "v1")
	never := "jamais classé"
	e.document(t, "enzo", "c.pdf", never, "", day)
	e.vectors(t, never, "v1")
	stale := "vecteurs anciens" // the workers classify it when they encode it again
	e.document(t, "enzo", "d.pdf", stale, "", day)
	e.vectors(t, stale, "v0")
	trashed := "à la corbeille"
	e.document(t, "enzo", "e.pdf", trashed, "", day)
	e.vectors(t, trashed, "v1")
	if _, err := e.files.Trash(e.ctx, "enzo", "e.pdf"); err != nil {
		t.Fatal(err)
	}

	q := &tasks.Queue{Pool: e.pool}
	n, err := doccat.Backfill(e.ctx, q)
	want := []string{hexOf(old), hexOf(never)}
	slices.Sort(want)
	if err != nil || n != 2 || !slices.Equal(queued(t, e), want) {
		t.Fatalf("backfill = %d, %v, queued %v; want %v", n, err, queued(t, e), want)
	}
	// Once per content while its task waits.
	if n, err := doccat.Backfill(e.ctx, q); err != nil || n != 0 {
		t.Errorf("second backfill = %d, %v", n, err)
	}
}

func TestSyncClassifiesAgainOncePerVersion(t *testing.T) {
	e := newEnv(t)
	q := &tasks.Queue{Pool: e.pool}
	e.declare(t, textemb.Model, "v1")
	e.document(t, "enzo", "a.pdf", invoice, "factures", day)
	e.vectors(t, invoice, "v1")
	sync := func(want int) {
		t.Helper()
		if n, err := doccat.Sync(e.ctx, q); err != nil || n != want {
			t.Errorf("sync = %d, %v; want %d", n, err, want)
		}
		e.exec(t, `UPDATE tasks SET state = 'done' WHERE kind = $1`, doccat.Kind)
	}

	sync(0) // no worker has declared the classifier yet
	e.declare(t, doccat.Name, "c1")
	sync(0) // the document is classified at c1 already
	sync(0)
	e.declare(t, doccat.Name, "c2") // new sentences
	sync(1)
	sync(0) // once per version
}
