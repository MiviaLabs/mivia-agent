package compiler

import (
	"sort"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// compileStackingFixture compiles the shared stacking fixture (default-on
// stacking, plan and implement inferred) and fails the test when the resolved
// config is missing.
func compileStackingFixture(t *testing.T) *CompiledWorkflow {
	t.Helper()
	cw, err := Compile(stackingFixture())
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Stacking == nil {
		t.Fatal("fixture did not resolve stacking, want non-nil config")
	}
	return cw
}

// stepByID returns the step with the given id from a compiled workflow.
func stepByID(t *testing.T, cw *CompiledWorkflow, id string) definition.Step {
	t.Helper()
	for _, s := range cw.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q not found", id)
	return definition.Step{}
}

// transitionMatches reports whether the workflow has a transition with the
// given from/to pair and match status.
func transitionMatches(cw *CompiledWorkflow, from, to, status string) bool {
	for _, tr := range cw.Transitions {
		if tr.From == from && tr.To == to && tr.Match.Status == status {
			return true
		}
	}
	return false
}

// backEdges finds every back edge of the declared-step graph with a
// three-color DFS. Terminals are not part of the graph.
func backEdges(cw *CompiledWorkflow) [][2]string {
	adj := make(map[string][]string)
	for _, tr := range cw.Transitions {
		if cw.StepIDs[tr.To] {
			adj[tr.From] = append(adj[tr.From], tr.To)
		}
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int)
	var edges [][2]string
	var dfs func(string)
	dfs = func(id string) {
		color[id] = gray
		for _, next := range adj[id] {
			switch color[next] {
			case gray:
				edges = append(edges, [2]string{id, next})
			case white:
				dfs(next)
			}
		}
		color[id] = black
	}
	for id := range cw.StepIDs {
		if color[id] == white {
			dfs(id)
		}
	}
	return edges
}

func TestSynthesizeStacking_NilStackingReturnsSamePointer(t *testing.T) {
	cw, err := Compile(newMinimalWorkflow("plain"))
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Stacking != nil {
		t.Fatal("minimal workflow resolved stacking, want nil (not inferable)")
	}
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}
	if synth != cw {
		t.Error("SynthesizeStacking returned a different pointer for nil stacking")
	}

	// A nil compiled workflow has nil stacking and must pass through.
	got, err := SynthesizeStacking(nil)
	if err != nil {
		t.Fatalf("SynthesizeStacking(nil) failed: %v", err)
	}
	if got != nil {
		t.Error("SynthesizeStacking(nil) must return nil")
	}
}

func TestSynthesizeStacking_ReservedInputsAdded(t *testing.T) {
	cw := compileStackingFixture(t)
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}
	want := []string{"stack_mode", "chunk", "pr_base", "stack_part", "chunk_plan"}
	for _, name := range want {
		def, ok := synth.Inputs[name]
		if !ok {
			t.Errorf("missing reserved input %q", name)
			continue
		}
		if def.Type != "string" {
			t.Errorf("reserved input %q type = %q, want string", name, def.Type)
		}
		if def.Required {
			t.Errorf("reserved input %q must not be required (plan-mode runs omit it)", name)
		}
	}
	if len(synth.Inputs) != len(want) {
		t.Errorf("synthesized inputs = %v, want exactly %v", synth.Inputs, want)
	}
}

func TestSynthesizeStacking_DoesNotMutateInput(t *testing.T) {
	cw := compileStackingFixture(t)
	beforeSteps := len(cw.Steps)
	beforeTransitions := len(cw.Transitions)
	beforeDigest := cw.Digest

	if _, err := SynthesizeStacking(cw); err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}

	if len(cw.Steps) != beforeSteps {
		t.Errorf("input Steps mutated: %d -> %d", beforeSteps, len(cw.Steps))
	}
	if len(cw.Transitions) != beforeTransitions {
		t.Errorf("input Transitions mutated: %d -> %d", beforeTransitions, len(cw.Transitions))
	}
	if cw.Digest != beforeDigest {
		t.Errorf("input Digest mutated: %q -> %q", beforeDigest, cw.Digest)
	}
	if _, ok := cw.Inputs["stack_mode"]; ok {
		t.Error("input Inputs map mutated: reserved input injected")
	}
	if cw.StepIDs["decompose"] {
		t.Error("input StepIDs mutated: decompose injected")
	}
	if cw.LoopNames["decompose_repair"] {
		t.Error("input LoopNames mutated: decompose_repair injected")
	}
}

