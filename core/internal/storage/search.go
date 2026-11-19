package storage

import (
	"context"
	"fmt"
	"strings"
)

// Search limits.
const (
	DefaultSearchLimit = 30
	MaxSearchLimit     = 100
	maxSearchWords     = 8
)

// Search finds the owner's live files and folders whose path holds every word
// of q, ignoring accents and case (cloudos_fold, migration 0012). Names that
// start with the first word come first, then names that hold it, then the
// rest; within each, the most recently modified first. An empty q finds nothing.
func (s *Store) Search(ctx context.Context, owner, q string, limit int) ([]File, error) {
	hits, err := s.SearchNames(ctx, owner, q, limit, Filter{})
	if err != nil {
		return nil, err
	}
	out := make([]File, len(hits))
	for i, h := range hits {
		out[i] = h.File
	}
	return out, nil
}

// How the name of a file found by Search holds the first word searched.
const (
	TierStart = iota // the name starts with it
	TierName         // the name holds it
	TierPath         // only a folder of its path does
)

// NameHit is a file or folder found by its path, and how its name matched (Tier).
type NameHit struct {
	File
	Tier int
}

// SearchNames is Search with the tier of each hit, narrowed by filter.
func (s *Store) SearchNames(ctx context.Context, owner, q string, limit int, filter Filter) ([]NameHit, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(q)
	if len(words) == 0 {
		return []NameHit{}, nil
	}
	if len(words) > maxSearchWords {
		words = words[:maxSearchWords]
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	limit = min(limit, MaxSearchLimit)

	// One condition per word, so that each can use the trigram index.
	args := []any{owner, limit}
	var where strings.Builder
	for _, w := range words {
		args = append(args, escapeLike(w))
		fmt.Fprintf(&where, ` AND cloudos_fold(path) LIKE '%%' || cloudos_fold($%d) || '%%' ESCAPE '\'`, len(args))
	}
	narrow, args := filter.SQL("files", args)
	first := `cloudos_fold($3)`
	name := `cloudos_fold(regexp_replace(path, '^.*/', ''))`
	tier := `CASE WHEN ` + name + ` LIKE ` + first + ` || '%' ESCAPE '\' THEN 0
		          WHEN ` + name + ` LIKE '%' || ` + first + ` || '%' ESCAPE '\' THEN 1
		          ELSE 2 END`
	rows, err := s.Pool.Query(ctx, `SELECT `+fileColumns+`, `+tier+` AS tier FROM files
		WHERE owner = $1 AND trash_id IS NULL`+where.String()+narrow+`
		ORDER BY tier, modified_at DESC, path
		LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NameHit{}
	for rows.Next() {
		var h NameHit
		if h.File, err = ScanFile(rows, &h.Tier); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// DefaultContentLimit is how many files SearchContent returns by default
// (MaxSearchLimit at most).
const DefaultContentLimit = 20

// ContentHit is a live file whose text holds the words searched: the rank of
// its content (higher first) and an extract of the text around the words.
type ContentHit struct {
	File
	Rank    float32
	Snippet []SnippetPart
}

// SnippetPart is a piece of an extract: a word found (Hit) or the text around it.
type SnippetPart struct {
	Text string `json:"text"`
	Hit  bool   `json:"hit,omitempty"`
}

// headlineOptions shape the extract: two fragments of about fifteen words.
// ts_headline marks each word found between \x01 and \x02, which
// splitSnippet turns into SnippetParts.
const headlineOptions = "MaxFragments=2, MaxWords=15, MinWords=6, FragmentDelimiter=\" … \", StartSel=\x01, StopSel=\x02"

// SearchContent finds the owner's live files whose text (document_text,
// docs/architecture/documents.md) holds the words of q, with the French rules
// of the cloudos_fr configuration: stems, accents and case ignored
// (migration 0018). q follows websearch_to_tsquery: every word, in any order;
// "a phrase" in quotes; -word excludes a word. Contents with the words close
// together and dense come first, then the most recently modified; every live
// file of the owner with that content is listed. A q without a word to look
// for (only stop words, only exclusions) finds nothing.
//
// The extract is read from a 2 400-character window around the first word
// found, not from the whole text: ts_headline parses everything it is given,
// 40 to 100 ms for a 460 000-character document. When the window has no word
// found (a stem that does not start the word, "chevaux" for 'cheval'), it
// falls back to the whole text. filter narrows the files listed.
func (s *Store) SearchContent(ctx context.Context, owner, q string, limit int, filter Filter) ([]ContentHit, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(q)
	if len(words) > maxSearchWords {
		words = words[:maxSearchWords]
	}
	if limit <= 0 {
		limit = DefaultContentLimit
	}
	limit = min(limit, MaxSearchLimit)

	// The lexemes to look for, without the excluded ones: without any, the
	// query would match every document that lacks the excluded words.
	var lexemes []string
	if err := s.Pool.QueryRow(ctx, `SELECT tsvector_to_array(to_tsvector('public.cloudos_fr', $1))`,
		strings.Join(wantedWords(words), " ")).Scan(&lexemes); err != nil {
		return nil, err
	}
	if len(lexemes) == 0 {
		return []ContentHit{}, nil
	}

	// d.text || '' is a full copy: substr() on the stored, compressed text can
	// fail on PostgreSQL 17.8 when its slice cuts an accented character. OFFSET 0
	// keeps the copy and the search for the first word from being done twice.
	args := []any{owner, strings.Join(words, " "), limit, firstWordPattern(lexemes), headlineOptions}
	narrow, args := filter.SQL("f", args)
	rows, err := s.Pool.Query(ctx, `
		WITH q AS (SELECT websearch_to_tsquery('public.cloudos_fr', $2) AS query),
		hits AS (
			SELECT d.sha256, ts_rank_cd(d.tsv, q.query, 2) AS rank
			FROM document_text d, q
			WHERE d.tsv @@ q.query
			  AND EXISTS (SELECT 1 FROM files f
			              WHERE f.sha256 = d.sha256 AND f.kind = 'file' AND f.owner = $1 AND f.trash_id IS NULL`+narrow+`)
			ORDER BY rank DESC
			LIMIT $3),
		extracts AS (
			SELECT h.sha256, h.rank,
			       CASE WHEN strpos(w.near, E'\x01') > 0 THEN w.near
			            ELSE ts_headline('public.cloudos_fr', x.doc, q.query, $5) END AS snippet
			FROM hits h
			JOIN document_text d ON d.sha256 = h.sha256
			CROSS JOIN q
			CROSS JOIN LATERAL (SELECT d.text || '' AS doc OFFSET 0) x
			CROSS JOIN LATERAL (SELECT regexp_instr(x.doc, $4, 1, 1, 0, 'i') AS at OFFSET 0) a
			CROSS JOIN LATERAL (SELECT CASE WHEN a.at > 0
			    THEN ts_headline('public.cloudos_fr', substr(x.doc, greatest(a.at - 400, 1), 2400), q.query, $5)
			    ELSE '' END AS near) w)
		SELECT `+qualified("f", fileColumns)+`, e.rank, e.snippet
		FROM extracts e
		JOIN files f ON f.sha256 = e.sha256 AND f.kind = 'file' AND f.owner = $1 AND f.trash_id IS NULL`+narrow+`
		ORDER BY e.rank DESC, f.modified_at DESC, f.path
		LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentHit{}
	for rows.Next() {
		var h ContentHit
		var snippet string
		if err := rows.Scan(&h.ID, &h.Owner, &h.Path, &h.Kind, &h.Size, &h.MIME, &h.SHA256,
			&h.CreatedAt, &h.ModifiedAt, &h.IndexedAt, &h.TrashID, &h.Rank, &snippet); err != nil {
			return nil, err
		}
		h.Snippet = splitSnippet(snippet)
		out = append(out, h)
	}
	return out, rows.Err()
}

// qualified prefixes each column of a list such as fileColumns with a table alias.
func qualified(alias, columns string) string {
	return alias + "." + strings.ReplaceAll(columns, ", ", ", "+alias+".")
}

// wantedWords drops from the words of a websearch query those it excludes
// (-word, -"a phrase") and its OR operators, leaving the words to look for.
func wantedWords(words []string) []string {
	var out []string
	excluded := false // inside a -"phrase"
	for i, w := range words {
		switch {
		case excluded:
			excluded = !strings.HasSuffix(w, `"`)
		case strings.HasPrefix(w, `-"`):
			excluded = len(w) == 2 || !strings.HasSuffix(w, `"`)
		case strings.HasPrefix(w, "-") && len(w) > 1:
		case strings.EqualFold(w, "or") && i > 0 && i < len(words)-1:
		default:
			out = append(out, w)
		}
	}
	return out
}

// letterVariants are the letters an unaccented lexeme letter may come from in
// French text; unaccent also turns œ into oe and æ into ae.
var (
	letterVariants = map[rune]string{
		'a': "aàâäáã", 'c': "cç", 'e': "eéèêë", 'i': "iîïí", 'n': "nñ", 'o': "oôöóõ", 'u': "uùûüú", 'y': "yÿ",
	}
	ligatures = map[rune]string{'a': "æ", 'o': "œ"} // the ligature of this letter and 'e'
)

// firstWordPattern is a regular expression (PostgreSQL ARE, used with the 'i'
// flag) that finds a word starting with one of the lexemes, accents or not:
// 'electricit' finds "Électricité". A stem is usually the start of its word,
// followed by at most a few letters.
func firstWordPattern(lexemes []string) string {
	alts := make([]string, len(lexemes))
	for i, lex := range lexemes {
		var b strings.Builder
		runes := []rune(lex)
		for j := 0; j < len(runes); j++ {
			r := runes[j]
			if lig := ligatures[r]; lig != "" && j+1 < len(runes) && runes[j+1] == 'e' {
				fmt.Fprintf(&b, "(?:[%s][%s]|%s)", letterVariants[r], letterVariants['e'], lig)
				j++
				continue
			}
			switch {
			case letterVariants[r] != "":
				fmt.Fprintf(&b, "[%s]", letterVariants[r])
			case strings.ContainsRune(`\^$.|?*+()[]{}`, r):
				b.WriteRune('\\')
				b.WriteRune(r)
			default:
				b.WriteRune(r)
			}
		}
		alts[i] = b.String()
	}
	return `\m(?:` + strings.Join(alts, "|") + `)[[:alnum:]]{0,8}\M`
}

// splitSnippet turns a ts_headline result into parts, the words found
// (between \x01 and \x02) apart, with its blanks (line and page breaks)
// folded into single spaces.
func splitSnippet(s string) []SnippetPart {
	s = strings.Join(strings.Fields(s), " ")
	out := []SnippetPart{}
	hit := false
	for s != "" {
		end := strings.IndexAny(s, "\x01\x02")
		if end < 0 {
			end = len(s)
		}
		if text := s[:end]; text != "" {
			out = append(out, SnippetPart{Text: text, Hit: hit})
		}
		if end == len(s) {
			break
		}
		hit = s[end] == '\x01'
		s = s[end+1:]
	}
	return out
}

// DefaultRecent is how many files Recent returns by default (and at most 4 times as many).
const DefaultRecent = 8

// Recent returns the owner's live files most recently added or modified,
// whichever came last, newest first.
func (s *Store) Recent(ctx context.Context, owner string, limit int) ([]File, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultRecent
	}
	limit = min(limit, 4*DefaultRecent)
	rows, err := s.Pool.Query(ctx, `SELECT `+fileColumns+` FROM files
		WHERE owner = $1 AND trash_id IS NULL AND kind = 'file'
		ORDER BY greatest(modified_at, created_at) DESC, path
		LIMIT $2`, owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
