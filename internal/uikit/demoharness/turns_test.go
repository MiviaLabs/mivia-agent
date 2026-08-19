package demoharness

import (
	"sort"
	"strings"
	"testing"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestScenariosSortedAndNonEmpty(t *testing.T) {
	got := Scenarios()
	if len(got) == 0 {
		t.Fatal("expected at least one known scenario")
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("Scenarios() = %v, want sorted", got)
	}
	found := false
	for _, name := range got {
		if name == DefaultScenario {
			found = true
		}
	}
	if !found {
		t.Errorf("DefaultScenario %q is not in Scenarios() %v", DefaultScenario, got)
	}
}

func TestLoadScenarioFullTourCoversEveryTurnShape(t *testing.T) {
	scripts, err := loadScenario("full-tour")
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 10 {
		t.Fatalf("got %d scripts, want 10 (one per required turn shape)", len(scripts))
	}
	for i, s := range scripts {
		if len(s.Before) == 0 {
			t.Errorf("script %d has an empty Before", i)
		}
	}
	// Exactly the approval turns carry a decision fork: their Before
	// ends on tool.pending and carries both continuations. Found by
	// shape, not position, so appending turn shapes to the tour cannot
	// silently shift the assertion onto the wrong script.
	forks := 0
	for i, s := range scripts {
		pends := false
		for _, ev := range s.Before {
			if _, ok := ev.Body.(uievent.ToolPendingBody); ok {
				pends = true
			}
		}
		if pends || len(s.OnApprove) > 0 || len(s.OnDeny) > 0 {
			forks++
			if !pends || len(s.OnApprove) == 0 || len(s.OnDeny) == 0 {
				t.Errorf("script %d has an incomplete decision fork (pending=%v)", i, pends)
			}
		}
	}
	if forks != 2 {
		t.Errorf("found %d decision forks, want 2 (approval.json, approval_diff.json)", forks)
	}
}

func TestLoadScenarioUnknownNameErrors(t *testing.T) {
	if _, err := loadScenario("does-not-exist"); err == nil {
		t.Error("expected an error for an unknown scenario name")
	}
}

func TestLoadScenarioSmalltalkAndApproval(t *testing.T) {
	if _, err := loadScenario("smalltalk"); err != nil {
		t.Errorf("smalltalk scenario: %v", err)
	}
	if _, err := loadScenario("approval"); err != nil {
		t.Errorf("approval scenario: %v", err)
	}
}

// TestApprovalDiffScriptCarriesAProposedDiff pins the wiring the inline
// diff preview depends on: the approval-diff scenario's tool.pending
// event decodes with a Diff, so the approval prompt has something to
// show before the tool runs.
func TestApprovalDiffScriptCarriesAProposedDiff(t *testing.T) {
	scripts, err := loadScenario("approval-diff")
	if err != nil {
		t.Fatal(err)
	}
	last := scripts[0].Before[len(scripts[0].Before)-1]
	pend, ok := last.Body.(uievent.ToolPendingBody)
	if !ok {
		t.Fatalf("last before-event body is %T, want ToolPendingBody", last.Body)
	}
	if pend.Diff == nil || len(pend.Diff.Hunks) == 0 {
		t.Fatal("approval-diff tool.pending carries no diff; the preview cannot render")
	}
	// The preview caps at uikitconfig.ApprovalDiffPreviewLines; this
	// script deliberately exceeds it so the cap note is exercised too.
	n := len(pend.Diff.Hunks)
	for _, h := range pend.Diff.Hunks {
		n += len(h.Lines)
	}
	if n <= uikitconfig.ApprovalDiffPreviewLines {
		t.Errorf("script has %d diff lines, want more than the %d-line cap", n, uikitconfig.ApprovalDiffPreviewLines)
	}
}

// TestLoadScenarioReportsFileAndDecodeErrors covers both failure arms:
// a mapped file that does not exist, and one that is not valid JSON.
// The scenario map is edited in-test and restored, because loadScenario
// only reads files the map names.
func TestLoadScenarioReportsFileAndDecodeErrors(t *testing.T) {
	orig := scenarios["approval-diff"]
	defer func() { scenarios["approval-diff"] = orig }()

	scenarios["approval-diff"] = []string{"does-not-exist.json"}
	_, err := loadScenario("approval-diff")
	if err == nil || !strings.Contains(err.Error(), "read does-not-exist.json") {
		t.Errorf("missing file: got %v, want a read error naming the file", err)
	}

	scenarios["approval-diff"] = []string{"bad.json"}
	_, err = loadScenario("approval-diff")
	if err == nil || !strings.Contains(err.Error(), "decode bad.json") {
		t.Errorf("bad JSON: got %v, want a decode error naming the file", err)
	}
}
