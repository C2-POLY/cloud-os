package search

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/photovec"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
	"github.com/C2-POLY/cloud-os/core/internal/textemb"
)

var day = time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)

func file(path string) storage.File {
	return storage.File{Path: path, Kind: "file", ModifiedAt: day}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScalesMapCosinesOntoConfidences(t *testing.T) {
	for _, c := range []struct {
		s      scale
		cosine float64
		want   float64
	}{
		{docScale, 0.10, 0}, {docScale, 0.15, 0}, {docScale, 0.30, 0.5}, {docScale, 0.45, 1}, {docScale, 0.68, 1},
		{photoScale, 0.06, 0}, {photoScale, 0.065, 0}, {photoScale, 0.0925, 0.5}, {photoScale, 0.12, 1}, {photoScale, 0.14, 1},
	} {
		if got := c.s.of(c.cosine); !near(got, c.want) {
			t.Errorf("%v.of(%v) = %v, want %v", c.s, c.cosine, got, c.want)
		}
	}
}

func scores(hits []Hit) map[string]float64 {
	out := map[string]float64{}
	for _, h := range hits {
		out[h.File.Path] = h.Score
	}
	return out
}

func paths(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.File.Path
	}
	return out
}

func TestSearchesThatAgreeReinforceEachOther(t *testing.T) {
	bail, quittance := file("bail.pdf"), file("quittance.pdf")
	hits := fuse(
		[]storage.NameHit{{File: bail, Tier: storage.TierStart}},
		[]storage.ContentHit{{File: bail, Snippet: []storage.SnippetPart{{Text: "bail", Hit: true}}}, {File: quittance}},
		[]textemb.Hit{{File: bail, Score: 0.45, Excerpt: "Contrat de location"}, {File: quittance, Score: 0.30, Excerpt: "Quittance"}},
		nil)
	got := scores(hits)
	// bail: 1 − (1 − 0.9)(1 − 0.4)(1 − 1); quittance: 1 − (1 − 0.4)(1 − 0.5).
	if !near(got["bail.pdf"], 1) || !near(got["quittance.pdf"], 0.7) {
		t.Errorf("scores %v", got)
	}
	if !slices.Equal(hits[0].FoundBy, []string{ByName, ByText, ByMeaning}) || !slices.Equal(hits[1].FoundBy, []string{ByText, ByMeaning}) {
		t.Errorf("found by %v and %v", hits[0].FoundBy, hits[1].FoundBy)
	}
	// The words found win over the passage nearest in meaning.
	if hits[0].Snippet == nil || hits[0].Excerpt != "" || hits[1].Snippet != nil || hits[1].Excerpt != "Quittance" {
		t.Errorf("bail %+v, quittance %+v", hits[0], hits[1])
	}
	only := fuse(nil, nil, []textemb.Hit{{File: bail, Score: 0.45, Excerpt: "Contrat de location"}}, nil)
	if only[0].Excerpt != "Contrat de location" {
		t.Errorf("excerpt %q", only[0].Excerpt)
	}
}

func TestNameTiers(t *testing.T) {
	got := scores(fuse([]storage.NameHit{
		{File: file("a"), Tier: storage.TierStart}, {File: file("b"), Tier: storage.TierName}, {File: file("c"), Tier: storage.TierPath},
	}, nil, nil, nil))
	if !near(got["a"], 0.9) || !near(got["b"], 0.8) || !near(got["c"], 0.6) {
		t.Errorf("scores %v", got)
	}
}

func TestPhotosThatAreMerelyTheLeastFarGo(t *testing.T) {
	photos := []photovec.Hit{{File: file("plage.jpg"), Score: 0.0815}, {File: file("chat.jpg"), Score: 0.07}}
	// Beside a document that answers clearly (score 1, so the floor is 0.3),
	// a photo at 0.3 stays and one under 0.2 goes.
	hits := fuse(nil, nil, []textemb.Hit{{File: file("bail.pdf"), Score: 0.5}}, photos)
	if want := []string{"bail.pdf", "plage.jpg"}; !slices.Equal(paths(hits), want) {
		t.Errorf("with a clear document: %v, want %v", paths(hits), want)
	}
	// A clear photo makes the floor 0.3 too.
	hits = fuse(nil, nil, nil, []photovec.Hit{{File: file("chien.jpg"), Score: 0.13}, {File: file("chat.jpg"), Score: 0.08}})
	if want := []string{"chien.jpg"}; !slices.Equal(paths(hits), want) {
		t.Errorf("with a clear photo: %v, want %v", paths(hits), want)
	}
	// A vague query keeps what reaches 0.2.
	hits = fuse(nil, nil, []textemb.Hit{{File: file("a.pdf"), Score: 0.24}, {File: file("b.pdf"), Score: 0.22}, {File: file("c.pdf"), Score: 0.19}}, nil)
	if want := []string{"a.pdf", "b.pdf"}; !slices.Equal(paths(hits), want) {
		t.Errorf("vague: %v, want %v", paths(hits), want)
	}
	// A file found by its name or its words is never cut.
	hits = fuse([]storage.NameHit{{File: file("Photos/bail"), Tier: storage.TierPath}}, []storage.ContentHit{{File: file("note.txt")}},
		[]textemb.Hit{{File: file("bail.pdf"), Score: 0.6}}, nil)
	if want := []string{"bail.pdf", "Photos/bail", "note.txt"}; !slices.Equal(paths(hits), want) {
		t.Errorf("lexical: %v, want %v", paths(hits), want)
	}
}

