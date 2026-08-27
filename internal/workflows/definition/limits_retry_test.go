package definition

import (
	"testing"
)

// TestCompile_LimitsCarryRetryKnobs verifies the workflow-level retry knobs
// (max_on_failure_reentries, max_transient_step_retries) survive compilation
// into the compiled workflow, and that leaving them at 0 keeps the zero value
// (the controller applies its defaults at runtime).
func TestCompile_LimitsCarryRetryKnobs(t *testing.T) {
	wf := newMinimalWorkflow("test-limits-retry-knobs")
	wf.Limits = Limits{
		MaxStepAttempts:         16,
		MaxOnFailureReentries:   7,
		MaxTransientStepRetries: 5,
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}
	if cw.Limits.MaxOnFailureReentries != 7 {
		t.Errorf("Limits.MaxOnFailureReentries = %d, want 7", cw.Limits.MaxOnFailureReentries)
	}
	if cw.Limits.MaxTransientStepRetries != 5 {
		t.Errorf("Limits.MaxTransientStepRetries = %d, want 5", cw.Limits.MaxTransientStepRetries)
	}
}

// TestCompile_LimitsRetryKnobsDefaultZero verifies that a workflow that omits
// the retry knobs compiles to zero values, so the controller's defaults stay
// authoritative.
func TestCompile_LimitsRetryKnobsDefaultZero(t *testing.T) {
	wf := newMinimalWorkflow("test-limits-retry-knobs-default")
	wf.Limits = Limits{MaxStepAttempts: 16}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}
	if cw.Limits.MaxOnFailureReentries != 0 || cw.Limits.MaxTransientStepRetries != 0 {
		t.Fatalf("omitted retry knobs = %d/%d, want 0/0", cw.Limits.MaxOnFailureReentries, cw.Limits.MaxTransientStepRetries)
	}
}
