// Package search answers Demander with one ranked list (#72,
// docs/architecture/search.md): the account's files found by their name, by
// the words of their text, by the meaning of their text and by what their
// photo shows. The four searches run at once with the same filter; their
// scores, which do not compare as they are, become confidences calibrated on
// the benchmark corpus, and a file's score combines the confidences of every
// search that found it. Reads only: it never touches the data root.
package search

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/embed"
	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

// Limits.
const (
	DefaultLimit = 30
	MaxLimit     = 100
	// DefaultBudget bounds the two searches by meaning, from the query sent to
	// the workers to the walk of the index. Past it, the answer leaves with the
	// names and words only: a worker that is starting, busy reloading a model
	// or stopped never holds Demander up.
	DefaultBudget = 800 * time.Millisecond
	// candidates is how many files each search hands the fusion, at least.
	candidates = 40
)

// How a file was found (Hit.FoundBy).
const (
	ByName    = "name"    // its path holds every word
	ByText    = "text"    // its text holds every word
	ByPrefix  = "prefix"  // its text holds a word typed in part (storage.MatchPrefix)
	ByFuzzy   = "fuzzy"   // its text holds a word near one typed with a typo (storage.MatchFuzzy)
	ByMeaning = "meaning" // its text is near in meaning
	ByImage   = "image"   // its photo shows what was asked
)

// Confidences, from 0 to 1, that a file a search found is what was asked.
// Measured on the benchmark corpus (worker/tests/fixtures/bench): see
// docs/architecture/search.md before changing one.
const (
	confNameStart = 0.9 // the name starts with the first word
	confName      = 0.8 // the name holds it
	confPath      = 0.6 // only a folder of the path does
	confText      = 0.4 // the text holds every word
	confPrefix    = 0.3 // and a word typed in part
	// A word typed with a typo: the floor cuts it when a sure answer exists,
	// so a guess never comes before an exact answer.
	confFuzzy = 0.25
)

// scale maps a cosine similarity linearly onto a confidence: 0 at floor and
// below, 1 at full and above.
type scale struct{ floor, full float64 }

func (s scale) of(cosine float64) float64 {
	return min(1, max(0, (cosine-s.floor)/(s.full-s.floor)))
}

// The cosines of the two models are on scales of their own: a right document
// scores 0.27 to 0.68 with Arctic Embed. A right photo scores 0.09 to 0.14
// with SigLIP 2, which gives unrelated photos up to 0.10, and every photo
// about 0.08 for a word it does not know: a photo's confidence comes from its
// lift over the mean of the account's photos (photovec.Hit.Lift), 0.036 or
// more for a right photo, 0.024 at most when the query is a word typed in
// part or with a typo.
var (
	docScale   = scale{floor: 0.15, full: 0.45}
	photoScale = scale{floor: 0.02, full: 0.06}
)

// A file found only by meaning is kept when its score reaches keepAbs and
// keepRel times the best score of the query: when a document answers clearly,
// the photos that are merely the least far go, and a vague query keeps its list.
const (
	keepAbs = 0.2
	keepRel = 0.3
)

// Query is what Demander asks.
type Query struct {
	Q      string
	Filter storage.Filter
	Limit  int // DefaultLimit when 0, MaxLimit at most
}

// Hit is a file of the list.
type Hit struct {
	File storage.File
	// Score is 1 − Π(1 − c) over the confidences c of the searches that found
	// it: one sure search is enough, two agreeing ones reinforce each other.
	// It orders the list of one query; it does not compare across queries.
	Score   float64
	FoundBy []string              // ByName, ByText (or ByPrefix, ByFuzzy), ByMeaning, ByImage, in that order
	Snippet []storage.SnippetPart // the words found in its text, when they are
	Excerpt string                // else its passage nearest in meaning, when it has one
}

// Result is the list, best first, and whether it misses the search by meaning.
type Result struct {
	Hits []Hit
	// Partial is true when a search by meaning did not answer within the
	// budget (no worker, no model, too slow): the list has names and words only.
	Partial bool
}

// Searcher runs the four searches. Docs and Photos may be nil: the list then
// has names and words only, and is Partial.
type Searcher struct {
	Files  *storage.Store
	Docs   *textemb.Searcher
	Photos *photovec.Searcher
	Budget time.Duration // DefaultBudget when 0
}

var errNoSearcher = errors.New("no searcher configured")

