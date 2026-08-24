package controller

import (
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestDefaultPanelLimitsMatchHistoricalConstants(t *testing.T) {
	// MaxOutputPerCall/MaxToolCalls are the only fields PanelLimits carries;
	// MaxTurns stays a per-step workflow knob (definition.Step.MaxTurns,
	// default 0 = unlimited) applied at build time in
	// buildPanelAttempt/buildPanelSynthesisWork, and MaxPromptTokens/
	// MaxOutputTokens stay 0 (unlimited cumulative) unconditionally - see
	// PanelLimits' doc comment in panel_attempt.go for why both bogus-bound
	// classes were removed rather than made configurable.
	got := DefaultPanelLimits()
	want := PanelLimits{
		MemberMaxOutputPerCall:    8192,
		MemberMaxToolCalls:        64,
		SynthesisMaxOutputPerCall: 8192,
		SynthesisMaxToolCalls:     16,
		MemberDeadlineDefault:     24 * time.Hour,
	}
	if got != want {
		t.Fatalf("DefaultPanelLimits() = %+v, want %+v", got, want)
	}
}

// TestNewLinearControllerAppliesDefaultPanelLimits proves a controller
// built without SetPanelLimits still runs under the compiled defaults -
// today's behavior for every host that does not set [workflows.panels].
func TestNewLinearControllerAppliesDefaultPanelLimits(t *testing.T) {
	ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), &linearRunner{}, linearWorkflow(t), nil, map[string]any{"task": "build"}, "wfr-panel-limits-default", []byte("snapshot"))
	if err != nil {
		t.Fatalf("NewLinearController: %v", err)
	}
	if got, want := ctrl.PanelLimits, DefaultPanelLimits(); got != want {
		t.Fatalf("PanelLimits = %+v, want the compiled default %+v", got, want)
	}
}

// TestSetPanelLimitsOverridesControllerField proves a host override
// actually lands on the field buildPanelAttempt/buildPanelSynthesisWork
// read (c.PanelLimits), not just that SetPanelLimits returns nil.
func TestSetPanelLimitsOverridesControllerField(t *testing.T) {
	ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), &linearRunner{}, linearWorkflow(t), nil, map[string]any{"task": "build"}, "wfr-panel-limits-override", []byte("snapshot"))
	if err != nil {
		t.Fatalf("NewLinearController: %v", err)
	}
	override := PanelLimits{
		MemberMaxOutputPerCall:    111,
		MemberMaxToolCalls:        7,
		SynthesisMaxOutputPerCall: 222,
		SynthesisMaxToolCalls:     3,
		MemberDeadlineDefault:     time.Hour,
	}
	if err := ctrl.SetPanelLimits(override); err != nil {
		t.Fatalf("SetPanelLimits: %v", err)
	}
	if got := ctrl.PanelLimits; got != override {
		t.Fatalf("PanelLimits after SetPanelLimits = %+v, want %+v", got, override)
	}
}
