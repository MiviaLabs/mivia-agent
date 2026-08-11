package memory

import (
	"strings"
	"testing"
)

func TestEntryRenderProducesStrictTemplate(t *testing.T) {
	e := Entry{
		Title:   "SQLite WAL busy timeout",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Tags:    []string{"sqlite", "persistence"},
		Created: "2026-08-09",
		Summary: "Set busy_timeout=5000 to survive concurrent writer contention.",
		Good:    "- Writes stopped failing with SQLITE_BUSY",
		Bad:     "- None observed",
		Why:     "Parallel agents share one database file.",
		References: []string{
			"internal/storage/sqlite.go",
		},
	}
	got := e.Render()
	for _, want := range []string{
		"# SQLite WAL busy timeout",
		"scope: project",
		"verdict: good",
		"tags: sqlite, persistence",
		"created: 2026-08-09",
		"## Summary",
		"Set busy_timeout=5000 to survive concurrent writer contention.",
		"## What worked",
		"## What did not work",
		"## Why",
		"Parallel agents share one database file.",
		"## References",
		"- internal/storage/sqlite.go",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered entry missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "# ") {
		t.Errorf("entry must start with the title heading, got:\n%s", got)
	}
}

func TestEntryRenderParseRoundTrip(t *testing.T) {
	e := Entry{
		Title:      "Build cache ordering",
		Scope:      ScopeOrg,
		Verdict:    VerdictMixed,
		Tags:       []string{"build", "ci"},
		Created:    "2026-08-09",
		Summary:    "Cache key order changes hit rate.",
		Good:       "- Inputs first, then version",
		Bad:        "- Ignoring the toolchain broke rebuilds",
		Why:        "The key must cover every input that changes output.",
		References: []string{"docs/build.md"},
	}
	rendered := e.Render()
	parsed, err := Parse([]byte(rendered))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Title != e.Title || parsed.Scope != e.Scope || parsed.Verdict != e.Verdict {
		t.Errorf("round trip lost metadata: got %+v", parsed)
	}
	if parsed.Summary != e.Summary || parsed.Good != e.Good || parsed.Bad != e.Bad || parsed.Why != e.Why {
		t.Errorf("round trip lost body: got %+v", parsed)
	}
	if len(parsed.Tags) != len(e.Tags) || parsed.Tags[0] != e.Tags[0] {
		t.Errorf("round trip lost tags: got %v", parsed.Tags)
	}
	if len(parsed.References) != len(e.References) || parsed.References[0] != e.References[0] {
		t.Errorf("round trip lost references: got %v", parsed.References)
	}
}

func TestParseToleratesHandEditedContent(t *testing.T) {
	// A human edits the file: extra sections, missing verdict, trailing text.
	data := []byte("# Tolerated title\n\nscope: project\ncreated: 2026-08-09\n\n## Summary\nHello world\n\n## Extra section\ngarbage here\nTail noise before why\n## Why\nBecause.\n")
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse must not fail on hand-edited content: %v", err)
	}
	if parsed.Title != "Tolerated title" {
		t.Errorf("title = %q", parsed.Title)
	}
	if parsed.Summary != "Hello world" || parsed.Why != "Because." {
		t.Errorf("body = %+v", parsed)
	}
}

func TestParseNeverPanicsOnMalformedInput(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		{},
		[]byte(""),
		[]byte("no title at all"),
		[]byte("---\nscope: project\n---\nbody"),
		[]byte("# \n\nscope: project\n\n## Summary\n"),
		[]byte(strings.Repeat("# x\n", 10000)),
		[]byte("# T\n\nscope: project\nverdict: good\n\n## Summary\n" + strings.Repeat("a", 100000)),
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse panicked on input %q: %v", data[:min(len(data), 40)], r)
				}
			}()
			_, _ = Parse(data)
		}()
	}
}

