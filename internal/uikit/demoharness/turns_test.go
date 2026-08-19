package demoharness

import (
	"sort"
	"testing"
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
	if len(scripts) != 8 {
		t.Fatalf("got %d scripts, want 8 (one per required turn shape)", len(scripts))
	}
	for i, s := range scripts {
		if len(s.Before) == 0 {
			t.Errorf("script %d has an empty Before", i)
		}
	}
	// The last script (approval.json) is the only one with a decision
	// fork: its Before ends on tool.pending and it carries both
	// continuations.
	last := scripts[len(scripts)-1]
	if len(last.OnApprove) == 0 || len(last.OnDeny) == 0 {
		t.Error("expected the approval script to carry both on_approve and on_deny continuations")
	}
	for i, s := range scripts[:len(scripts)-1] {
		if len(s.OnApprove) != 0 || len(s.OnDeny) != 0 {
			t.Errorf("script %d is not the approval turn but carries a decision continuation", i)
		}
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
