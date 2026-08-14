package compiler

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// stackingFixture returns a minimal but fully valid workflow with a plan step
// ("plan"), an implement step that binds the plan and emits the change-summary
// schema, and a linear plan -> implement -> success graph.
func stackingFixture() *definition.WorkflowFile {
	return &definition.WorkflowFile{
		Version:     1,
		Name:        "stacked-fixture",
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "workflow-engineer"},
			{ID: "implement", Kind: "agent", Agent: "workflow-engineer",
				OutputSchema: "schemas/change-summary-v1.json",
				Context: []definition.ContextBinding{
					{From: "steps.plan.output", As: "plan"},
				}},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "implement"},
			{From: "implement", To: "success"},
		},
	}
}

func boolPtr(b bool) *bool { return &b }

func TestCompile_StackingExplicitValid(t *testing.T) {
	wf := stackingFixture()
	wf.Stacking = &definition.Stacking{
		Enabled:             boolPtr(true),
		PlanStep:            "plan",
		ImplementStep:       "implement",
		MaxChunks:           6,
		SoftLines:           150,
		HardLines:           300,
		MaxFiles:            3,
		MergePolicy:         "auto",
		MaxTotalChunks:      50,
		MaxWaveChunks:       8,
		MaxConcurrentChunks: 2,
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Stacking == nil {
		t.Fatal("CompiledWorkflow.Stacking is nil, want resolved config")
	}
	if cw.Stacking.PlanStep != "plan" || cw.Stacking.ImplementStep != "implement" {
		t.Errorf("resolved steps wrong: %+v", cw.Stacking)
	}
	if cw.Stacking.MaxChunks != 6 || cw.Stacking.SoftLines != 150 || cw.Stacking.HardLines != 300 || cw.Stacking.MaxFiles != 3 {
		t.Errorf("resolved thresholds wrong: %+v", cw.Stacking)
	}
	if cw.Stacking.MergePolicy != "auto" {
		t.Errorf("MergePolicy = %q, want auto", cw.Stacking.MergePolicy)
	}
	if cw.Stacking.MaxTotalChunks != 50 || cw.Stacking.MaxWaveChunks != 8 || cw.Stacking.MaxConcurrentChunks != 2 {
		t.Errorf("resolved wave/concurrency knobs wrong: %+v", cw.Stacking)
	}
}

func TestCompile_StackingUnknownSteps(t *testing.T) {
	tests := []struct {
		name string
		wf   *definition.WorkflowFile
		want string
	}{
		{
			name: "unknown plan_step",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{PlanStep: "triage", ImplementStep: "implement"}
				return wf
			}(),
			want: `plan_step "triage" is not a declared step`,
		},
		{
			name: "unknown implement_step",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{PlanStep: "plan", ImplementStep: "code"}
				return wf
			}(),
			want: `implement_step "code" is not a declared step`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.wf)
			if err == nil {
				t.Fatal("expected compile error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestCompile_StackingBadConfig(t *testing.T) {
	tests := []struct {
		name string
		wf   *definition.WorkflowFile
		want string
	}{
		{
			name: "max_chunks too large",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{MaxChunks: 1000}
				return wf
			}(),
			want: "max_chunks must be in range [0, 100]",
		},
		{
			name: "negative max_files",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{MaxFiles: -1}
				return wf
			}(),
			want: "max_files must be in range [0, 1000]",
		},
		{
			name: "soft exceeds hard",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{SoftLines: 500, HardLines: 400}
				return wf
			}(),
			want: "soft_lines 500 exceeds hard_lines 400",
		},
		{
			name: "invalid merge policy",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{MergePolicy: "merge-queue"}
				return wf
			}(),
			want: `merge_policy "merge-queue" must be one of approve, auto`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.wf)
			if err == nil {
				t.Fatal("expected compile error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should contain %q", err.Error(), tt.want)
			}
		})
	}
}

