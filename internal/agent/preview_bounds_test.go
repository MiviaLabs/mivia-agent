package agent

// Bounding helpers for the tool preview. These are the last line before a
// preview breaks the size contract, and shrinkRunes in particular is what
// stops a multi-byte character being split into invalid UTF-8 that
// json.Marshal would re-encode as U+FFFD.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestShrinkRunesLeavesAShortStringAlone pins the fast path: a string
// already inside the budget is returned untouched, with no ellipsis.
func TestShrinkRunesLeavesAShortStringAlone(t *testing.T) {
	const in = "short"
	if got := shrinkRunes(in, 32); got != in {
		t.Fatalf("shrinkRunes(%q, 32) = %q, want it unchanged", in, got)
	}
}

// TestShrinkRunesCutsOnARuneBoundary is the reason this helper exists: the
// cut must land between runes, so the result stays valid UTF-8 even when
// the budget falls in the middle of a multi-byte character.
func TestShrinkRunesCutsOnARuneBoundary(t *testing.T) {
	// Four 3-byte runes: a byte-wise cut at 5 would split the second one.
	const in = "日本語訳"
	got := shrinkRunes(in, 2)

	if !utf8.ValidString(got) {
		t.Fatalf("shrinkRunes(%q, 2) = %q, which is not valid UTF-8", in, got)
	}
	if !strings.HasSuffix(got, "\u2026") {
		t.Fatalf("shrinkRunes(%q, 2) = %q, want it to end with an ellipsis", in, got)
	}
	if got != "日本\u2026" {
		t.Fatalf("shrinkRunes(%q, 2) = %q, want %q", in, got, "日本…")
	}
}

// TestShrinkRunesWithNoBudgetIsJustAnEllipsis pins the budget<=0 arm: the
// caller still gets a valid marker rather than an empty string it cannot
// distinguish from "no value".
func TestShrinkRunesWithNoBudgetIsJustAnEllipsis(t *testing.T) {
	if got := shrinkRunes("anything", 0); got != "\u2026" {
		t.Fatalf("shrinkRunes(_, 0) = %q, want a bare ellipsis", got)
	}
	if got := shrinkRunes("anything", -5); got != "\u2026" {
		t.Fatalf("shrinkRunes(_, -5) = %q, want a bare ellipsis", got)
	}
}

// TestShrinkJSONPreviewFallsBackWhenEvenTheSmallestLeafBudgetIsTooBig
// covers shrinkJSONPreview's exhausted-ladder return: when no leaf budget
// down to 0 gets the payload under the cap, the preview is refused rather
// than emitted oversized. maxBytes=1 cannot fit even `{}`.
func TestShrinkJSONPreviewFallsBackWhenEvenTheSmallestLeafBudgetIsTooBig(t *testing.T) {
	raw := `{"a":"` + strings.Repeat("x", 200) + `"}`

	got, ok := shrinkJSONPreview(raw, 1)
	if ok {
		t.Fatalf("shrinkJSONPreview(_, 1) = (%q, true), want a refusal", got)
	}
	if got != "" {
		t.Fatalf("refused shrinkJSONPreview returned %q, want the empty string", got)
	}
}

// TestShrinkJSONPreviewSucceedsAtALargerBudget is the positive counterpart:
// with room to work the ladder finds a leaf budget that fits, and the
// result is valid JSON under the cap.
func TestShrinkJSONPreviewSucceedsAtALargerBudget(t *testing.T) {
	raw := `{"a":"` + strings.Repeat("x", 200) + `"}`

	got, ok := shrinkJSONPreview(raw, 128)
	if !ok {
		t.Fatal("shrinkJSONPreview(_, 128) refused, want a shrunken preview")
	}
	if len(got) > 128 {
		t.Fatalf("shrinkJSONPreview returned %d bytes, want <= 128", len(got))
	}
	if !strings.HasPrefix(got, "{") {
		t.Fatalf("shrinkJSONPreview returned %q, want a JSON object", got)
	}
}

// TestShrinkJSONPreviewRefusesUnparseableInput pins the marshal-side
// refusal: input that is not JSON at all cannot be shrunk structurally and
// must fall back rather than be emitted as-is.
func TestShrinkJSONPreviewRefusesUnparseableInput(t *testing.T) {
	if got, ok := shrinkJSONPreview("this is not json", 8); ok {
		t.Fatalf("shrinkJSONPreview(non-JSON) = (%q, true), want a refusal", got)
	}
}

// TestStructurallyReduceDispatchOutputRefusesWhenStillOversized covers the
// post-reduction size check: reducing the rows is not guaranteed to get
// under the cap, and when it does not the preview is refused.
func TestStructurallyReduceDispatchOutputRefusesWhenStillOversized(t *testing.T) {
	raw := `{"results":[{"id":"` + strings.Repeat("y", 400) + `"}]}`

	if got, ok := structurallyReduceDispatchOutput(raw, 4); ok {
		t.Fatalf("structurallyReduceDispatchOutput(_, 4) = (%q, true), want a refusal", got)
	}
}
