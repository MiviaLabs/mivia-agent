package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// stackingFixture compiles a stacking workflow whose config resolves, mirroring
// the compiler's s2 synthesis contract (plan + implement + plan binding).
func stackingFixture(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	return stackingFixtureWith(t, nil)
}

func stackingFixtureWith(t *testing.T, implementContext []definition.ContextBinding) *definition.CompiledWorkflow {
	t.Helper()
	enabled := true
	wf := &definition.WorkflowFile{
		Version: 1, Name: "stacked", InitialStep: "plan",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 12},
		Stacking: &definition.Stacking{
			Enabled:       &enabled,
			PlanStep:      "plan",
			ImplementStep: "implement",
			MaxChunks:     3,
			SoftLines:     20,
			HardLines:     40,
			MaxFiles:      2,
		},
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "eng", OnFailure: "failure"},
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure", Context: implementContext},
			{ID: "verify", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "implement", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Stacking == nil {
		t.Fatal("stacking config did not resolve")
	}
	return compiled
}

func stackingRuntimes() map[string]StepRuntime {
	return map[string]StepRuntime{
		"plan":      {Agent: agents.ResolvedAgent{Name: "eng"}, Digest: "sha256:eng"},
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}, Digest: "sha256:dev"},
		"verify":    {Agent: agents.ResolvedAgent{Name: "rev"}, Digest: "sha256:rev"},
		// Engine-synthesized steps run on the plan step's agent (the fixture
		// sets no stacking.agent) and MUST carry a routing digest like any
		// declared step: admission refuses a synthesized step whose runtime is
		// missing or digest-less, because such a step could never dispatch.
		"decompose":           {Agent: agents.ResolvedAgent{Name: "eng"}, Digest: "sha256:eng"},
		"chunk_plan_validate": {Agent: agents.ResolvedAgent{Name: "eng"}, Digest: "sha256:eng"},
	}
}

func chunkInputs() map[string]any {
	return map[string]any{
		"task":       "build",
		"stack_mode": "chunk",
		"chunk":      "c1",
		"pr_base":    "main",
		"stack_part": "1/3",
	}
}

func newStackingController(t *testing.T, runner AgentStepRunner, wf *definition.CompiledWorkflow, inputs map[string]any) (*LinearController, error) {
	t.Helper()
	repo := workflowledger.NewMemoryRepository()
	return NewLinearController(repo, runner, wf, stackingRuntimes(), inputs, "wfr-stacking", []byte("snap"))
}