// TestCompile_StackingWaveConcurrencyBadConfig covers the max_total_chunks /
// max_wave_chunks / max_concurrent_chunks range and consistency checks,
// split from TestCompile_StackingBadConfig to keep each function under the
// repo's per-function line ceiling (.mivia/policy/go-structure.json).
func TestCompile_StackingWaveConcurrencyBadConfig(t *testing.T) {
	tests := []struct {
		name string
		wf   *definition.WorkflowFile
		want string
	}{
		{
			name: "max_total_chunks too large",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{MaxTotalChunks: 10000}
				return wf
			}(),
			want: "max_total_chunks must be in range [0, 2000]",
		},
		{
			name: "negative max_wave_chunks",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{MaxWaveChunks: -1}
				return wf
			}(),
			want: "max_wave_chunks must be in range [0, 100]",
		},
		{
			name: "max_concurrent_chunks too large",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{MaxConcurrentChunks: 1000}
				return wf
			}(),
			want: "max_concurrent_chunks must be in range [0, 64]",
		},
		{
			name: "max_wave_chunks exceeds max_total_chunks",
			wf: func() *definition.WorkflowFile {
				wf := stackingFixture()
				wf.Stacking = &definition.Stacking{MaxWaveChunks: 50, MaxTotalChunks: 10}
				return wf
			}(),
			want: "max_wave_chunks 50 exceeds max_total_chunks 10",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.wf)
			if err == nil {
				t.Fatal("expected compile error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestCompile_StackingOptOutIgnoresSemantics(t *testing.T) {
	// A deliberate opt-out must compile even with refs and policy that would
	// otherwise fail: the author turned the capability off.
	wf := stackingFixture()
	wf.Stacking = &definition.Stacking{
		Enabled:       boolPtr(false),
		PlanStep:      "does-not-exist",
		ImplementStep: "also-missing",
		MergePolicy:   "bogus",
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed for opted-out workflow: %v", err)
	}
	if cw.Stacking != nil {
		t.Errorf("CompiledWorkflow.Stacking = %+v, want nil for opted-out workflow", cw.Stacking)
	}
}

func TestCompile_StackingDefaultOnInference(t *testing.T) {
	// No [stacking] section: stacking is on by default and steps are inferred
	// from the graph (implement via change-summary schema, plan via the "plan"
	// binding).
	wf := stackingFixture()
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Stacking == nil {
		t.Fatal("CompiledWorkflow.Stacking is nil, want inferred config (default on)")
	}
	if cw.Stacking.Enabled != true {
		t.Errorf("Stacking.Enabled = %v, want true", cw.Stacking.Enabled)
	}
	if cw.Stacking.PlanStep != "plan" || cw.Stacking.ImplementStep != "implement" {
		t.Errorf("inferred steps wrong: plan=%q implement=%q", cw.Stacking.PlanStep, cw.Stacking.ImplementStep)
	}
	if cw.Stacking.MaxChunks != definition.DefaultStackingMaxChunks {
		t.Errorf("MaxChunks = %d, want global default %d", cw.Stacking.MaxChunks, definition.DefaultStackingMaxChunks)
	}
	if cw.Stacking.MaxTotalChunks != definition.DefaultStackingMaxTotalChunks {
		t.Errorf("MaxTotalChunks = %d, want global default %d", cw.Stacking.MaxTotalChunks, definition.DefaultStackingMaxTotalChunks)
	}
	if cw.Stacking.MaxWaveChunks != definition.DefaultStackingMaxWaveChunks {
		t.Errorf("MaxWaveChunks = %d, want global default %d", cw.Stacking.MaxWaveChunks, definition.DefaultStackingMaxWaveChunks)
	}
	if cw.Stacking.MaxConcurrentChunks != definition.DefaultStackingMaxConcurrentChunks {
		t.Errorf("MaxConcurrentChunks = %d, want global default %d", cw.Stacking.MaxConcurrentChunks, definition.DefaultStackingMaxConcurrentChunks)
	}
}

func TestCompile_StackingNonInferableStaysSinglePR(t *testing.T) {
	// A workflow with no plan/implement shape must compile unchanged (default
	// on is best-effort; nothing breaks) and carry no stacking config.
	wf := &definition.WorkflowFile{
		Version:     1,
		Name:        "plain",
		InitialStep: "one",
		Steps: []definition.Step{
			{ID: "one", Kind: "agent", Agent: "workflow-engineer"},
			{ID: "two", Kind: "agent", Agent: "workflow-engineer"},
		},
		Transitions: []definition.Transition{
			{From: "one", To: "two"},
			{From: "two", To: "success"},
		},
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed for plain workflow: %v", err)
	}
	if cw.Stacking != nil {
		t.Errorf("CompiledWorkflow.Stacking = %+v, want nil (not inferable)", cw.Stacking)
	}
	if len(cw.StepIDs) != 2 {
		t.Errorf("StepIDs = %v, want exactly the two declared steps", cw.StepIDs)
	}
}

func TestInferImplementStep(t *testing.T) {
	wf := stackingFixture()
	if got := InferImplementStep(wf); got != "implement" {
		t.Errorf("InferImplementStep() = %q, want implement (schema match)", got)
	}

	idFallback := &definition.WorkflowFile{Steps: []definition.Step{
		{ID: "step_one", Kind: "agent"},
		{ID: "implement", Kind: "agent"},
	}}
	if got := InferImplementStep(idFallback); got != "implement" {
		t.Errorf("InferImplementStep() = %q, want implement (id fallback)", got)
	}

	none := &definition.WorkflowFile{Steps: []definition.Step{
		{ID: "a", Kind: "agent"},
		{ID: "b", Kind: "agent"},
	}}
	if got := InferImplementStep(none); got != "" {
		t.Errorf("InferImplementStep() = %q, want empty", got)
	}
}

func TestInferPlanStep(t *testing.T) {
	wf := stackingFixture()
	if got := InferPlanStep(wf, "implement"); got != "plan" {
		t.Errorf("InferPlanStep() = %q, want plan (binding match)", got)
	}
	if got := InferPlanStep(wf, ""); got != "" {
		t.Errorf("InferPlanStep() with empty implement = %q, want empty", got)
	}
	// A binding from a step that does not exist cannot name a plan step.
	if got := InferPlanStep(wf, "missing"); got != "" {
		t.Errorf("InferPlanStep() with unknown implement = %q, want empty", got)
	}
}

func TestResolveStackingSteps(t *testing.T) {
	// Explicit values win over inference.
	wf := stackingFixture()
	wf.Stacking = &definition.Stacking{PlanStep: "plan", ImplementStep: "implement"}
	plan, implement := ResolveStackingSteps(wf)
	if plan != "plan" || implement != "implement" {
		t.Errorf("ResolveStackingSteps() = (%q, %q), want (plan, implement)", plan, implement)
	}

	// Inference when the section is absent.
	wf2 := stackingFixture()
	plan, implement = ResolveStackingSteps(wf2)
	if plan != "plan" || implement != "implement" {
		t.Errorf("ResolveStackingSteps() inferred = (%q, %q), want (plan, implement)", plan, implement)
	}

	// No inference possible.
	wf3 := &definition.WorkflowFile{Steps: []definition.Step{{ID: "a", Kind: "agent"}}}
	plan, implement = ResolveStackingSteps(wf3)
	if plan != "" || implement != "" {
		t.Errorf("ResolveStackingSteps() = (%q, %q), want empty", plan, implement)
	}
}
