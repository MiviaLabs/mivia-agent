package memory

import (
	"strings"
	"testing"
	"time"
)

// baseGapEntry returns a valid entry: it passes every Validate check except
// the one each gap test deliberately breaks.
func baseGapEntry() Entry {
	return Entry{
		Title:   "Gap",
		Scope:   ScopeProject,
		Verdict: VerdictGood,
		Created: "2026-08-09",
		Summary: "S",
		Why:     "W",
	}
}

// TestGapValidateRejectsControlCharInTag covers the tag control-character
// branch of Validate: a tag that passes the length checks but contains a line
// break (or other C0 control) must be refused, because tags are stored on one
// line and a break would corrupt the rendered template.
func TestGapValidateRejectsControlCharInTag(t *testing.T) {
	e := baseGapEntry()
	e.Tags = []string{"alpha\x00"}
	err := e.Validate(Limits{})
	if err == nil {
		t.Fatal("expected tag control-character rejection")
	}
	if !strings.Contains(err.Error(), "tag must not contain line breaks") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGapValidateRejectsControlCharInReference covers the reference
// control-character branch of Validate: a reference that passes the length
// checks but contains a line break (or other C0 control) must be refused,
// because references are stored on one line each.
func TestGapValidateRejectsControlCharInReference(t *testing.T) {
	e := baseGapEntry()
	e.References = []string{"https://example.com/ref\x01"}
	err := e.Validate(Limits{})
	if err == nil {
		t.Fatal("expected reference control-character rejection")
	}
	if !strings.Contains(err.Error(), "reference must not contain line breaks") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGapValidateRejectsOversizedRender covers the rendered-size guard of
// Validate: every field passes its per-field limit, but the rendered Markdown
// still exceeds the configured cap.
func TestGapValidateRejectsOversizedRender(t *testing.T) {
	e := baseGapEntry()
	err := e.Validate(Limits{MaxEntryBytes: 64})
	if err == nil {
		t.Fatal("expected rendered-size rejection")
	}
	if !strings.Contains(err.Error(), "exceeds the") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGapValidateRejectsInvalidBlockPattern covers the invalid-regex branch
// of the block-pattern loop: an unparseable pattern must fail with an
// "invalid block pattern" error rather than panic or match.
func TestGapValidateRejectsInvalidBlockPattern(t *testing.T) {
	e := baseGapEntry()
	err := e.Validate(Limits{BlockPatterns: []string{"["}})
	if err == nil {
		t.Fatal("expected invalid block pattern rejection")
	}
	if !strings.Contains(err.Error(), "invalid block pattern") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGapNormalizeOrgIDRejectsUnsupportedCharacter covers the default branch
// of NormalizeOrgID's rune switch: a character outside the allowed set (and
// not whitespace or control) must be refused.
func TestGapNormalizeOrgIDRejectsUnsupportedCharacter(t *testing.T) {
	for _, in := range []string{"acme!", "acme@corp", "héllo", "acme?"} {
		if got, err := NormalizeOrgID(in); err == nil {
			t.Errorf("NormalizeOrgID(%q) = %q, want error", in, got)
		} else if !strings.Contains(err.Error(), "unsupported character") {
			t.Errorf("NormalizeOrgID(%q): unexpected error %v", in, err)
		}
	}
}

// TestGapParseEmptyInputReturnsZeroEntry documents that Parse tolerates empty
// or nil input and returns a zero Entry (it takes the empty-title early
// return, not the len(lines) == 0 guard, which strings.Split can never hit).
func TestGapParseEmptyInputReturnsZeroEntry(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("")} {
		e, err := Parse(data)
		if err != nil {
			t.Fatalf("Parse(%q): %v", data, err)
		}
		if e.Title != "" || e.Summary != "" || e.Scope != "" {
			t.Fatalf("Parse(%q) = %+v, want zero Entry", data, e)
		}
	}
}

// TestGapValidateRejectsUnknownImportance covers the importance branch of
// Validate: an empty importance is allowed (the store defaults it), but any
// value outside high/medium/low must be refused, so a typo never reaches the
// rendered file as an unrecognized frontmatter value.
func TestGapValidateRejectsUnknownImportance(t *testing.T) {
	e := baseGapEntry()
	e.Importance = Importance("urgent")
	err := e.Validate(Limits{})
	if err == nil {
		t.Fatal("expected unknown-importance rejection")
	}
	if !strings.Contains(err.Error(), "importance must be one of high, medium, low") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "urgent") {
		t.Fatalf("error %v must name the rejected value", err)
	}
}

// TestGapRenderProtocolFileDefaultsUpdatedToToday covers the empty-Created
// branch of RenderProtocolFile: the frontmatter's updated field is mandatory,
// so an entry with no Created date is stamped with today rather than emitting
// an empty value the memories gate would reject.
func TestGapRenderProtocolFileDefaultsUpdatedToToday(t *testing.T) {
	e := baseGapEntry()
	e.Created = ""
	got := e.RenderProtocolFile("gap_entry")
	want := "updated: " + time.Now().Format("2006-01-02") + "\n"
	if !strings.Contains(got, want) {
		t.Fatalf("rendered file lacks %q:\n%s", want, got)
	}

	dated := baseGapEntry()
	dated.Created = "2026-08-09"
	if datedOut := dated.RenderProtocolFile("gap_entry"); !strings.Contains(datedOut, "updated: 2026-08-09\n") {
		t.Fatalf("a recorded Created date must be used verbatim:\n%s", datedOut)
	}
}