func TestStackingAdmissionReservedInputs(t *testing.T) {
	wf := stackingFixture(t)
	tests := []struct {
		name    string
		inputs  map[string]any
		wantErr string
	}{
		{name: "valid chunk mode", inputs: chunkInputs()},
		{name: "plan mode without stack_mode", inputs: map[string]any{"task": "build"}},
		{name: "plan mode", inputs: map[string]any{"task": "build", "stack_mode": "plan"}},
		{name: "single mode", inputs: map[string]any{"task": "build", "stack_mode": "single"}},
		{
			name:    "unknown stack_mode",
			inputs:  map[string]any{"task": "build", "stack_mode": "bogus"},
			wantErr: "stack_mode",
		},
		{
			name:    "chunk mode missing chunk",
			inputs:  map[string]any{"task": "build", "stack_mode": "chunk", "pr_base": "main", "stack_part": "1/3"},
			wantErr: "chunk",
		},
		{
			name:    "chunk mode missing pr_base",
			inputs:  map[string]any{"task": "build", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/3"},
			wantErr: "pr_base",
		},
		{
			name:    "chunk mode missing stack_part",
			inputs:  map[string]any{"task": "build", "stack_mode": "chunk", "chunk": "c1", "pr_base": "main"},
			wantErr: "stack_part",
		},
		{
			name:    "single mode forbids chunk_plan",
			inputs:  map[string]any{"task": "build", "stack_mode": "single", "chunk_plan": `{"x":1}`},
			wantErr: "chunk_plan",
		},
		{
			name:    "plan mode forbids chunk_plan",
			inputs:  map[string]any{"task": "build", "stack_mode": "plan", "chunk_plan": `{"x":1}`},
			wantErr: "chunk_plan",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &scriptedRunner{}
			_, err := newStackingController(t, runner, wf, tt.inputs)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("admission rejected valid run: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("admission accepted invalid run; want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestStackingAdmissionNonStackingWorkflowUnchanged(t *testing.T) {
	wf := linearWorkflow(t)
	runner := &scriptedRunner{}
	inputs := map[string]any{"task": "build", "stack_mode": "chunk", "chunk": "c1", "pr_base": "main", "stack_part": "1/3"}
	repo := workflowledger.NewMemoryRepository()
	steps := map[string]StepRuntime{
		"first":  {Agent: agents.ResolvedAgent{Name: "dev"}},
		"second": {Agent: agents.ResolvedAgent{Name: "rev"}},
	}
	ctrl, err := NewLinearController(repo, runner, wf, steps, inputs, "wfr-plain", []byte("snap"))
	if err != nil {
		t.Fatalf("non-stacking workflow must not be affected by reserved inputs: %v", err)
	}
	if ctrl.Workflow != wf {
		t.Fatal("non-stacking workflow must be used as compiled, not copied or synthesized")
	}
	if got := ctrl.runStartStepID(); got != "first" {
		t.Fatalf("non-stacking run starts at %q; want %q", got, "first")
	}
}

// TestStackingAdmissionRefusesDigestlessSynthesizedSteps pins the admission
// invariant behind the "agent task routing snapshot mismatch (resume not
// allowed)" failures: a synthesized step (decompose, chunk_plan_validate)
// admitted with a missing or digest-less runtime could never dispatch - the
// agent handler refuses null routing snapshots. Admission must refuse the run
// instead of letting it die at its first synthesized step.
func TestStackingAdmissionRefusesDigestlessSynthesizedSteps(t *testing.T) {
	wf := stackingFixture(t)
	runner := &scriptedRunner{}
	inputs := map[string]any{"task": "build"}

	t.Run("missing synthesized runtime", func(t *testing.T) {
		steps := stackingRuntimes()
		delete(steps, synthesizedDecomposeStepID)
		repo := workflowledger.NewMemoryRepository()
		_, err := NewLinearController(repo, runner, wf, steps, inputs, "wfr-stacking", []byte("snap"))
		if err == nil || !strings.Contains(err.Error(), "no agent runtime") {
			t.Fatalf("admission error = %v; want missing-runtime refusal", err)
		}
	})

	t.Run("empty routing digest", func(t *testing.T) {
		steps := stackingRuntimes()
		steps[synthesizedDecomposeStepID] = StepRuntime{Agent: agents.ResolvedAgent{Name: "eng"}}
		repo := workflowledger.NewMemoryRepository()
		_, err := NewLinearController(repo, runner, wf, steps, inputs, "wfr-stacking", []byte("snap"))
		if err == nil || !strings.Contains(err.Error(), "routing digest") {
			t.Fatalf("admission error = %v; want routing-digest refusal", err)
		}
	})

	t.Run("complete runtimes admit and survive", func(t *testing.T) {
		ctrl, err := newStackingController(t, runner, wf, inputs)
		if err != nil {
			t.Fatalf("admission rejected a run with complete synthesized runtimes: %v", err)
		}
		rt := ctrl.Steps[synthesizedDecomposeStepID]
		if rt.Digest == "" || rt.Agent.Name != "eng" {
			t.Fatalf("admitted decompose runtime = %+v; want the digest-carrying eng runtime", rt)
		}
	})
}

func TestStackingChunkRunStartsAtImplementStep(t *testing.T) {
	wf := stackingFixture(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := newStackingController(t, runner, wf, chunkInputs())
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo := ctrl.Repo
	run, err := repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ActiveStepID != "implement" {
		t.Fatalf("chunk run active step = %q; want implement", run.ActiveStepID)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) == 0 || runner.calls[0].StepID != "implement" {
		t.Fatalf("first executed step = %v; want implement", runner.calls)
	}
}

func TestStackingChunkRunInjectsReservedContextInputs(t *testing.T) {
	wf := stackingFixture(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := newStackingController(t, runner, wf, chunkInputs())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) == 0 {
		t.Fatal("no step ran")
	}
	in := runner.calls[0].Inputs
	for key, want := range map[string]any{"chunk": "c1", "pr_base": "main", "stack_part": "1/3"} {
		if in[key] != want {
			t.Fatalf("implement inputs[%q] = %v; want %v", key, in[key], want)
		}
	}
}

func TestStackingChunkModePreImplementOptionalBindingResolves(t *testing.T) {
	binding := definition.ContextBinding{From: "steps.plan.output", As: "plan", Optional: true}
	wf := stackingFixtureWith(t, []definition.ContextBinding{binding})
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := newStackingController(t, runner, wf, chunkInputs())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) == 0 {
		t.Fatal("no step ran")
	}
	if ev, ok := runner.calls[0].Evidence["plan"]; !ok || ev != "" {
		t.Fatalf("optional plan binding resolved to %v; want empty string", runner.calls[0].Evidence["plan"])
	}
}

func TestStackingChunkModePreImplementEnvelopeOnlyBindingResolves(t *testing.T) {
	binding := definition.ContextBinding{From: "steps.plan.output", As: "plan", EnvelopeOnly: true}
	wf := stackingFixtureWith(t, []definition.ContextBinding{binding})
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := newStackingController(t, runner, wf, chunkInputs())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) == 0 {
		t.Fatal("no step ran")
	}
	if ev, ok := runner.calls[0].Evidence["plan"]; !ok || ev != "" {
		t.Fatalf("envelope-only plan binding resolved to %v; want empty string", runner.calls[0].Evidence["plan"])
	}
}

// TestStackingChunkModeMandatoryPreImplementBindingResolves pins the
// chunk-mode grace for MANDATORY pre-implement bindings: the run starts at the
// implement step, the plan phase ran in the parent run, and the chunk's own
// description (inputs.chunk) carries the context - so a mandatory steps.plan
// binding must ADMIT and resolve absent ("") at runtime instead of failing
// with "missing prior output". This is what lets the shipped feature-delivery
// workflow (whose implement and repair steps bind plan outputs mandatorily)
// run as a stack of chunk runs without per-workflow TOML annotations.
func TestStackingChunkModeMandatoryPreImplementBindingResolves(t *testing.T) {
	binding := definition.ContextBinding{From: "steps.plan.output", As: "plan"}
	wf := stackingFixtureWith(t, []definition.ContextBinding{binding})
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := newStackingController(t, runner, wf, chunkInputs())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) == 0 {
		t.Fatal("no step ran")
	}
	if ev, ok := runner.calls[0].Evidence["plan"]; !ok || ev != "" {
		t.Fatalf("mandatory plan binding resolved to %v; want empty string (chunk-mode grace)", runner.calls[0].Evidence["plan"])
	}
}

func TestStackingResumeReconstructsSynthesizedGraph(t *testing.T) {
	wf := stackingFixture(t)
	runnerA := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrlA, err := newStackingController(t, runnerA, wf, chunkInputs())
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrlA.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := ctrlA.Workflow.StepIDs[synthesizedDecomposeStepID]; !ok {
		t.Fatal("fresh chunk controller lacks the synthesized decompose step")
	}
	// A resumed controller rebuilds the synthesized graph from the original
	// compiled workflow and the snapshot inputs, so CompileForResume needs no
	// changes and resume continues at the implement step.
	runnerB := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#2": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#2":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrlB, err := NewLinearController(workflowledger.NewMemoryRepository(), runnerB, wf, stackingRuntimes(), chunkInputs(), ctrlA.RunID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ctrlB.Workflow.StepIDs[synthesizedDecomposeStepID]; !ok {
		t.Fatal("resumed controller lacks the synthesized decompose step")
	}
	if got := ctrlB.runStartStepID(); got != "implement" {
		t.Fatalf("resumed chunk run starts at %q; want implement", got)
	}
}

func stackingConfigFixture() *definition.StackingConfig {
	return &definition.StackingConfig{
		Enabled:       true,
		PlanStep:      "plan",
		ImplementStep: "implement",
		MaxChunks:     3,
		SoftLines:     20,
		HardLines:     40,
		MaxFiles:      2,
		MergePolicy:   "approve",
	}
}

func chunkPlanJSON(chunks ...string) string {
	return `{"stack_mode":"multi","chunk_plan":{"chunks":[` + strings.Join(chunks, ",") + `]}}`
}

func chunkJSON(id, files string, lines int, tests bool, depends []string) string {
	dep := "[]"
	if len(depends) > 0 {
		quoted := make([]string, 0, len(depends))
		for _, d := range depends {
			quoted = append(quoted, `"`+d+`"`)
		}
		dep = "[" + strings.Join(quoted, ",") + "]"
	}
	return `{"id":"` + id + `","title":"t","files":` + files + `,"est_diff_lines":` + intString(lines) + `,"tests":` + itoaBool(tests) + `,"depends_on":` + dep + `}`
}

func intString(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func itoaBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestValidateChunkPlanAcceptsGoodPlan(t *testing.T) {
	cfg := stackingConfigFixture()
	plan := chunkPlanJSON(
		chunkJSON("c1", `["a.go"]`, 10, true, nil),
		chunkJSON("c2", `["b.go"]`, 5, true, []string{"c1"}),
	)
	out, err := ValidateChunkPlan(json.RawMessage(plan), cfg)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if !out.Valid {
		t.Fatalf("good plan rejected: %v", out.Reasons)
	}
}

func TestValidateChunkPlanSingleModeIsValid(t *testing.T) {
	out, err := ValidateChunkPlan(json.RawMessage(`{"stack_mode":"single"}`), stackingConfigFixture())
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if !out.Valid {
		t.Fatalf("single mode rejected: %v", out.Reasons)
	}
}

func TestValidateChunkPlanRejects(t *testing.T) {
	cfg := stackingConfigFixture()
	tests := []struct {
		name       string
		raw        string
		wantReason string
	}{
		{
			name:       "unknown stack_mode",
			raw:        `{"stack_mode":"bogus"}`,
			wantReason: "stack_mode",
		},
		{
			name: "too many chunks",
			raw: chunkPlanJSON(
				chunkJSON("c1", `["a.go"]`, 1, true, nil),
				chunkJSON("c2", `["b.go"]`, 1, true, nil),
				chunkJSON("c3", `["c.go"]`, 1, true, nil),
				chunkJSON("c4", `["d.go"]`, 1, true, nil),
			),
			wantReason: "max_chunks",
		},
		{
			name:       "over hard lines",
			raw:        chunkPlanJSON(chunkJSON("c1", `["a.go"]`, 41, true, nil)),
			wantReason: "hard_lines",
		},
		{
			name:       "too many files",
			raw:        chunkPlanJSON(chunkJSON("c1", `["a.go","b.go","c.go"]`, 5, true, nil)),
			wantReason: "max_files",
		},
		{
			name:       "missing tests",
			raw:        chunkPlanJSON(chunkJSON("c1", `["a.go"]`, 5, false, nil)),
			wantReason: "tests",
		},
		{
			name: "overlapping file sets",
			raw: chunkPlanJSON(
				chunkJSON("c1", `["a.go"]`, 5, true, nil),
				chunkJSON("c2", `["a.go","b.go"]`, 5, true, nil),
			),
			wantReason: "overlap",
		},
		{
			name: "depends_on cycle",
			raw: chunkPlanJSON(
				chunkJSON("c1", `["a.go"]`, 5, true, []string{"c2"}),
				chunkJSON("c2", `["b.go"]`, 5, true, []string{"c1"}),
			),
			wantReason: "cycle",
		},
		{
			name:       "unknown depends_on target",
			raw:        chunkPlanJSON(chunkJSON("c1", `["a.go"]`, 5, true, []string{"ghost"})),
			wantReason: "depends_on",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertChunkPlanRejected(t, cfg, tt.raw, tt.wantReason)
		})
	}
}

// assertChunkPlanRejected asserts one chunk plan decodes, is invalid, and
// carries a reason mentioning wantReason.
func assertChunkPlanRejected(t *testing.T, cfg *definition.StackingConfig, raw, wantReason string) {
	t.Helper()
	out, err := ValidateChunkPlan(json.RawMessage(raw), cfg)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if out.Valid {
		t.Fatal("invalid plan accepted")
	}
	for _, r := range out.Reasons {
		if strings.Contains(r, wantReason) {
			return
		}
	}
	t.Fatalf("reasons %v do not mention %q", out.Reasons, wantReason)
}

func TestValidateChunkPlanRejectsMalformedJSON(t *testing.T) {
	_, err := ValidateChunkPlan(json.RawMessage(`{"stack_mode":`), stackingConfigFixture())
	if err == nil {
		t.Fatal("malformed chunk plan must be an error")
	}
}

// decomposeInvalidPlan is a multi plan with three files in one chunk, over the
// fixture's max_files=2, so the deterministic validator rejects it.
const decomposeInvalidPlan = `{"stack_mode":"multi","chunk_plan":{"chunks":[` +
	`{"id":"c1","title":"t","files":["a.go","b.go","c.go"],"est_diff_lines":10,"tests":true,"depends_on":[]}]}}`

const decomposeValidPlan = `{"stack_mode":"multi","chunk_plan":{"chunks":[` +
	`{"id":"c1","title":"t","files":["a.go"],"est_diff_lines":10,"tests":true,"depends_on":[]},` +
	`{"id":"c2","title":"t","files":["b.go"],"est_diff_lines":5,"tests":true,"depends_on":["c1"]}]}}`

func TestStackingDecomposeInvalidPlanRoutesToRepairLoop(t *testing.T) {
	wf := stackingFixture(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"plan#1":                json.RawMessage(`{"summary":"s"}`),
		"decompose#1":           json.RawMessage(decomposeInvalidPlan),
		"decompose#2":           json.RawMessage(decomposeValidPlan),
		"chunk_plan_validate#1": json.RawMessage(`{"valid":true,"reasons":[]}`),
	}}
	ctrl, err := newStackingController(t, runner, wf, map[string]any{"task": "build"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.stepCalls["decompose"] != 2 {
		t.Fatalf("decompose ran %d times; want 2 after one repair loop", runner.stepCalls["decompose"])
	}
	counters, err := ctrl.Repo.GetLoopCounters(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, lc := range counters {
		if lc.LoopName == "decompose_repair" && lc.Iterations != 1 {
			t.Fatalf("decompose_repair iterations = %d; want 1", lc.Iterations)
		}
	}
}

func TestStackingDecomposeRepairLoopExhausts(t *testing.T) {
	wf := stackingFixture(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"plan#1":      json.RawMessage(`{"summary":"s"}`),
		"decompose#*": json.RawMessage(decomposeInvalidPlan),
	}}
	ctrl, err := newStackingController(t, runner, wf, map[string]any{"task": "build"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil {
		t.Fatal("run must fail when the decompose repair loop is exhausted")
	}
	if !strings.Contains(err.Error(), "decompose_repair") {
		t.Fatalf("error %q must mention the decompose_repair loop", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("status = %s; want failed", got.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.stepCalls["decompose"] != 4 {
		t.Fatalf("decompose ran %d times; want 4 before loop exhaustion", runner.stepCalls["decompose"])
	}
}

// TestStackingDecomposeContinueRunStartsAtDecompose pins §12.1's
// incremental-decompose entry point: a run admitted with
// stack_mode=decompose_continue starts directly at the decompose step (no
// plan phase runs in this run), and the decompose step's context binding for
// the plan step's output resolves optional-absent (preImplementStep grace)
// while its remaining_scope binding carries the caller-provided text.
func TestStackingDecomposeContinueRunStartsAtDecompose(t *testing.T) {
	// stack_mode=multi with a valid one-chunk plan (rather than no_bug) so the
	// run reaches success through chunk_plan_validate without also exercising
	// chunkPlanRepairRoute's no_bug/planDeclaresActionableSteps gate: that gate
	// fails closed as "actionable" when a run has no plan-step output at all
	// (exactly true for a decompose-continuation run, which never ran a plan
	// step), rerouting no_bug through the repair loop - correct behavior, but
	// unrelated to what this test pins (the start-step and binding wiring).
	wf := stackingFixture(t)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"decompose#1":           json.RawMessage(`{"stack_mode":"multi","chunk_plan":{"chunks":[{"id":"c1","title":"t","files":["a.go"],"est_diff_lines":10,"tests":true,"depends_on":[]}]}}`),
		"chunk_plan_validate#1": json.RawMessage(`{"valid":true,"reasons":[]}`),
	}}
	inputs := map[string]any{"task": "build", "stack_mode": "decompose_continue", "remaining_scope": "chunks c3, c4 remain"}
	ctrl, err := newStackingController(t, runner, wf, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := ctrl.Repo.GetRun(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ActiveStepID != "decompose" {
		t.Fatalf("decompose-continuation run active step = %q; want decompose", run.ActiveStepID)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) == 0 || runner.calls[0].StepID != "decompose" {
		t.Fatalf("first executed step = %v; want decompose", runner.calls)
	}
	if ev, ok := runner.calls[0].Evidence["plan"]; !ok || ev != "" {
		t.Fatalf("plan binding on a continuation run resolved to %v; want empty string (optional-absent)", runner.calls[0].Evidence["plan"])
	}
	if got := runner.calls[0].Inputs["remaining_scope"]; got != "chunks c3, c4 remain" {
		t.Fatalf("remaining_scope input = %v; want %q", got, "chunks c3, c4 remain")
	}
}

// TestStackingDecomposeContinueRequiresRemainingScope pins admission
// validation: stack_mode=decompose_continue without a non-empty
// remaining_scope is refused before any step runs.
func TestStackingDecomposeContinueRequiresRemainingScope(t *testing.T) {
	wf := stackingFixture(t)
	tests := []struct {
		name   string
		inputs map[string]any
	}{
		{"missing", map[string]any{"task": "build", "stack_mode": "decompose_continue"}},
		{"empty", map[string]any{"task": "build", "stack_mode": "decompose_continue", "remaining_scope": ""}},
		{"blank", map[string]any{"task": "build", "stack_mode": "decompose_continue", "remaining_scope": "   "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{}}
			_, err := newStackingController(t, runner, wf, tt.inputs)
			if err == nil {
				t.Fatal("expected admission error for missing/empty remaining_scope")
			}
			if !strings.Contains(err.Error(), "remaining_scope") {
				t.Fatalf("error %q must mention remaining_scope", err)
			}
		})
	}
}

func TestStackingChunkRunSucceedsWithoutChunkPlanInput(t *testing.T) {
	// The chunk_plan reserved input is for the plan-mode decompose gate only;
	// a chunk run must never require it, and a chunk_plan passed by a caller
	// is not treated as evidence for downstream steps.
	wf := stackingFixture(t)
	inputs := chunkInputs()
	inputs["chunk_plan"] = decomposeValidPlan
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := newStackingController(t, runner, wf, inputs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
}

// TestStackingSiblingFilesValidation pins that sibling_files follows the
// chunk_plan contract: optional in chunk mode, forbidden where chunk_plan is
// forbidden.
func TestStackingSiblingFilesValidation(t *testing.T) {
	wf := stackingFixture(t)
	tests := []struct {
		name    string
		inputs  map[string]any
		wantErr string
	}{
		{
			name: "chunk mode accepts sibling_files",
			inputs: map[string]any{"task": "build", "stack_mode": "chunk", "chunk": "c1",
				"pr_base": "main", "stack_part": "1/3", "sibling_files": `["internal/b/b.go"]`},
		},
		{
			name:    "single mode forbids sibling_files",
			inputs:  map[string]any{"task": "build", "stack_mode": "single", "sibling_files": `["internal/b/b.go"]`},
			wantErr: "sibling_files",
		},
		{
			name:    "plan mode forbids sibling_files",
			inputs:  map[string]any{"task": "build", "stack_mode": "plan", "sibling_files": `["internal/b/b.go"]`},
			wantErr: "sibling_files",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newStackingController(t, &scriptedRunner{}, wf, tt.inputs)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("admission rejected valid run: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to name %q", err, tt.wantErr)
			}
		})
	}
}
