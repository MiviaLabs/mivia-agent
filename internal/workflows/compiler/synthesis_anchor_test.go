package compiler

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

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

// TestSynthesizeStacking_PreservesOutputDiscriminatedShortcutEdge pins the
// fix for the confirmed defect: a workflow can legally declare both a plain
// plan -> plan_review edge and an output-discriminated shortcut plan ->
// implement edge (match.output={"skip_review":"true"}), admission-legal via
// validateTransitions' strict-superset exception. When the plan phase has a
// distinct anchor (plan_review), the anchor rewire must supersede only the
// plain generic edge, not the discriminated shortcut: a run whose plan step
// outputs skip_review=true must still route directly to implement.
func TestSynthesizeStacking_PreservesOutputDiscriminatedShortcutEdge(t *testing.T) {
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
		{From: "plan", To: "plan_review", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "plan", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"skip_review": "true"}}},
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

	// The output-discriminated shortcut survives synthesis unchanged.
	found := false
	for _, tr := range synth.Transitions {
		if tr.From == "plan" && tr.To == "implement" && tr.Match.Status == "succeeded" &&
			tr.Match.Output["skip_review"] == "true" {
			found = true
			break
		}
	}
	if !found {
		t.Error("output-discriminated plan -> implement (skip_review=true) shortcut was dropped during synthesis")
	}
	// The plain edge is still correctly rewired through the anchor.
	if !transitionMatches(synth, "plan", "plan_review", "succeeded") {
		t.Error("plan step lost its plain succeeded exit: no plan -> plan_review (succeeded) route")
	}
	if !transitionMatches(synth, "plan_review", "decompose", "succeeded") {
		t.Error("router plan_review -> decompose (succeeded) missing")
	}
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