func TestSynthesizeStacking_InjectsSteps(t *testing.T) {
	synth, err := SynthesizeStacking(compileStackingFixture(t))
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}

	decompose := stepByID(t, synth, "decompose")
	if decompose.Kind != "agent" {
		t.Errorf("decompose kind = %q, want agent", decompose.Kind)
	}
	if decompose.Template != "templates/decompose.md" {
		t.Errorf("decompose template = %q, want templates/decompose.md", decompose.Template)
	}
	if decompose.OutputSchema != "schemas/chunk-plan-v1.json" {
		t.Errorf("decompose output_schema = %q, want schemas/chunk-plan-v1.json", decompose.OutputSchema)
	}
	if !hasBinding(decompose.Context, "steps.plan.output", "plan") {
		t.Errorf("decompose context = %v, want steps.plan.output bound as plan", decompose.Context)
	}

	gate := stepByID(t, synth, "chunk_plan_validate")
	if gate.Kind != "agent_gate" {
		t.Errorf("chunk_plan_validate kind = %q, want agent_gate", gate.Kind)
	}
	if gate.Template != "templates/chunk-plan-validate.md" {
		t.Errorf("chunk_plan_validate template = %q, want templates/chunk-plan-validate.md", gate.Template)
	}
	if gate.OutputSchema != "schemas/chunk-plan-review-v1.json" {
		t.Errorf("chunk_plan_validate output_schema = %q, want schemas/chunk-plan-review-v1.json", gate.OutputSchema)
	}
	if !hasBinding(gate.Context, "steps.decompose.output", "chunk_plan") {
		t.Errorf("chunk_plan_validate context = %v, want steps.decompose.output bound as chunk_plan", gate.Context)
	}
	if !synth.StepIDs["decompose"] || !synth.StepIDs["chunk_plan_validate"] {
		t.Error("StepIDs missing synthesized step ids")
	}
}

func TestSynthesizeStacking_DecomposeRouting(t *testing.T) {
	synth, err := SynthesizeStacking(compileStackingFixture(t))
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}

	if !transitionMatches(synth, "plan", "decompose", "succeeded") {
		t.Error("missing plan -> decompose (succeeded)")
	}
	if !transitionMatches(synth, "decompose", "implement", "succeeded") {
		t.Error("missing decompose -> implement (succeeded)")
	}
	if !transitionMatches(synth, "decompose", "success", "succeeded") {
		t.Error("missing decompose -> success (succeeded)")
	}
	if !transitionMatches(synth, "decompose", "chunk_plan_validate", "succeeded") {
		t.Error("missing decompose -> chunk_plan_validate (succeeded)")
	}
	if !transitionMatches(synth, "decompose", "failure", "failed") {
		t.Error("missing decompose -> failure (failed)")
	}
	if !transitionMatches(synth, "chunk_plan_validate", "success", "succeeded") {
		t.Error("missing chunk_plan_validate -> success (succeeded)")
	}
	if !transitionMatches(synth, "chunk_plan_validate", "decompose", "failed") {
		t.Error("missing chunk_plan_validate -> decompose (failed)")
	}

	// The three succeeded decompose transitions differ only by stack_mode.
	byMode := map[string]string{}
	for _, tr := range synth.Transitions {
		if tr.From == "decompose" && tr.Match.Status == "succeeded" {
			byMode[tr.Match.Output["stack_mode"]] = tr.To
		}
	}
	want := map[string]string{
		"single": "implement",
		"no_bug": "success",
		"multi":  "chunk_plan_validate",
	}
	for mode, to := range want {
		if byMode[mode] != to {
			t.Errorf("decompose succeeded output stack_mode=%s routes to %q, want %q", mode, byMode[mode], to)
		}
	}
}

