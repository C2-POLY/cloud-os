package storage

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// How SearchContent found the words of a query in a text (ContentHit.Match).
const (
	MatchWords  = "text"   // every word as typed
	MatchPrefix = "prefix" // a word typed in part, completed
	MatchFuzzy  = "fuzzy"  // a word with a typo, replaced by the nearest words
)

// The rules of textQuery, measured on the vocabulary of the benchmark corpus
// (docs/architecture/search.md): typos of a letter or two score 0.57 to 0.73
// in trigram similarity, while unrelated words that share letters stop at
// 0.50. "warolo" is far from "warhallow" in letters (0.21) but has the same
// skeleton, "wrl".
const (
	prefixMinChars  = 3    // a shorter lexeme is never completed
	fuzzyMinChars   = 4    // nor corrected
	soundMinChars   = 5    // nor matched by its skeleton
	soundMinLen     = 3    // and a skeleton must have 3 letters
	fuzzySimilarity = 0.55 // trigram similarity of a typo
	soundSimilarity = 0.2  // the least a word of the same skeleton shares
	fuzzyCandidates = 3    // nearest words kept for a word with a typo
)

// textQuery is how SearchContent looks for the words of a query.
type textQuery struct {
	match   string   // MatchWords: websearch_to_tsquery as typed
	query   string   // otherwise, a tsquery of the words found, completed or corrected
	lexemes []string // the lexemes that query looks for, for the extract
	unknown bool     // a word is in no text, near or far: nothing matches
}

// textQuery classifies each lexeme of the words against search_words, the
// vocabulary of the texts (migration 0022):
//   - a lexeme the vocabulary has, or does not keep (a number, two letters),
//     is looked for as it is;
//   - one that starts a word of the vocabulary is completed: 'warhal':*;
//   - one that is near words of the vocabulary is replaced by the nearest
//     three: trigram similarity of fuzzySimilarity or more, or the same
//     skeleton (cloudos_sound) and soundSimilarity or more;
//   - otherwise the query has a word that no text has.
//
// When every lexeme is as typed, or the words use quotes, exclusions or or,
// the query is websearch_to_tsquery as before (MatchWords). Reads only.
func (s *Store) textQuery(ctx context.Context, words, lexemes []string) (textQuery, error) {
	plain := textQuery{match: MatchWords}
	if len(wantedWords(words)) != len(words) || strings.Contains(strings.Join(words, " "), `"`) {
		return plain, nil
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return textQuery{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type word struct {
		lex             string
		asIs, completed bool
		near            []string
	}
	patterns := make([]string, len(lexemes))
	for i, l := range lexemes {
		patterns[i] = escapeLike(l) + "%"
	}
	rows, err := tx.Query(ctx, `
		SELECT l.lex,
		       NOT cloudos_search_word(l.lex) OR EXISTS (SELECT 1 FROM search_words w WHERE w.word = l.lex),
		       length(l.lex) >= $3 AND EXISTS (SELECT 1 FROM search_words w WHERE w.word LIKE l.pattern ESCAPE '\')
		FROM unnest($1::text[], $2::text[]) AS l(lex, pattern)`, lexemes, patterns, prefixMinChars)
	if err != nil {
		return textQuery{}, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (word, error) {
		var w word
		return w, row.Scan(&w.lex, &w.asIs, &w.completed)
	})
	if err != nil {
		return textQuery{}, err
	}

	var unmatched []string
	for _, w := range all {
		if !w.asIs && !w.completed {
			unmatched = append(unmatched, w.lex)
		}
	}
	if len(unmatched) > 0 {
		if _, err := tx.Exec(ctx, `SELECT set_config('pg_trgm.similarity_threshold', $1, true)`, strconv.FormatFloat(fuzzySimilarity, 'f', -1, 64)); err != nil {
			return textQuery{}, err
		}
		rows, err := tx.Query(ctx, `
			SELECT l.lex, ARRAY(
			    SELECT c.word FROM (
			        SELECT w.word, w.docs, similarity(w.word, l.lex) AS sim FROM search_words w
			        WHERE w.word % l.lex
			        UNION
			        SELECT w.word, w.docs, similarity(w.word, l.lex) FROM search_words w
			        WHERE length(l.lex) >= $2 AND length(cloudos_sound(l.lex)) >= $3
			          AND w.sound = cloudos_sound(l.lex) AND similarity(w.word, l.lex) >= $4
			    ) c
			    WHERE c.word <> l.lex
			    ORDER BY c.sim DESC, c.docs DESC, c.word
			    LIMIT $5)
			FROM unnest($1::text[]) AS l(lex)
			WHERE length(l.lex) >= $6`,
			unmatched, soundMinChars, soundMinLen, soundSimilarity, fuzzyCandidates, fuzzyMinChars)
		if err != nil {
			return textQuery{}, err
		}
		near := map[string][]string{}
		for rows.Next() {
			var lex string
			var words []string
			if err := rows.Scan(&lex, &words); err != nil {
				rows.Close()
				return textQuery{}, err
			}
			near[lex] = words
		}
		if err := rows.Err(); err != nil {
			return textQuery{}, err
		}
		for i := range all {
			all[i].near = near[all[i].lex]
		}
	}

	out := textQuery{match: MatchWords}
	parts := make([]string, 0, len(all))
	for _, w := range all {
		switch {
		case w.asIs:
			parts = append(parts, tsLexeme(w.lex))
			out.lexemes = append(out.lexemes, w.lex)
		case w.completed:
			parts = append(parts, tsLexeme(w.lex)+":*")
			out.lexemes = append(out.lexemes, w.lex)
			if out.match == MatchWords {
				out.match = MatchPrefix
			}
		case len(w.near) > 0:
			alts := make([]string, len(w.near))
			for i, n := range w.near {
				alts[i] = tsLexeme(n)
			}
			parts = append(parts, "("+strings.Join(alts, " | ")+")")
			out.lexemes = append(out.lexemes, w.near...)
			out.match = MatchFuzzy
		default:
			return textQuery{unknown: true}, nil
		}
	}
	if out.match == MatchWords {
		return plain, nil
	}
	out.query = strings.Join(parts, " & ")
	return out, tx.Commit(ctx)
}

// tsLexeme quotes a lexeme for the input of a tsquery.
func tsLexeme(lex string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(lex) + "'"
}
