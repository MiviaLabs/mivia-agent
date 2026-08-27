package definition

import (
	"testing"
)

// --- Step max_turns validation tests ---

func TestCompile_StepMaxTurnsValidation(t *testing.T) {
	t.Run("unset defaults to unlimited", func(t *testing.T) {
		wf := newMinimalWorkflow("test-step-maxturns-zero")
		if _, err := Compile(wf); err != nil {
			t.Fatalf("unexpected compile error for unset max_turns: %v", err)
		}
	})
	t.Run("positive max_turns accepted", func(t *testing.T) {
		wf := newMinimalWorkflow("test-step-maxturns-positive")
		wf.Steps[0].MaxTurns = 5
		if _, err := Compile(wf); err != nil {
			t.Fatalf("unexpected compile error for max_turns=5: %v", err)
		}
	})
	wantErr := []struct {
		name, substr string
		maxTurns     int
	}{
		{"negative max_turns", "max_turns", -1},
		{"max_turns exceeds 10000", "max_turns", 20000},
	}
	for _, tc := range wantErr {
		t.Run(tc.name, func(t *testing.T) {
			wf := newMinimalWorkflow("test-step-maxturns-invalid")
			wf.Steps[0].MaxTurns = tc.maxTurns
			assertCompileError(t, wf, "test-step-maxturns-invalid", tc.substr)
		})
	}
}

// TestCompile_StepMaxTurnsPositivePassthrough pins that a compiled workflow
// preserves the step's max_turns value for the controller to apply.
func TestCompile_StepMaxTurnsPositivePassthrough(t *testing.T) {
	wf := newMinimalWorkflow("test-step-maxturns-passthrough")
	wf.Steps[0].MaxTurns = 12
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}
	if got := cw.Steps[0].MaxTurns; got != 12 {
		t.Fatalf("compiled step max_turns = %d, want 12", got)
	}
	if got := cw.Steps[0].MaxTurns; got == 0 {
		t.Fatal("compiled step max_turns = 0, want the configured value")
	}
}

var _ = Step{}
