package delivery

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// TestEffectiveBase pins the single source of truth for the delivery base a
// fresh admission must guard: a valid pr_base input overrides the workflow's
// declared delivery base (the same override delivery honors at publish time,
// resolveStackingInputs), while an absent, empty, or invalid pr_base never
// fails admission and the declared base is used instead. A nil snapshot, a
// nil workflow, or a workflow without an active delivery section all resolve
// to the declared base or "".
func TestEffectiveBase(t *testing.T) {
	declared := newCompiledPRWorkflow(t, "draft") // Delivery.Base "main"
	cases := []struct {
		name     string
		wf       *definition.CompiledWorkflow
		snapshot map[string]string
		want     string
	}{
		{"valid pr_base overrides the declared base", declared, map[string]string{InputPRBase: "release/2.x"}, "release/2.x"},
		{"no pr_base uses the declared base", declared, map[string]string{}, "main"},
		{"absent pr_base key uses the declared base", declared, map[string]string{"task": "x"}, "main"},
		{"empty pr_base uses the declared base", declared, map[string]string{InputPRBase: ""}, "main"},
		{"invalid pr_base uses the declared base", declared, map[string]string{InputPRBase: "-evil"}, "main"},
		{"pr_base with traversal uses the declared base", declared, map[string]string{InputPRBase: "a..b"}, "main"},
		{"nil snapshot uses the declared base", declared, nil, "main"},
		{"nil workflow yields empty", nil, nil, ""},
		{"workflow without delivery yields empty", &definition.CompiledWorkflow{Delivery: nil}, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveBase(tc.wf, tc.snapshot); got != tc.want {
				t.Fatalf("EffectiveBase() = %q, want %q", got, tc.want)
			}
		})
	}
}
