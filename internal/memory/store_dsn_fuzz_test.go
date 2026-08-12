package memory

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzSQLiteMemoryDSN asserts the sqliteMemoryDSN contract for every input
// path, mirroring the storage package's FuzzSQLiteDSN: a path without '?'
// keeps the plain path+"?"+pragmas DSN byte-for-byte; a path with '?' becomes
// a file: URI whose path portion contains no literal '?' and whose
// percent-decoded path portion round-trips to the input path. The invariant
// matters because the modernc.org/sqlite driver splits a DSN at the first
// literal '?', so any literal '?' inside the path portion would truncate the
// filename and open the wrong database file. sqliteMemoryDSN is a pure string
// function over a closed input space, so this deterministic target is the
// bounded host fuzz gate for the fix.
func FuzzSQLiteMemoryDSN(f *testing.F) {
	for _, seed := range []string{
		"events.db",           // plain
		"ledger?part.db",      // single '?'
		"a?b?c.db",            // double '?'
		"?leading.db",         // leading '?'
		"/tmp/ctx?name.db",    // absolute path with '?'
		"dir/with%percent.db", // '%' in path
		"dir/with#hash.db",    // '#' in path
		"dir/with space.db",   // space in path
		"dir/with;semi.db",    // ';' in path
		"dir/with,comma.db",   // ',' in path
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		dsn := sqliteMemoryDSN(path)
		if !strings.Contains(path, "?") {
			want := path + "?" + pragmaMemoryDSNParams
			if dsn != want {
				t.Fatalf("sqliteMemoryDSN(%q) = %q, want %q", path, dsn, want)
			}
			return
		}
		if !strings.HasPrefix(dsn, "file:") {
			t.Fatalf("sqliteMemoryDSN(%q) = %q, want a file: URI", path, dsn)
		}
		rest := strings.TrimPrefix(dsn, "file:")
		sep := strings.IndexByte(rest, '?')
		if sep < 0 {
			t.Fatalf("sqliteMemoryDSN(%q) = %q has no query separator", path, dsn)
		}
		encoded := rest[:sep]
		if strings.Contains(encoded, "?") {
			t.Fatalf("path portion of sqliteMemoryDSN(%q) = %q still contains a literal '?'", path, encoded)
		}
		decoded, err := url.PathUnescape(encoded)
		if err != nil {
			t.Fatalf("PathUnescape(%q): %v", encoded, err)
		}
		if decoded != path {
			t.Fatalf("PathUnescape(%q) = %q, want %q", encoded, decoded, path)
		}
	})
}

// FuzzMemorySearchQuery asserts the tokenizer and FTS eligibility/buildMatch
// contracts for every query string: no panic; tokens are non-empty, composed
// of token runes, stopword-free, deduplicated preserving first-seen order, and
// bounded by the documented caps; a clean query classifies stably through
// eligibility and MATCH building. The tokenizer and the FTS parser are pure
// functions over a closed string input space, so this deterministic target is
// the bounded host fuzz gate for Phase 1/2.
func FuzzMemorySearchQuery(f *testing.F) {
	var parts65 strings.Builder
	for i := 0; i < 65; i++ {
		if i > 0 {
			parts65.WriteByte(' ')
		}
		parts65.WriteString("w" + string(rune('a'+i%26)))
	}
	for _, seed := range []string{
		"DeepSeek 400",             // multi-word
		"v4-flash",                 // punctuation-split
		"the of",                   // stopword-only
		`"exact phrase"`,           // quoted phrase
		"tok*",                     // prefix token
		"AND OR NOT NEAR",          // reserved operator words
		"café naïve",               // non-ASCII
		"",                         // empty
		strings.Repeat("a", 65),    // oversized token
		parts65.String(),           // oversized part count
		"cache cache invalidation", // duplicate tokens
		"\x01cache\t\n",            // control characters
		"internal/memory/store.go", // file path (not clean)
		`say ""hi""`,               // doubled-quote phrase
		"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6", // 60-char token
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, q string) {
		tokens, _ := tokenize(q)
		seen := make(map[string]bool, len(tokens))
		for _, tok := range tokens {
			if tok == "" {
				t.Fatalf("tokenize(%q) produced an empty token", q)
			}
			if len([]rune(tok)) > maxTokenLen {
				t.Fatalf("tokenize(%q) produced a token over %d runes", q, maxTokenLen)
			}
			for _, r := range tok {
				if !isTokenRune(r) {
					t.Fatalf("tokenize(%q) produced token %q containing separator rune %q", q, tok, r)
				}
			}
			if _, stop := stopwords[tok]; stop {
				t.Fatalf("tokenize(%q) produced stopword token %q", q, tok)
			}
			if seen[tok] {
				t.Fatalf("tokenize(%q) produced duplicate token %q", q, tok)
			}
			seen[tok] = true
		}
		if len(tokens) > maxQueryTokens {
			t.Fatalf("tokenize(%q) produced %d tokens, over the cap %d", q, len(tokens), maxQueryTokens)
		}
		// Eligibility and MATCH building must not panic on any input.
		parts, ok := parseFTSQuery(q)
		if ok {
			match := buildFTSMatch(parts)
			if strings.Contains(match, "\x00") {
				t.Fatalf("buildFTSMatch(%q) = %q contains a NUL byte", q, match)
			}
		}
		_ = ftsMatchFromParsed(parseQuery(q))
		_ = extractPhrases(q)
	})
}