func TestSynthesizeStacking_RewiresPlanStepEdges(t *testing.T) {
	wf := stackingFixture()
	// The declared plan -> implement edge has an explicit succeeded status:
	// synthesis must remove it. The declared plan -> failure edge with an
	// explicit failed status must survive the rewrite.
	wf.Transitions = []definition.Transition{
		{From: "plan", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "plan", To: "failure", Match: definition.MatchCriteria{Status: "failed"}},
		{From: "implement", To: "success"},
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}

	if transitionMatches(synth, "plan", "implement", "succeeded") {
		t.Error("superseded plan -> implement (succeeded) survived synthesis")
	}
	if !transitionMatches(synth, "plan", "failure", "failed") {
		t.Error("explicit plan -> failure (failed) edge was removed")
	}
	if !transitionMatches(synth, "plan", "decompose", "succeeded") {
		t.Error("router plan -> decompose (succeeded) missing")
	}
}

func TestSynthesizeStacking_RepairLoopIsTheOnlyCycle(t *testing.T) {
	synth, err := SynthesizeStacking(compileStackingFixture(t))
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}

	if !synth.LoopNames["decompose_repair"] {
		t.Error("LoopNames missing decompose_repair")
	}
	if len(synth.LoopNames) != 1 {
		t.Errorf("LoopNames = %v, want only decompose_repair", synth.LoopNames)
	}
	var loop *definition.Transition
	for i := range synth.Transitions {
		tr := &synth.Transitions[i]
		if tr.Loop == "decompose_repair" {
			loop = tr
		}
	}
	if loop == nil {
		t.Fatal("no transition carries loop decompose_repair")
	}
	if loop.From != "chunk_plan_validate" || loop.To != "decompose" {
		t.Errorf("loop edge = %s -> %s, want chunk_plan_validate -> decompose", loop.From, loop.To)
	}
	if loop.Match.Status != "failed" {
		t.Errorf("loop edge match status = %q, want failed", loop.Match.Status)
	}
	if loop.MaxIterations != 3 {
		t.Errorf("loop decompose_repair max_iterations = %d, want 3", loop.MaxIterations)
	}

	// The graph has exactly one cycle: decompose -> chunk_plan_validate ->
	// decompose. backEdges iterates StepIDs in map order, so the reported
	// direction of the back edge is nondeterministic; assert the pair.
	edges := backEdges(synth)
	if len(edges) != 1 {
		t.Fatalf("back edges = %v, want exactly one cycle", edges)
	}
	a, b := edges[0][0], edges[0][1]
	if !((a == "chunk_plan_validate" && b == "decompose") || (a == "decompose" && b == "chunk_plan_validate")) {
		t.Errorf("cycle = %v, want the decompose <-> chunk_plan_validate edge", edges[0])
	}
}

// synthWorkflowFile rebuilds a WorkflowFile from a compiled workflow so the
// package's own admission validators can run on the synthesized graph.
func synthWorkflowFile(t *testing.T, cw *CompiledWorkflow) *definition.WorkflowFile {
	t.Helper()
	return &definition.WorkflowFile{
		Version:     cw.Version,
		Name:        cw.Name,
		Description: cw.Description,
		InitialStep: cw.InitialStep,
		Inputs:      cw.Inputs,
		Limits:      cw.Limits,
		Steps:       cw.Steps,
		Transitions: cw.Transitions,
		Delivery:    cw.Delivery,
	}
}

func TestSynthesizeStacking_GraphPassesAllValidators(t *testing.T) {
	synth, err := SynthesizeStacking(compileStackingFixture(t))
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}
	wf := synthWorkflowFile(t, synth)
	stepIDs := make(map[string]bool, len(synth.StepIDs))
	for id := range synth.StepIDs {
		stepIDs[id] = true
	}

	validators := []struct {
		name string
		run  func() error
	}{
		{"validateGraph", func() error { return validateGraph(wf, stepIDs) }},
		{"validateTransitions", func() error { return validateTransitions(wf, stepIDs, false) }},
		{"validateCycles", func() error { return validateCycles(wf) }},
		{"validateContextBindings", func() error { return validateContextBindings(wf, stepIDs, false) }},
		{"validateOnFailure", func() error { return validateOnFailure(wf, stepIDs) }},
		{"validateLimitsAndStacking", func() error { return validateLimitsAndStacking(wf, stepIDs) }},
		{"validateStepMaxTurns", func() error { return validateStepMaxTurns(wf) }},
		{"validatePanels", func() error { return validatePanels(wf) }},
	}
	for _, v := range validators {
		if err := v.run(); err != nil {
			t.Errorf("%s rejected the synthesized graph: %v", v.name, err)
		}
	}

	// Full admission also accepts the synthesized graph.
	if _, err := Compile(wf); err != nil {
		t.Errorf("Compile rejected the synthesized graph: %v", err)
	}
}