func TestEntryValidate(t *testing.T) {
	base := Entry{
		Title:   "Valid",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Created: "2026-08-09",
		Summary: "Summary",
		Why:     "Why",
	}
	lim := Limits{}
	if err := base.Validate(lim); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}

	cases := []struct {
		name  string
		mut   func(*Entry)
		check string
	}{
		{"empty title", func(e *Entry) { e.Title = "" }, "title"},
		{"long title", func(e *Entry) { e.Title = strings.Repeat("t", 121) }, "title"},
		{"empty summary", func(e *Entry) { e.Summary = "" }, "summary"},
		{"long summary", func(e *Entry) { e.Summary = strings.Repeat("s", 401) }, "summary"},
		{"empty why", func(e *Entry) { e.Why = "" }, "why"},
		{"long why", func(e *Entry) { e.Why = strings.Repeat("w", 1001) }, "why"},
		{"bad scope", func(e *Entry) { e.Scope = Scope("elsewhere") }, "scope"},
		{"bad verdict", func(e *Entry) { e.Verdict = Verdict("maybe") }, "verdict"},
		{"bad created", func(e *Entry) { e.Created = "09-08-2026" }, "created"},
		{"too many tags", func(e *Entry) { e.Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} }, "tags"},
		{"long tag", func(e *Entry) { e.Tags = []string{strings.Repeat("t", 33)} }, "tag"},
		{"long good", func(e *Entry) { e.Good = strings.Repeat("g", 2001) }, "good"},
		{"long bad", func(e *Entry) { e.Bad = strings.Repeat("b", 2001) }, "bad"},
		{"long reference", func(e *Entry) { e.References = []string{strings.Repeat("r", 201)} }, "reference"},
		{"too many references", func(e *Entry) { e.References = make([]string, 9) }, "reference"},
	}
	for _, tc := range cases {
		e := base
		tc.mut(&e)
		err := e.Validate(lim)
		if err == nil {
			t.Errorf("%s: expected validation error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.check) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.check)
		}
	}
}

func TestEntryValidateRejectsControlCharacters(t *testing.T) {
	e := Entry{
		Title:   "T",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Created: "2026-08-09",
		Summary: "S",
		Why:     "W",
	}
	if err := e.Validate(Limits{}); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	e.Summary = "has \x00 nul"
	if err := e.Validate(Limits{}); err == nil {
		t.Fatal("NUL byte must be rejected")
	}
	e.Summary = "line1\nline2\ttab"
	if err := e.Validate(Limits{}); err != nil {
		t.Fatalf("LF and TAB are allowed: %v", err)
	}
}

// TestEntryValidateRejectsCommaInTag is the negative path for the comma-tag
// corruption: the stored template renders tags as ", "-joined single-line
// items and Parse/splitTags split on ",", so a comma inside one tag would
// silently split it into several tags on the next read. Validation must
// refuse a comma in a tag with an error naming the comma.
func TestEntryValidateRejectsCommaInTag(t *testing.T) {
	e := Entry{
		Title:   "T",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Created: "2026-08-09",
		Summary: "S",
		Why:     "W",
		Tags:    []string{"a,b"},
	}
	err := e.Validate(Limits{})
	if err == nil {
		t.Fatal("tag containing a comma must be refused")
	}
	if !strings.Contains(err.Error(), "comma") {
		t.Fatalf("error must mention the comma, got %v", err)
	}
}

// TestEntryTagRoundTripLegalPunctuation is the positive path for the same
// class: legal punctuation that is NOT a comma (for example "C++") must
// survive the Render -> Parse round-trip unchanged. Rejecting commas must
// not corrupt or refuse tags that merely contain punctuation.
func TestEntryTagRoundTripLegalPunctuation(t *testing.T) {
	e := Entry{
		Title:   "T",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Created: "2026-08-09",
		Summary: "S",
		Why:     "W",
		Tags:    []string{"go", "C++", "sql"},
	}
	if err := e.Validate(Limits{}); err != nil {
		t.Fatalf("legal punctuation tags must validate: %v", err)
	}
	parsed, err := Parse([]byte(e.Render()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"go", "C++", "sql"}
	if len(parsed.Tags) != len(want) {
		t.Fatalf("round trip tags = %v, want %v", parsed.Tags, want)
	}
	for i := range want {
		if parsed.Tags[i] != want[i] {
			t.Errorf("round trip tags = %v, want %v (tag %d corrupted)", parsed.Tags, want, i)
		}
	}
}

func TestEntryValidateSizeCap(t *testing.T) {
	e := Entry{
		Title:   "T",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Created: "2026-08-09",
		Summary: "S",
		Why:     "W",
		Bad:     strings.Repeat("b", 1000),
	}
	if err := e.Validate(Limits{MaxEntryBytes: 2048}); err != nil {
		t.Fatalf("entry under cap rejected: %v", err)
	}
	e.Bad = strings.Repeat("b", 100000)
	if err := e.Validate(Limits{MaxEntryBytes: 2048}); err == nil {
		t.Fatal("oversized entry must be rejected")
	}
}

func TestEntryValidateBlockPatterns(t *testing.T) {
	e := Entry{
		Title:   "Deploy steps",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Created: "2026-08-09",
		Summary: "How we ship",
		Why:     "Repeatable releases",
		Good:    "Token sk-x",
	}
	lim := Limits{BlockPatterns: []string{`sk-[A-Za-z0-9]+`}}
	if err := e.Validate(lim); err == nil {
		t.Fatal("content matching a block pattern must be refused")
	}
	e.Good = "Token redacted"
	if err := e.Validate(lim); err != nil {
		t.Fatalf("clean content refused: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