// Search returns the owner's live files that answer q, best first. The owner
// is the caller's session, never a parameter. An empty q finds nothing,
// without asking the workers. The query is never logged.
func (s *Searcher) Search(ctx context.Context, owner string, q Query) (Result, error) {
	owner, err := storage.CleanOwner(owner)
	if err != nil {
		return Result{}, err
	}
	text := strings.Join(strings.Fields(q.Q), " ")
	if text == "" {
		return Result{Hits: []Hit{}}, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	n := max(limit, candidates)
	budget := s.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	meaning, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	var (
		wg               sync.WaitGroup
		names            []storage.NameHit
		texts            []storage.ContentHit
		docs             []textemb.Hit
		photos           []photovec.Hit
		nameErr, textErr error
		docErr, photoErr error = errNoSearcher, nil
	)
	wg.Go(func() { names, nameErr = s.Files.SearchNames(ctx, owner, text, n, q.Filter) })
	wg.Go(func() { texts, textErr = s.Files.SearchContent(ctx, owner, text, n, q.Filter) })
	if s.Docs != nil {
		wg.Go(func() { docs, docErr = s.Docs.Search(meaning, owner, text, n, q.Filter) })
	}
	if q.Filter.Type != storage.TypeDocs { // only images and videos have a photo vector
		if s.Photos == nil {
			photoErr = errNoSearcher
		} else {
			wg.Go(func() { photos, photoErr = s.Photos.Search(meaning, owner, text, n, q.Filter) })
		}
	}
	wg.Wait()
	if err := cmp.Or(nameErr, textErr); err != nil {
		return Result{}, err
	}
	partial := false
	if missed("documents", docErr) {
		docs, partial = nil, true
	}
	if missed("photos", photoErr) {
		photos, partial = nil, true
	}
	hits := fuse(names, texts, docs, photos)
	return Result{Hits: hits[:min(len(hits), limit)], Partial: partial}, nil
}

// missed tells a search by meaning failed, and logs why unless the workers
// could not encode the query in time, which the answer already says.
func missed(source string, err error) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, embed.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errNoSearcher) {
		slog.Warn("search by meaning", "source", source, "err", err)
	}
	return true
}

// candidate is a file on its way into the list.
type candidate struct {
	hit     Hit
	miss    float64 // Π(1 − c) over the confidences so far
	first   int     // the best place it has in one of the lists
	lexical bool    // found by its name or its words, as typed or begun: always kept
}

// fuse merges the four lists, each best first, into one, best first.
func fuse(names []storage.NameHit, texts []storage.ContentHit, docs []textemb.Hit, photos []photovec.Hit) []Hit {
	byPath := map[string]*candidate{}
	var all []*candidate
	add := func(f storage.File, place int, by string, confidence float64) *candidate {
		c := byPath[f.Path]
		if c == nil {
			c = &candidate{hit: Hit{File: f}, miss: 1, first: place}
			byPath[f.Path] = c
			all = append(all, c)
		}
		c.hit.FoundBy = append(c.hit.FoundBy, by)
		c.miss *= 1 - confidence
		c.first = min(c.first, place)
		return c
	}
	for i, h := range names {
		add(h.File, i, ByName, nameConfidence(h.Tier)).lexical = true
	}
	for i, h := range texts {
		by, confidence := textSource(h.Match)
		c := add(h.File, i, by, confidence)
		c.lexical = c.lexical || by != ByFuzzy
		c.hit.Snippet = h.Snippet
	}
	for i, h := range docs {
		add(h.File, i, ByMeaning, docScale.of(h.Score)).hit.Excerpt = h.Excerpt
	}
	for i, h := range photos {
		add(h.File, i, ByImage, photoScale.of(h.Lift))
	}

	best := 0.0
	for _, c := range all {
		c.hit.Score = 1 - c.miss
		best = max(best, c.hit.Score)
	}
	floor := max(keepAbs, keepRel*best)
	out := make([]Hit, 0, len(all))
	kept := slices.DeleteFunc(all, func(c *candidate) bool { return !c.lexical && c.hit.Score < floor })
	slices.SortStableFunc(kept, func(a, b *candidate) int {
		return cmp.Or(
			cmp.Compare(b.hit.Score, a.hit.Score),
			cmp.Compare(a.first, b.first),
			b.hit.File.ModifiedAt.Compare(a.hit.File.ModifiedAt),
			strings.Compare(a.hit.File.Path, b.hit.File.Path),
		)
	})
	for _, c := range kept {
		if len(c.hit.Snippet) > 0 {
			c.hit.Excerpt = ""
		}
		out = append(out, c.hit)
	}
	return out
}

// textSource is how a file found in its text was found, and the confidence of that.
func textSource(match string) (string, float64) {
	switch match {
	case storage.MatchPrefix:
		return ByPrefix, confPrefix
	case storage.MatchFuzzy:
		return ByFuzzy, confFuzzy
	default:
		return ByText, confText
	}
}

// nameConfidence is the confidence of a file found by its path.
func nameConfidence(tier int) float64 {
	switch tier {
	case storage.TierStart:
		return confNameStart
	case storage.TierName:
		return confName
	default:
		return confPath
	}
}
