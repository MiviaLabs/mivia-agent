package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestContextUsageAppliesTheSameCalibrationAsTheTrigger pins the fix for the
// "112% context" defect: the gauge and the compaction trigger must measure a
// history with ONE ruler.
//
// The planner scores a CALIBRATED cost against 80% of the budget
// (contextmgr.Plan). While ContextUsage reported the RAW estimate, a session
// whose estimator over-counted showed a percentage inflated by 1/ratio - up to
// 5x at the calibration floor - so the badge could sit far above 100% while
// the planner correctly measured the same messages below the trigger and never
// compacted. Neither number is wrong on its own; measuring with two rulers is.
func TestContextUsageAppliesTheSameCalibrationAsTheTrigger(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system prompt"},
		{Role: provider.RoleUser, Content: "a reasonably long user objective to price"},
		{Role: provider.RoleAssistant, Content: "an assistant reply that also costs tokens"},
	}

	newSession := func() *Session {
		s := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
		s.Messages = append([]provider.Message(nil), messages...)
		s.MaxContextTokens = 1000
		return s
	}

	uncalibrated := newSession().ContextUsage()
	if uncalibrated.UsedTokens <= 0 {
		t.Fatalf("baseline used tokens = %d, want a positive estimate", uncalibrated.UsedTokens)
	}

	// A ratio below 1.0 is the reachable case: the len/4 heuristic over-counts
	// relative to what the provider actually bills, which is exactly when the
	// raw gauge ran ahead of the trigger.
	corrected := newSession()
	corrected.Calibration = contextmgr.Calibration{Ratio: 0.5, Samples: 4}
	got := corrected.ContextUsage()

	want := corrected.Calibration.Apply(uncalibrated.UsedTokens)
	if got.UsedTokens != want {
		t.Errorf("calibrated used tokens = %d, want %d (the same contextmgr.Calibration.Apply the planner uses)",
			got.UsedTokens, want)
	}
	if got.UsedTokens >= uncalibrated.UsedTokens {
		t.Errorf("a ratio below 1.0 must lower the reported usage: got %d, uncalibrated %d",
			got.UsedTokens, uncalibrated.UsedTokens)
	}
	if got.Percent != got.UsedTokens*100/got.BudgetTokens {
		t.Errorf("percent %d is not derived from the calibrated used/budget (%d/%d)",
			got.Percent, got.UsedTokens, got.BudgetTokens)
	}
}

// TestCalibrationApplyMatchesThePlannerConvention pins the two rules
// Calibration.Apply must share with PlanInput.CalibrationRatio: a calibration
// with no samples applies no correction (callers leave the planner field unset
// until the first observation lands), and one with samples scales by its
// ratio.
func TestCalibrationApplyMatchesThePlannerConvention(t *testing.T) {
	if got := (contextmgr.Calibration{}).Apply(1000); got != 1000 {
		t.Errorf("zero-value calibration applied a correction: got %d, want 1000", got)
	}
	// A ratio is only meaningful alongside samples; a stray ratio with no
	// samples must still be inert, matching buildPrepareInput's own guard.
	if got := (contextmgr.Calibration{Ratio: 0.5}).Apply(1000); got != 1000 {
		t.Errorf("sample-less calibration applied its ratio: got %d, want 1000", got)
	}
	if got := (contextmgr.Calibration{Ratio: 0.5, Samples: 3}).Apply(1000); got != 500 {
		t.Errorf("calibrated estimate = %d, want 500", got)
	}
}