func TestSynthesizeStacking_AgentInheritedFromPlanStep(t *testing.T) {
	cw := compileStackingFixture(t)
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}

	planAgent := stepByID(t, cw, "plan").Agent
	if planAgent == "" {
		t.Fatal("fixture plan step has no agent")
	}
	if got := stepByID(t, synth, "decompose").Agent; got != planAgent {
		t.Errorf("decompose agent = %q, want plan step agent %q", got, planAgent)
	}
	if got := stepByID(t, synth, "chunk_plan_validate").Agent; got != planAgent {
		t.Errorf("chunk_plan_validate agent = %q, want plan step agent %q", got, planAgent)
	}
	// The synthesized agent equals an existing step's agent, so the agent
	// reference resolves in any workspace that runs the workflow.
	for _, s := range synth.Steps {
		if s.Agent == planAgent {
			return
		}
	}
	t.Errorf("no step references the inherited agent %q", planAgent)
}

func TestSynthesizeStacking_ExplicitAgentWins(t *testing.T) {
	wf := stackingFixture()
	wf.Stacking = &definition.Stacking{
		PlanStep:      "plan",
		ImplementStep: "implement",
		Agent:         "workflow-engineer",
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}
	if got := stepByID(t, synth, "decompose").Agent; got != "workflow-engineer" {
		t.Errorf("decompose agent = %q, want stacking.agent workflow-engineer", got)
	}
	if got := stepByID(t, synth, "chunk_plan_validate").Agent; got != "workflow-engineer" {
		t.Errorf("chunk_plan_validate agent = %q, want stacking.agent workflow-engineer", got)
	}
}

func TestSynthesizeStacking_DigestUnchanged(t *testing.T) {
	cw := compileStackingFixture(t)
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}
	if synth.Digest != cw.Digest {
		t.Errorf("synthesized digest = %q, want original %q", synth.Digest, cw.Digest)
	}
	// Compile stays deterministic: the same definition compiles to the same
	// digest before and after synthesis runs.
	cw2, err := Compile(stackingFixture())
	if err != nil {
		t.Fatalf("second Compile failed: %v", err)
	}
	if cw2.Digest != cw.Digest {
		t.Errorf("Compile digest unstable: %q != %q", cw2.Digest, cw.Digest)
	}
}

