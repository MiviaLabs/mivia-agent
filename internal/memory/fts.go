package memory

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// FTS5 index schema. The virtual table mirrors the memories columns FTS5
// reads by name from the content table (external content, content='memories');
// rowids map one-to-one to the memories table's implicit integer rowid. The
// porter unicode61 tokenizer matches the shared Go tokenizer's word
// characters (Unicode letters and digits; underscore and punctuation split).
const ftsSchema = `CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
  title, summary, content,
  content='memories',
  tokenize='porter unicode61'
)`

// ftsBackfill is the FTS5 special rebuild command: it deletes and repopulates
// the entire index from the content table (memories), so it is idempotent and
// safe to run on every open. Existing databases (like .mivia/memory.db)
// migrate for free. A rowid-guarded INSERT is deliberately NOT used: for an
// external content table (content='memories'), any SELECT against
// memories_fts without a MATCH operator is passed straight through to the
// content table, so `rowid NOT IN (SELECT rowid FROM memories_fts)` would
// select nothing and the index would never be populated; the rebuild command
// is the documented, correct way to (re)populate an external content index.
const ftsBackfill = `INSERT INTO memories_fts(memories_fts) VALUES('rebuild')`

// hasFTS5Option reports whether a PRAGMA compile_options row set includes the
// FTS5 build option. Pure function, unit-tested.
func hasFTS5Option(options []string) bool {
	for _, opt := range options {
		if opt == "ENABLE_FTS5" {
			return true
		}
	}
	return false
}

// probeFTS5 queries the SQLite build options. A failed query (closed database)
// or a failed scan reports unavailable, failing closed to the LIKE path.
func probeFTS5(db *sql.DB) bool {
	rows, err := db.Query("PRAGMA compile_options")
	if err != nil {
		return false
	}
	defer rows.Close()
	var options []string
	for rows.Next() {
		var opt string
		if err := rows.Scan(&opt); err != nil {
			return false
		}
		options = append(options, opt)
	}
	return hasFTS5Option(options)
}

