package controller

import "testing"

func panelReport(hostVerdict string, finalIDs ...string) map[string]any {
	dispositions := make([]any, 0, len(finalIDs))
	for _, id := range finalIDs {
		dispositions = append(dispositions, map[string]any{
			"member_id":        "correctness",
			"finding_id":       "m-" + id,
			"disposition":      "accepted",
			"final_finding_id": id,
		})
	}
	return map[string]any{
		"host_verdict": hostVerdict,
		"dispositions": dispositions,
		"summary":      "panel round",
	}
}

// TestPanelFindingIDSetReadsDispositions is the half of the stall guard that
// silently did nothing. A panel persists a PanelFinalReport, which has no
// "findings" key, so the id set was always empty and no two rounds ever
// compared equal - the guard could never report a stalled panel even after its
// kind check let panels through.
func TestPanelFindingIDSetReadsDispositions(t *testing.T) {
	got := findingIDSet(panelReport("changes_requested", "f-1", "f-2"))
	if len(got) != 2 || !got["f-1"] || !got["f-2"] {
		t.Fatalf("findingIDSet(panel report) = %v, want the two disposition ids", got)
	}
}

// TestPanelFindingIDSetFallsBackToMemberID covers a synthesizer that left the
// final id blank: the member's own finding id still identifies the finding.
func TestPanelFindingIDSetFallsBackToMemberID(t *testing.T) {
	out := map[string]any{"dispositions": []any{
		map[string]any{"member_id": "security", "finding_id": "m-9", "final_finding_id": ""},
	}}
	got := findingIDSet(out)
	if len(got) != 1 || !got["m-9"] {
		t.Fatalf("findingIDSet = %v, want the member finding id as a fallback", got)
	}
}

// TestReviewRequestedChangesAcceptsBothShapes pins the verdict read for both
// reviewer kinds: an agent_gate says verdict, a panel says host_verdict.
func TestReviewRequestedChangesAcceptsBothShapes(t *testing.T) {
	if !reviewRequestedChanges(map[string]any{"verdict": "changes_requested"}) {
		t.Error("an agent_gate verdict of changes_requested was not recognized")
	}
	if !reviewRequestedChanges(panelReport("changes_requested", "f-1")) {
		t.Error("a panel host_verdict of changes_requested was not recognized")
	}
	if reviewRequestedChanges(panelReport("approved", "f-1")) {
		t.Error("an approved panel was read as requesting changes")
	}
	if reviewRequestedChanges(map[string]any{}) {
		t.Error("an output with no verdict at all was read as requesting changes")
	}
}

// TestIdenticalPanelRoundsCompareEqual states the property the guard depends
// on: two rounds that raise the same findings must produce equal sets, and a
// round that raises a different finding must not.
func TestIdenticalPanelRoundsCompareEqual(t *testing.T) {
	first := findingIDSet(panelReport("changes_requested", "f-1", "f-2"))
	same := findingIDSet(panelReport("changes_requested", "f-2", "f-1"))
	if !equalStringSets(first, same) {
		t.Fatalf("identical panel rounds compared unequal: %v vs %v", first, same)
	}
	moved := findingIDSet(panelReport("changes_requested", "f-1", "f-3"))
	if equalStringSets(first, moved) {
		t.Fatalf("a panel round that raised a different finding compared equal: %v vs %v", first, moved)
	}
}