func TestSynthesizeStacking_RejectsReservedStepIDCollision(t *testing.T) {
	wf := stackingFixture()
	wf.Steps = append(wf.Steps, definition.Step{ID: "decompose", Kind: "agent", Agent: "workflow-engineer"})
	wf.Transitions = append(wf.Transitions,
		definition.Transition{From: "implement", To: "decompose", Match: definition.MatchCriteria{Status: "failed"}},
		definition.Transition{From: "decompose", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	)
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if _, err := SynthesizeStacking(cw); err == nil {
		t.Fatal("SynthesizeStacking accepted a workflow that declares the reserved step id decompose")
	} else if !strings.Contains(err.Error(), "engine-reserved") {
		t.Errorf("error %q should mention engine-reserved", err.Error())
	}
}

func TestSynthesizedInputs(t *testing.T) {
	cfg := &definition.StackingConfig{Enabled: true}
	inputs := SynthesizedInputs(cfg)
	if len(inputs) != 5 {
		t.Fatalf("SynthesizedInputs = %v, want 5 reserved inputs", inputs)
	}
	for _, name := range []string{"stack_mode", "chunk", "pr_base", "stack_part", "chunk_plan"} {
		def, ok := inputs[name]
		if !ok {
			t.Errorf("missing reserved input %q", name)
			continue
		}
		if def.Type != "string" {
			t.Errorf("reserved input %q type = %q, want string", name, def.Type)
		}
		if def.Required {
			t.Errorf("reserved input %q must not be required", name)
		}
	}

	// A disabled or missing config adds no reserved inputs.
	if got := SynthesizedInputs(&definition.StackingConfig{Enabled: false}); got != nil {
		t.Errorf("SynthesizedInputs for disabled config = %v, want nil", got)
	}
	if got := SynthesizedInputs(nil); got != nil {
		t.Errorf("SynthesizedInputs(nil) = %v, want nil", got)
	}
}

func TestMergeStackingInputs(t *testing.T) {
	// A stacking-enabled workflow gains the engine-reserved input defs in its
	// input contract (the same set SynthesizeStacking adds to the run graph).
	cw := compileStackingFixture(t)
	if _, ok := cw.Inputs["stack_mode"]; ok {
		t.Fatal("fixture compiled inputs already carry a reserved name")
	}
	MergeStackingInputs(cw)
	for _, name := range []string{"stack_mode", "chunk", "pr_base", "stack_part", "chunk_plan"} {
		def, ok := cw.Inputs[name]
		if !ok {
			t.Errorf("missing reserved input %q after merge", name)
			continue
		}
		if def.Type != "string" {
			t.Errorf("reserved input %q type = %q, want string", name, def.Type)
		}
		if def.Required {
			t.Errorf("reserved input %q must not be required", name)
		}
	}

	// A workflow-declared input with a reserved name is never overwritten.
	cw = compileStackingFixture(t)
	cw.Inputs = map[string]definition.InputDef{"chunk": {Type: "integer", Required: true}}
	MergeStackingInputs(cw)
	if def := cw.Inputs["chunk"]; def.Type != "integer" || !def.Required {
		t.Errorf("declared input %q was overwritten by the merge: %+v", "chunk", def)
	}
	if len(cw.Inputs) != 5 {
		t.Errorf("merged inputs = %d, want the declared chunk plus 4 remaining reserved names", len(cw.Inputs))
	}

	// A non-stacking workflow (nil Stacking) is a no-op.
	cw, err := Compile(newMinimalWorkflow("plain"))
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Stacking != nil {
		t.Fatal("minimal workflow resolved stacking, want nil")
	}
	before := len(cw.Inputs)
	MergeStackingInputs(cw)
	if len(cw.Inputs) != before {
		t.Errorf("non-stacking merge mutated inputs: %d -> %d", before, len(cw.Inputs))
	}

	// A nil compiled workflow is a safe no-op.
	MergeStackingInputs(nil)
}

// TestSynthesizeStacking_MultiStepPlanPhaseAnchorsRouterAtPlanPhaseExit pins
// the regression the feature-delivery smoke run found: a workflow with a
// multi-step plan phase (plan -> plan_review -> plan_tests ->
// test_plan_review -> implement) must synthesize a graph that keeps every
// plan-phase step reachable. The router anchors at the last plan-phase step
// (test_plan_review), not at the plan step, so admission validation passes
// and the implement step's plan-phase bindings stay resolvable in single
// mode.
func TestSynthesizeStacking_MultiStepPlanPhaseAnchorsRouterAtPlanPhaseExit(t *testing.T) {
	wf := stackingFixture()
	wf.Steps = []definition.Step{
		{ID: "plan", Kind: "agent", Agent: "workflow-engineer"},
		{ID: "plan_review", Kind: "agent", Agent: "reviewer"},
		{ID: "plan_tests", Kind: "agent", Agent: "workflow-engineer"},
		{ID: "test_plan_review", Kind: "agent", Agent: "reviewer"},
		{ID: "implement", Kind: "agent", Agent: "workflow-engineer",
			OutputSchema: "schemas/change-summary-v1.json",
			Context: []definition.ContextBinding{
				{From: "steps.plan.output", As: "plan"},
				{From: "steps.plan_tests.output", As: "test_plan"},
			}},
	}
	wf.Transitions = []definition.Transition{
		{From: "plan", To: "plan_review", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "plan_review", To: "plan_tests", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		{From: "plan_review", To: "plan", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "plan_review_repair", MaxIterations: 5},
		{From: "plan_tests", To: "test_plan_review", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "test_plan_review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		{From: "test_plan_review", To: "plan_tests", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "test_plan_review_repair", MaxIterations: 5},
		{From: "implement", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Stacking == nil {
		t.Fatal("fixture did not resolve stacking")
	}
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}

	// The router anchors at the plan-phase exit, not at the plan step.
	if !transitionMatches(synth, "test_plan_review", "decompose", "succeeded") {
		t.Error("router test_plan_review -> decompose (succeeded) missing")
	}
	if transitionMatches(synth, "test_plan_review", "implement", "succeeded") {
		t.Error("superseded test_plan_review -> implement (approved) survived synthesis")
	}
	// Plan-phase edges before the anchor survive, including the repair loop.
	for _, want := range [][3]string{
		{"plan", "plan_review", "succeeded"},
		{"plan_review", "plan_tests", "succeeded"},
		{"plan_tests", "test_plan_review", "succeeded"},
		{"test_plan_review", "plan_tests", "succeeded"},
	} {
		if !transitionMatches(synth, want[0], want[1], want[2]) {
			t.Errorf("plan-phase edge %s -> %s (%s) was removed", want[0], want[1], want[2])
		}
	}
	// Every declared step stays reachable in the synthesized graph.
	if unreachable := unreachableStepIDs(synth); len(unreachable) > 0 {
		t.Errorf("unreachable steps after synthesis: %v", unreachable)
	}
	// Synthesized steps carry the context bindings their templates require.
	decompose := stepByID(t, synth, "decompose")
	if !hasBinding(decompose.Context, "steps.plan.output", "plan") {
		t.Errorf("decompose missing plan binding, got %+v", decompose.Context)
	}
	gate := stepByID(t, synth, "chunk_plan_validate")
	if !hasBinding(gate.Context, "steps.decompose.output", "chunk_plan") {
		t.Errorf("gate missing chunk_plan binding, got %+v", gate.Context)
	}
}

// TestSynthesizeStacking_PlanDirectEdgeWithDistinctAnchor pins the happy-path
// dead-end regression: a multi-step plan phase (plan -> plan_review ->
// implement) where the plan step ALSO carries a direct succeeded edge into
// implement. The router anchors at the last plan-phase step (plan_review);
// synthesis used to drop BOTH the anchor's succeeded edge and the plan step's
// direct succeeded edge, leaving the plan step with no succeeded exit. The
// run then hard-failed ('no matching transition') on its happy path while the
// plan-failed path still worked. Every plan-phase step must keep at least one
// succeeded exit: the plan step's direct exit is rewired through the anchor
// instead of removed.
func TestSynthesizeStacking_PlanDirectEdgeWithDistinctAnchor(t *testing.T) {
	wf := stackingFixture()
	wf.Steps = []definition.Step{
		{ID: "plan", Kind: "agent", Agent: "workflow-engineer"},
		{ID: "plan_review", Kind: "agent", Agent: "reviewer"},
		{ID: "implement", Kind: "agent", Agent: "workflow-engineer",
			OutputSchema: "schemas/change-summary-v1.json",
			Context: []definition.ContextBinding{
				{From: "steps.plan.output", As: "plan"},
			}},
	}
	wf.Transitions = []definition.Transition{
		{From: "plan", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "plan", To: "plan_review", Match: definition.MatchCriteria{Status: "failed"}},
		{From: "plan_review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "implement", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if cw.Stacking == nil {
		t.Fatal("fixture did not resolve stacking")
	}
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}

	// The router anchors at the last plan-phase step (plan_review).
	if !transitionMatches(synth, "plan_review", "decompose", "succeeded") {
		t.Error("router plan_review -> decompose (succeeded) missing")
	}
	if transitionMatches(synth, "plan_review", "implement", "succeeded") {
		t.Error("superseded plan_review -> implement (succeeded) survived synthesis")
	}
	// The plan step keeps a succeeded route: its direct implement exit is
	// rewired through the anchor, not dropped.
	if transitionMatches(synth, "plan", "implement", "succeeded") {
		t.Error("plan -> implement (succeeded) survived synthesis")
	}
	if !transitionMatches(synth, "plan", "plan_review", "succeeded") {
		t.Error("plan step lost its succeeded exit: no plan -> plan_review (succeeded) route")
	}
	// The declared plan-failed path survives unchanged.
	if !transitionMatches(synth, "plan", "plan_review", "failed") {
		t.Error("plan -> plan_review (failed) edge was removed")
	}
	// Every declared step stays reachable in the synthesized graph.
	if unreachable := unreachableStepIDs(synth); len(unreachable) > 0 {
		t.Errorf("unreachable steps after synthesis: %v", unreachable)
	}
}

// TestSynthesizeStacking_PlanDirectEdgeRewireSkipsExistingAnchorEdge pins the
// rewire guard: when the plan step already declares a plain succeeded edge
// into the anchor, the direct plan->implement edge is rewired by reusing that
// edge instead of adding a second identical one (which admission would reject
// as ambiguous). The plan step keeps its succeeded exit and synthesis
// succeeds.
func TestSynthesizeStacking_PlanDirectEdgeRewireSkipsExistingAnchorEdge(t *testing.T) {
	wf := stackingFixture()
	wf.Steps = []definition.Step{
		{ID: "plan", Kind: "agent", Agent: "workflow-engineer"},
		{ID: "plan_review", Kind: "agent", Agent: "reviewer"},
		{ID: "implement", Kind: "agent", Agent: "workflow-engineer",
			OutputSchema: "schemas/change-summary-v1.json",
			Context: []definition.ContextBinding{
				{From: "steps.plan.output", As: "plan"},
			}},
	}
	wf.Transitions = []definition.Transition{
		{From: "plan", To: "implement"}, // empty status; rewired through the anchor
		{From: "plan", To: "plan_review", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "plan_review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "implement", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	synth, err := SynthesizeStacking(cw)
	if err != nil {
		t.Fatalf("SynthesizeStacking failed: %v", err)
	}
	// The plan step reuses its declared succeeded exit into the anchor: no
	// second identical edge is added.
	if !transitionMatches(synth, "plan", "plan_review", "succeeded") {
		t.Error("plan step lost its succeeded exit: no plan -> plan_review (succeeded) route")
	}
	if count := succeededEdgeCount(synth, "plan", "plan_review"); count != 1 {
		t.Errorf("plan -> plan_review (succeeded) edges = %d, want exactly 1", count)
	}
	// The direct implement exit is superseded by the rewire.
	if transitionMatches(synth, "plan", "implement", "") || transitionMatches(synth, "plan", "implement", "succeeded") {
		t.Error("plan -> implement edge survived synthesis")
	}
	if !transitionMatches(synth, "plan_review", "decompose", "succeeded") {
		t.Error("router plan_review -> decompose (succeeded) missing")
	}
}

// succeededEdgeCount counts plain transitions from from to to with a
// succeeded match.
func succeededEdgeCount(cw *CompiledWorkflow, from, to string) int {
	n := 0
	for _, tr := range cw.Transitions {
		if tr.From == from && tr.To == to && tr.Match.Status == "succeeded" {
			n++
		}
	}
	return n
}

// unreachableStepIDs returns the declared step ids not reachable from the
// initial step over the workflow's transitions.
func unreachableStepIDs(cw *CompiledWorkflow) []string {
	seen := map[string]bool{cw.InitialStep: true}
	queue := []string{cw.InitialStep}
	adj := map[string][]string{}
	for _, tr := range cw.Transitions {
		adj[tr.From] = append(adj[tr.From], tr.To)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	var out []string
	for id := range cw.StepIDs {
		if !seen[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// hasBinding reports whether bindings contains a binding with the given from
// source and as target.
func hasBinding(bindings []definition.ContextBinding, from, as string) bool {
	for _, b := range bindings {
		if b.From == from && b.As == as {
			return true
		}
	}
	return false
}