// ensureFTSIndex creates the memories_fts index (if missing) and backfills it
// idempotently. It reports whether the index is usable for search
// acceleration; a build without FTS5, or any index-level failure, degrades
// the store to the Phase-1 LIKE path with identical results and no error.
func ensureFTSIndex(db *sql.DB) bool {
	if !probeFTS5(db) {
		return false
	}
	if _, err := db.Exec(ftsSchema); err != nil {
		return false
	}
	if _, err := db.Exec(ftsBackfill); err != nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// FTS5 clean-query parsing
// ---------------------------------------------------------------------------

// ftsPartKind distinguishes the FTS5 query part kinds.
type ftsPartKind int

const (
	ftsPartToken ftsPartKind = iota
	ftsPartPrefix
	ftsPartPhrase
)

// ftsPart is one parsed FTS5 query part: a simple token, a prefix token
// (trailing asterisk), or a double-quoted phrase.
type ftsPart struct {
	kind ftsPartKind
	text string
}

var (
	ftsSimpleRe = regexp.MustCompile(`^[0-9A-Za-z_]{1,64}$`)
	ftsPrefixRe = regexp.MustCompile(`^[0-9A-Za-z_]{1,63}\*$`)
)

// ftsOperatorWords are FTS5 operator barewords. A query using them as bare
// terms is not clean (it would change matching semantics) and falls back to
// the LIKE path.
var ftsOperatorWords = map[string]struct{}{"and": {}, "or": {}, "not": {}, "near": {}}

// isFTSSpace reports whether b separates FTS5 query parts.
func isFTSSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// parseFTSQuery parses a trimmed query into FTS5 MATCH parts. ok is false
// when the query is not clean and must fall back to the LIKE path: non-ASCII
// input, stray asterisks, column-filter colons, unbalanced or un-doubled
// quotes, empty phrases, and reserved operator barewords (AND/OR/NOT/NEAR,
// case-insensitive) all make a query not clean. Doubled inner quotes inside a
// phrase are escaped literals and are preserved verbatim for the MATCH
// emitter (FTS5 phrase syntax); when every quote in the query is part of a
// doubled pair ('say ""hi""'), the whole query is one implicit phrase and the
// pairs are preserved for the emitter.
func parseFTSQuery(q string) ([]ftsPart, bool) {
	if allQuotesDoubled(q) {
		un := strings.TrimSpace(strings.ReplaceAll(q, `""`, `"`))
		if un == "" || strings.Trim(un, `"`) == "" {
			return nil, false // only escaped quotes: no real phrase content
		}
		return []ftsPart{{kind: ftsPartPhrase, text: q}}, true
	}
	var parts []ftsPart
	i := 0
	n := len(q)
	for i < n {
		for i < n && isFTSSpace(q[i]) {
			i++
		}
		if i >= n {
			break
		}
		if q[i] == '"' {
			j := i + 1
			var b strings.Builder
			closed := false
			for j < n {
				if q[j] == '"' {
					if j+1 < n && q[j+1] == '"' {
						b.WriteString(`""`)
						j += 2
						continue
					}
					closed = true
					j++
					break
				}
				b.WriteByte(q[j])
				j++
			}
			if !closed || b.Len() == 0 {
				return nil, false
			}
			if j < n && !isFTSSpace(q[j]) {
				return nil, false
			}
			parts = append(parts, ftsPart{kind: ftsPartPhrase, text: b.String()})
			i = j
			continue
		}
		j := i
		for j < n && !isFTSSpace(q[j]) {
			j++
		}
		term := q[i:j]
		switch {
		case ftsSimpleRe.MatchString(term):
			if _, op := ftsOperatorWords[strings.ToLower(term)]; op {
				return nil, false
			}
			parts = append(parts, ftsPart{kind: ftsPartToken, text: term})
		case ftsPrefixRe.MatchString(term):
			parts = append(parts, ftsPart{kind: ftsPartPrefix, text: term})
		default:
			return nil, false
		}
		i = j
	}
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

// ftsPartsAllPlainTokens reports whether every parsed FTS5 query part is a
// plain bare token. Prefix and phrase parts are excluded: FTS5 MATCH
// semantics for 'run*' (token prefix) and '"cache invalidation"' (contiguous
// token sequence) differ from the LIKE path's substring semantics, so a query
// with such a part must always run the authoritative LIKE path to keep
// FTS-enabled and FTS-disabled results identical (the ensureFTSIndex
// contract). Empty input is false: the MATCH gate needs at least one plain
// part to be taken.
func ftsPartsAllPlainTokens(parts []ftsPart) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part.kind != ftsPartToken {
			return false
		}
	}
	return true
}

// buildFTSMatch renders clean FTS5 query parts into a MATCH string. Bare
// stopword terms are dropped so the FTS candidate set stays aligned with the
// shared tokenizer's token-AND (the tokenizer drops the same stopwords);
// phrase parts are preserved verbatim (FTS5 phrase syntax).
func buildFTSMatch(parts []ftsPart) string {
	var b strings.Builder
	wrote := false
	for _, part := range parts {
		base := part.text
		if part.kind == ftsPartPrefix {
			base = strings.TrimSuffix(base, "*")
		}
		if part.kind != ftsPartPhrase {
			if _, stop := stopwords[strings.ToLower(base)]; stop {
				continue
			}
		}
		if wrote {
			b.WriteByte(' ')
		}
		switch part.kind {
		case ftsPartToken, ftsPartPrefix:
			b.WriteString(part.text)
		case ftsPartPhrase:
			b.WriteByte('"')
			b.WriteString(part.text)
			b.WriteByte('"')
		}
		wrote = true
	}
	return b.String()
}