func TestTiesKeepTheOrderOfTheSearches(t *testing.T) {
	older, newer := file("b-older"), file("c-newer")
	newer.ModifiedAt = day.Add(time.Hour)
	var names []storage.NameHit
	for _, f := range []storage.File{file("z"), file("a"), older, newer} {
		names = append(names, storage.NameHit{File: f, Tier: storage.TierName})
	}
	// Same score: the place in the name list first, then the most recent, then the path.
	got := paths(fuse(names, nil, nil, nil))
	if want := []string{"z", "a", "b-older", "c-newer"}; !slices.Equal(got, want) {
		t.Errorf("by place: %v, want %v", got, want)
	}
	// Sure of both (confidence 1) and first of their lists: the most recent first.
	got = paths(fuse(nil, nil, []textemb.Hit{{File: older, Score: 0.6}}, []photovec.Hit{{File: newer, Score: 0.2}}))
	if want := []string{"c-newer", "b-older"}; !slices.Equal(got, want) {
		t.Errorf("same place: %v, want %v", got, want)
	}
	// Then the path.
	got = paths(fuse(nil, nil, []textemb.Hit{{File: file("b"), Score: 0.6}}, []photovec.Hit{{File: file("a"), Score: 0.2}}))
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("same date: %v, want %v", got, want)
	}
}

func TestTheSameFileFromTwoSearchesIsOneHit(t *testing.T) {
	f := file("Photos/plage.jpg")
	hits := fuse([]storage.NameHit{{File: f, Tier: storage.TierStart}}, nil, nil, []photovec.Hit{{File: f, Score: 0.12}})
	if len(hits) != 1 || !near(hits[0].Score, 1) || fmt.Sprint(hits[0].FoundBy) != "[name image]" {
		t.Errorf("hits %+v", hits)
	}
}

func TestAWordTypedInPartOrWithATypoComesAfterExactWords(t *testing.T) {
	exact, begun, typo := file("exact.txt"), file("begun.txt"), file("typo.txt")
	texts := []storage.ContentHit{
		{File: typo, Match: storage.MatchFuzzy},
		{File: begun, Match: storage.MatchPrefix},
		{File: exact, Match: storage.MatchWords},
	}
	hits := fuse(nil, texts, nil, nil)
	got := scores(hits)
	if want := []string{"exact.txt", "begun.txt", "typo.txt"}; !slices.Equal(paths(hits), want) {
		t.Errorf("order %v, want %v", paths(hits), want)
	}
	if !near(got["exact.txt"], 0.4) || !near(got["begun.txt"], 0.3) || !near(got["typo.txt"], 0.25) {
		t.Errorf("scores %v", got)
	}
	if fmt.Sprint(hits[1].FoundBy, hits[2].FoundBy) != "[prefix] [fuzzy]" {
		t.Errorf("found by %v %v", hits[1].FoundBy, hits[2].FoundBy)
	}
	// Beside a sure answer (score 1, floor 0.3) a guess goes; a word begun stays.
	hits = fuse(nil, texts, []textemb.Hit{{File: file("bail.pdf"), Score: 0.6}}, nil)
	if want := []string{"bail.pdf", "exact.txt", "begun.txt"}; !slices.Equal(paths(hits), want) {
		t.Errorf("beside a sure answer: %v, want %v", paths(hits), want)
	}
	// A guess that the meaning confirms stays.
	hits = fuse(nil, texts, []textemb.Hit{{File: file("bail.pdf"), Score: 0.6}, {File: typo, Score: 0.3}}, nil)
	if !slices.Contains(paths(hits), "typo.txt") {
		t.Errorf("a confirmed guess went: %v", paths(hits))
	}
}
