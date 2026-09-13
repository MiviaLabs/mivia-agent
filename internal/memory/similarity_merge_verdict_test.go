package memory

import "testing"

// A neutral verdict on an incoming save means "no assessment recorded" (the
// tool layer defaults it). It must not clobber an assertive verdict - and the
// Good/Bad text that backs it - that an existing near-duplicate already
// carries.
func TestMergeEntriesNeutralIncomingKeepsAssertiveVerdict(t *testing.T) {
	existing := Entry{
		Title:      "pinned runner image",
		Summary:    "the runner image is pinned",
		Verdict:    VerdictBad,
		Good:       "found the drift quickly",
		Bad:        "the image drifted anyway",
		Importance: ImportanceMedium,
	}
	incoming := Entry{
		Title:   "pinned runner image",
		Summary: "the runner image is pinned in CI",
		Verdict: VerdictNeutral,
	}
	got, _ := MergeEntries(existing, incoming)
	if got.Verdict != VerdictBad {
		t.Fatalf("Verdict = %q, want %q (neutral incoming must not clobber assertive existing)", got.Verdict, VerdictBad)
	}
	if got.Good != existing.Good || got.Bad != existing.Bad {
		t.Fatalf("Good/Bad = %q/%q, want existing %q/%q", got.Good, got.Bad, existing.Good, existing.Bad)
	}
	// The richer summary is still adopted: content updates stay welcome.
	if got.Summary != incoming.Summary {
		t.Fatalf("Summary = %q, want updated %q", got.Summary, incoming.Summary)
	}
}

// An assertive incoming verdict still wins over a neutral/empty existing one.
func TestMergeEntriesAssertiveIncomingWinsOverNeutralExisting(t *testing.T) {
	existing := Entry{Title: "x", Summary: "x facts", Verdict: VerdictNeutral}
	incoming := Entry{Title: "x", Summary: "x facts richer", Verdict: VerdictGood}
	got, _ := MergeEntries(existing, incoming)
	if got.Verdict != VerdictGood {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, VerdictGood)
	}
}