// ftsMatchFromParsed builds an FTS5 MATCH string from a reduced parsed query
// (used for zero-hit relaxation): every token as a bare term and every phrase
// in double quotes with interior quotes re-doubled. Bare terms come from the
// shared tokenizer (lowercase alphanumeric, stopword-free), so they are valid
// FTS5 terms.
func ftsMatchFromParsed(p parsedQuery) string {
	var b strings.Builder
	wrote := false
	for _, tok := range p.tokens {
		if wrote {
			b.WriteByte(' ')
		}
		b.WriteString(tok)
		wrote = true
	}
	for _, phrase := range p.phrases {
		if wrote {
			b.WriteByte(' ')
		}
		b.WriteString(`"` + strings.ReplaceAll(phrase, `"`, `""`) + `"`)
		wrote = true
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Search SQL builders (shared by the LIKE and FTS paths)
// ---------------------------------------------------------------------------

// likeWhereSQL builds the WHERE clause for the token-AND LIKE path. Every
// token and phrase must appear in lower(title|summary|content); when
// zeroToken the full query is matched as one contiguous phrase (today's
// behavior). All patterns are escaped with the existing escapeLike helper and
// bound as parameters.
func likeWhereSQL(p parsedQuery, scope Scope, org, text string) (string, []any) {
	var b strings.Builder
	b.WriteString("WHERE scope = ? AND org = ?")
	args := []any{string(scope), org}
	if p.zeroToken {
		contains := "%" + escapeLike(text) + "%"
		b.WriteString(" AND (lower(title) LIKE ? ESCAPE '\\' OR lower(summary) LIKE ? ESCAPE '\\' OR lower(content) LIKE ? ESCAPE '\\')")
		args = append(args, contains, contains, contains)
		return b.String(), args
	}
	for _, tok := range p.tokens {
		contains := "%" + escapeLike(tok) + "%"
		b.WriteString(" AND (lower(title) LIKE ? ESCAPE '\\' OR lower(summary) LIKE ? ESCAPE '\\' OR lower(content) LIKE ? ESCAPE '\\')")
		args = append(args, contains, contains, contains)
	}
	for _, phrase := range p.phrases {
		contains := "%" + escapeLike(phrase) + "%"
		b.WriteString(" AND (lower(title) LIKE ? ESCAPE '\\' OR lower(summary) LIKE ? ESCAPE '\\' OR lower(content) LIKE ? ESCAPE '\\')")
		args = append(args, contains, contains, contains)
	}
	return b.String(), args
}

// ftsWhereSQL builds the FTS candidate WHERE: scope/org plus the memories
// rowid in the memories_fts MATCH result. match is the bound MATCH string.
func ftsWhereSQL(match string, scope Scope, org string) (string, []any) {
	where := "WHERE scope = ? AND org = ? AND rowid IN (SELECT rowid FROM memories_fts WHERE memories_fts MATCH ?)"
	return where, []any{string(scope), org, match}
}

// rankCaseSQL builds the ORDER BY rank CASE fragment and its bound arguments.
// Ranks: 0 full query equals title, 1 full query (phrase) in title, 2 in
// summary, 3 in content, 4 all tokens+phrases in title, 5 in summary, 6 in
// content, else 7 (tokens spread across fields). Ties break by created DESC,
// title ASC, then id ASC on both backends.
func rankCaseSQL(p parsedQuery, text string) (string, []any) {
	var b strings.Builder
	var args []any
	b.WriteString("CASE\n  WHEN lower(title) = lower(?) THEN 0")
	args = append(args, text)
	phrase := "%" + escapeLike(text) + "%"
	b.WriteString("\n  WHEN lower(title) LIKE ? ESCAPE '\\' THEN 1\n  WHEN lower(summary) LIKE ? ESCAPE '\\' THEN 2\n  WHEN lower(content) LIKE ? ESCAPE '\\' THEN 3")
	args = append(args, phrase, phrase, phrase)
	if !p.zeroToken {
		for _, field := range []struct {
			name string
			rank int
		}{{"title", 4}, {"summary", 5}, {"content", 6}} {
			b.WriteString("\n  WHEN ")
			first := true
			for _, tok := range p.tokens {
				if !first {
					b.WriteString(" AND ")
				}
				first = false
				b.WriteString("lower(" + field.name + ") LIKE ? ESCAPE '\\'")
				args = append(args, "%"+escapeLike(tok)+"%")
			}
			for _, phrase := range p.phrases {
				if !first {
					b.WriteString(" AND ")
				}
				first = false
				b.WriteString("lower(" + field.name + ") LIKE ? ESCAPE '\\'")
				args = append(args, "%"+escapeLike(phrase)+"%")
			}
			b.WriteString(fmt.Sprintf(" THEN %d", field.rank))
		}
	}
	b.WriteString("\n  ELSE 7 END, created DESC, title ASC, id ASC")
	return b.String(), args
}

// ---------------------------------------------------------------------------
// sqliteStore search execution
// ---------------------------------------------------------------------------

// querySearch runs one parameterized search query and scans the result rows.
func (s *sqliteStore) querySearch(ctx context.Context, db *sql.DB, query string, args []any) ([]Result, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var tags string
		if err := rows.Scan(&r.ID, &r.Scope, &r.Org, &r.Title, &r.Verdict, &tags, &r.Created, &r.Snippet); err != nil {
			return nil, err
		}
		r.Tags = splitTags(tags)
		out = append(out, r)
	}
	return out, rows.Err()
}

// searchLike runs the token-AND LIKE search for one parsed query.
func (s *sqliteStore) searchLike(ctx context.Context, db *sql.DB, scope Scope, org, text string, p parsedQuery, limit int) ([]Result, error) {
	where, args := likeWhereSQL(p, scope, org, text)
	rank, rankArgs := rankCaseSQL(p, text)
	query := "SELECT id, scope, org, title, verdict, tags, created, summary FROM memories\n" +
		where + "\nORDER BY " + rank + "\nLIMIT ?"
	args = append(args, rankArgs...)
	args = append(args, limit)
	return s.querySearch(ctx, db, query, args)
}

// searchFTS runs the FTS MATCH candidate search. The MATCH string is bound as
// one parameter; the final ORDER BY is the rank CASE (identical to the LIKE
// path), so ordering pins hold on both paths.
func (s *sqliteStore) searchFTS(ctx context.Context, db *sql.DB, scope Scope, org, text, match string, p parsedQuery, limit int) ([]Result, error) {
	where, args := ftsWhereSQL(match, scope, org)
	rank, rankArgs := rankCaseSQL(p, text)
	query := "SELECT id, scope, org, title, verdict, tags, created, summary FROM memories\n" +
		where + "\nORDER BY " + rank + "\nLIMIT ?"
	args = append(args, rankArgs...)
	args = append(args, limit)
	return s.querySearch(ctx, db, query, args)
}

// searchTokens runs one token-AND search for the parsed query p. On a build
// with FTS5 it prefers the FTS MATCH path for clean queries and falls back to
// the LIKE path when the index is missing or the query is not clean. The
// MATCH path is taken only when every parsed FTS part is a plain token
// (ftsPartsAllPlainTokens): any query with a prefix or phrase part always
// runs the LIKE path, whose substring semantics are authoritative, so
// FTS-enabled and FTS-disabled results stay identical. The full FTS parts
// (plain tokens) are used only for the unrelaxed query (drop == 0); a relaxed
// search rebuilds the MATCH from the reduced token set so the FTS candidate
// set stays aligned with the LIKE token-AND.
func (s *sqliteStore) searchTokens(ctx context.Context, db *sql.DB, scope Scope, org, text string, p parsedQuery, parts []ftsPart, clean bool, drop int, limit int) ([]Result, error) {
	if s.fts && clean && len(p.tokens) > 0 && len(parts) > 0 && ftsPartsAllPlainTokens(parts) {
		var match string
		if drop == 0 {
			match = buildFTSMatch(parts)
		} else {
			match = ftsMatchFromParsed(p)
		}
		if match != "" {
			res, err := s.searchFTS(ctx, db, scope, org, text, match, p, limit)
			if err == nil {
				return res, nil
			}
			// The index is missing or unusable (e.g. a manually dropped
			// memories_fts): fail closed to the LIKE path below, correct results
			// and no error.
		}
	}
	return s.searchLike(ctx, db, scope, org, text, p, limit)
}
