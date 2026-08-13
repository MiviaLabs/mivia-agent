package localengine_test

// Engine-level integration tests for the stacking engine (small-PR delivery).
// These drive the REAL engine, controller, and ledger with a scripted agent
// step runner, exercising the full admission + synthesis + routing path that
// unit tests cover in isolation: plan-mode runs through the synthesized
// decompose/chunk_plan_validate graph, chunk-mode runs that start at the
// implement step, the deterministic chunk-plan gate's repair loop, and the
// reserved-input admission rules. Mirrors the scriptedTwoStep harness in
// integration_test.go; helpers mustService/mustTool/waitRun are shared.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// --- fixtures ---

// stackValidMultiPlan is a chunk plan that passes the deterministic
// host-side validator (ValidateChunkPlan): two disjoint, test-carrying
// chunks under the default hard_lines=400 / max_files=5 limits, forming a
// depends_on DAG.
const stackValidMultiPlan = `{"stack_mode":"multi","chunk_plan":{"chunks":[
  {"id":"c1","title":"chunk one","files":["a.go"],"est_diff_lines":60,"tests":true,"depends_on":[]},
  {"id":"c2","title":"chunk two","files":["b.go"],"est_diff_lines":70,"tests":true,"depends_on":["c1"]}
]}}`

// stackInvalidMultiPlan exceeds hard_lines (400) on the first chunk, so the
// controller's chunk-plan gate must route decompose back into the
// decompose_repair loop instead of advancing.
const stackInvalidMultiPlan = `{"stack_mode":"multi","chunk_plan":{"chunks":[
  {"id":"c1","title":"too big","files":["a.go"],"est_diff_lines":500,"tests":true,"depends_on":[]}
]}}`

const stackSinglePlan = `{"stack_mode":"single","chunk_plan":{"chunks":[
  {"id":"c1","title":"only","files":["a.go"],"est_diff_lines":40,"tests":true,"depends_on":[]}
]}}`

const stackNoBugPlan = `{"stack_mode":"no_bug","chunk_plan":{"chunks":[]}}`

const stackChunkPlanReviewValid = `{"valid":true,"reasons":[]}`

// writeStackingWorkspace writes a minimal stacking-enabled workflow: authored
// plan + implement steps, explicit [stacking] plan_step/implement_step, and
// no templates/schemas (the authored steps declare none, and the
// engine-synthesized steps need no files in the scripted-runner harness).
func writeStackingWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(wfRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `version = 1
name = "stack-me"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[stacking]
plan_step = "plan"
implement_step = "implement"

[[steps]]
id = "plan"
kind = "agent"
agent = "planner"

[[steps]]
id = "implement"
kind = "agent"
agent = "implementer"

[[transitions]]
from = "plan"
to = "implement"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "implement"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfRoot, "stack-me.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// scriptedAttemptRunner returns scripted JSON per step per attempt
// ("decompose#1", "decompose#2"), falling back to a per-step script
// ("decompose") and finally {"ok":true}. Attempt numbering follows the
// controller's fresh, numbered step attempts, so a repair loop can be
// scripted deterministically.
type scriptedAttemptRunner struct {
	mu         sync.Mutex
	calls      map[string]int
	byStepCall map[string]json.RawMessage
}

func (r *scriptedAttemptRunner) RunStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[req.StepID]++
	n := r.calls[req.StepID]
	r.mu.Unlock()
	out := json.RawMessage(`{"ok":true}`)
	if v, ok := r.byStepCall[fmt.Sprintf("%s#%d", req.StepID, n)]; ok {
		out = v
	} else if v, ok := r.byStepCall[req.StepID]; ok {
		out = v
	}
	return controller.AgentStepResult{
		CoordinatorRunID: "coord-" + req.StepID + "-" + strconv.Itoa(req.AttemptNo),
		TaskID:           req.TaskID,
		Output:           out,
		EvidenceJSON:     []byte(`[]`),
	}, nil
}

func (r *scriptedAttemptRunner) callsFor(step string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[step]
}

// scriptedStackingEngine builds an engine over a fresh stacking workspace
// with a scripted agent runner.
func scriptedStackingEngine(t *testing.T, byStepCall map[string]json.RawMessage) (*localengine.Engine, *agenttools.Service) {
	t.Helper()
	return scriptedStackingEngineRoot(t, writeStackingWorkspace(t), byStepCall)
}

// scriptedStackingEngineRoot builds an engine over an already-written
// workspace root with a scripted agent runner.
func scriptedStackingEngineRoot(t *testing.T, root string, byStepCall map[string]json.RawMessage) (*localengine.Engine, *agenttools.Service) {
	t.Helper()
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &scriptedAttemptRunner{byStepCall: byStepCall}
		},
	}
	return engine, mustService(t, engine, repo)
}

// scriptedStackingEngineMultiPhase builds an engine over a workspace whose
// stacking workflow has a multi-step plan phase (the feature-delivery shape).
func scriptedStackingEngineMultiPhase(t *testing.T, byStepCall map[string]json.RawMessage) (*localengine.Engine, *agenttools.Service) {
	t.Helper()
	return scriptedStackingEngineRoot(t, writeStackingWorkspaceMultiPhase(t), byStepCall)
}

// startStackingRun admits a run of the "stack-me" workflow with the given
// extra inputs (nil for a plain plan-mode run).
func startStackingRun(t *testing.T, svc *agenttools.Service, extra map[string]string) agenttools.StartResult {
	t.Helper()
	return startStackingRunFor(t, svc, "stack-me", extra)
}

// startStackingRunFor admits a run of the named workflow with the given extra
// inputs (nil for a plain plan-mode run).
func startStackingRunFor(t *testing.T, svc *agenttools.Service, workflow string, extra map[string]string) agenttools.StartResult {
	t.Helper()
	inputs := map[string]any{"task": "build"}
	for k, v := range extra {
		inputs[k] = v
	}
	payload, err := json.Marshal(map[string]any{"workflow": workflow, "inputs": inputs})
	if err != nil {
		t.Fatal(err)
	}
	out, err := mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(context.Background(), payload)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var started agenttools.StartResult
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	if started.RunID == "" || started.Status == "" {
		t.Fatalf("start = %+v", started)
	}
	return started
}

// startStackingRunErr admits a run and expects the admission to fail.
func startStackingRunErr(t *testing.T, svc *agenttools.Service, extra map[string]string) error {
	t.Helper()
	inputs := map[string]any{"task": "build"}
	for k, v := range extra {
		inputs[k] = v
	}
	payload, err := json.Marshal(map[string]any{"workflow": "stack-me", "inputs": inputs})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mustTool(t, svc, agenttools.ToolWorkflowRun).Execute(context.Background(), payload)
	return err
}

func stackStatusView(t *testing.T, svc *agenttools.Service, runID string) agenttools.StatusView {
	t.Helper()
	out, err := mustTool(t, svc, agenttools.ToolWorkflowStatus).Execute(
		context.Background(), json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, runID)))
	if err != nil {
		t.Fatal(err)
	}
	var view agenttools.StatusView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

type wantAttempt struct {
	step string
	to   string
}

// assertAttemptSequence pins the exact execution order and routing of a run.
func assertAttemptSequence(t *testing.T, view agenttools.StatusView, want []wantAttempt) {
	t.Helper()
	if len(view.Attempts) != len(want) {
		t.Fatalf("attempts = %+v; want %d attempts", view.Attempts, len(want))
	}
	for i, w := range want {
		a := view.Attempts[i]
		if a.Step != w.step || a.ToStep != w.to {
			t.Fatalf("attempt %d = %s->%s; want %s->%s", i, a.Step, a.ToStep, w.step, w.to)
		}
	}
}

func assertLoopIterations(t *testing.T, view agenttools.StatusView, name string, want int) {
	t.Helper()
	for _, l := range view.Loops {
		if l.Name == name {
			if l.Iterations != want {
				t.Fatalf("loop %s iterations = %d; want %d", name, l.Iterations, want)
			}
			return
		}
	}
	t.Fatalf("loop %s not found in %+v", name, view.Loops)
}

// --- tests ---

// writeStackingWorkspaceMultiPhase writes a stacking workflow with a
// multi-step plan phase (plan -> plan_review -> plan_tests ->
// test_plan_review -> implement), the shape of the shipped feature-delivery
// workflow. The stacking router must anchor at the plan phase's last step so
// every plan-phase step stays reachable and admission passes.
func writeStackingWorkspaceMultiPhase(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(wfRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `version = 1
name = "stack-multi"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[stacking]
plan_step = "plan"
implement_step = "implement"

[[steps]]
id = "plan"
kind = "agent"
agent = "planner"

[[steps]]
id = "plan_review"
kind = "agent"
agent = "reviewer"

[[steps]]
id = "plan_tests"
kind = "agent"
agent = "planner"

[[steps]]
id = "test_plan_review"
kind = "agent"
agent = "reviewer"

[[steps]]
id = "implement"
kind = "agent"
agent = "implementer"

[[transitions]]
from = "plan"
to = "plan_review"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "plan_review"
to = "plan_tests"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "plan_tests"
to = "test_plan_review"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "test_plan_review"
to = "implement"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "implement"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfRoot, "stack-multi.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestStackingE2EPlanModeMultiStepPlanPhase is the regression for the
// feature-delivery-shaped workflow: the stacking router must anchor at the
// last plan-phase step (test_plan_review), keeping every plan-phase step
// reachable, so admission succeeds and the run walks the full plan phase
// before decompose and the gate.
func TestStackingE2EPlanModeMultiStepPlanPhase(t *testing.T) {
	engine, svc := scriptedStackingEngineMultiPhase(t, map[string]json.RawMessage{
		"plan":                json.RawMessage(`{"summary":"p"}`),
		"plan_review":         json.RawMessage(`{"verdict":"approved"}`),
		"plan_tests":          json.RawMessage(`{"summary":"tp"}`),
		"test_plan_review":    json.RawMessage(`{"verdict":"approved"}`),
		"decompose":           json.RawMessage(stackValidMultiPlan),
		"chunk_plan_validate": json.RawMessage(stackChunkPlanReviewValid),
	})
	started := startStackingRunFor(t, svc, "stack-multi", nil)
	waitRun(t, engine, started.RunID)
	view := stackStatusView(t, svc, started.RunID)
	if view.Status != "succeeded" {
		t.Fatalf("status = %q; want succeeded (%+v)", view.Status, view.Attempts)
	}
	assertAttemptSequence(t, view, []wantAttempt{
		{"plan", "plan_review"},
		{"plan_review", "plan_tests"},
		{"plan_tests", "test_plan_review"},
		{"test_plan_review", "decompose"},
		{"decompose", "chunk_plan_validate"},
		{"chunk_plan_validate", "success"},
	})
}

func TestStackingE2EPlanModeMultiRoutesThroughGate(t *testing.T) {
	engine, svc := scriptedStackingEngine(t, map[string]json.RawMessage{
		"plan":                json.RawMessage(`{"summary":"p"}`),
		"decompose":           json.RawMessage(stackValidMultiPlan),
		"chunk_plan_validate": json.RawMessage(stackChunkPlanReviewValid),
	})
	started := startStackingRun(t, svc, nil)
	waitRun(t, engine, started.RunID)
	view := stackStatusView(t, svc, started.RunID)
	if view.Status != "succeeded" {
		t.Fatalf("status = %q; want succeeded (%+v)", view.Status, view.Attempts)
	}
	assertAttemptSequence(t, view, []wantAttempt{
		{"plan", "decompose"},
		{"decompose", "chunk_plan_validate"},
		{"chunk_plan_validate", "success"},
	})
}

// TestStackingE2EPlanModeSingleRoutesThroughImplement pins the single-mode
// router edge: decompose -> implement (no chunk_plan_validate).
func TestStackingE2EPlanModeSingleRoutesThroughImplement(t *testing.T) {
	engine, svc := scriptedStackingEngine(t, map[string]json.RawMessage{
		"plan":      json.RawMessage(`{"summary":"p"}`),
		"decompose": json.RawMessage(stackSinglePlan),
		"implement": json.RawMessage(`{"summary":"s"}`),
	})
	started := startStackingRun(t, svc, nil)
	waitRun(t, engine, started.RunID)
	view := stackStatusView(t, svc, started.RunID)
	if view.Status != "succeeded" {
		t.Fatalf("status = %q; want succeeded", view.Status)
	}
	assertAttemptSequence(t, view, []wantAttempt{
		{"plan", "decompose"},
		{"decompose", "implement"},
		{"implement", "success"},
	})
}

// TestStackingE2EPlanModeNoBugSkipsImplement pins the no_bug router edge:
// decompose -> success directly, no implement, no gate.
func TestStackingE2EPlanModeNoBugSkipsImplement(t *testing.T) {
	engine, svc := scriptedStackingEngine(t, map[string]json.RawMessage{
		"plan":      json.RawMessage(`{"summary":"p"}`),
		"decompose": json.RawMessage(stackNoBugPlan),
	})
	started := startStackingRun(t, svc, nil)
	waitRun(t, engine, started.RunID)
	view := stackStatusView(t, svc, started.RunID)
	if view.Status != "succeeded" {
		t.Fatalf("status = %q; want succeeded", view.Status)
	}
	assertAttemptSequence(t, view, []wantAttempt{
		{"plan", "decompose"},
		{"decompose", "success"},
	})
}

// TestStackingE2EChunkRunStartsAtImplement pins the chunk-mode admission: a
// run carrying stack_mode=chunk + chunk + pr_base + stack_part starts at the
// implement step (never at plan), and the reserved inputs are accepted by the
// engine's input validation (the regression this test guards: the engine
// rejected them as unknown inputs before the controller synthesis ran).
func TestStackingE2EChunkRunStartsAtImplement(t *testing.T) {
	engine, svc := scriptedStackingEngine(t, map[string]json.RawMessage{
		"implement": json.RawMessage(`{"summary":"s"}`),
	})
	started := startStackingRun(t, svc, map[string]string{
		"stack_mode": "chunk",
		"chunk":      "c1",
		"pr_base":    "master",
		"stack_part": "1/2",
		"chunk_plan": stackValidMultiPlan,
	})
	waitRun(t, engine, started.RunID)
	view := stackStatusView(t, svc, started.RunID)
	if view.Status != "succeeded" {
		t.Fatalf("status = %q; want succeeded (%+v)", view.Status, view.Attempts)
	}
	assertAttemptSequence(t, view, []wantAttempt{
		{"implement", "success"},
	})
}

// TestStackingE2EInvalidDecomposeRepairs pins the deterministic chunk-plan
// gate through the engine: decompose emits an over-limit plan, the controller
// routes it back into the decompose_repair loop (bounded at 3), the second
// decompose emits a valid plan, and the run succeeds.
func TestStackingE2EInvalidDecomposeRepairs(t *testing.T) {
	engine, svc := scriptedStackingEngine(t, map[string]json.RawMessage{
		"plan":                json.RawMessage(`{"summary":"p"}`),
		"decompose#1":         json.RawMessage(stackInvalidMultiPlan),
		"decompose#2":         json.RawMessage(stackValidMultiPlan),
		"chunk_plan_validate": json.RawMessage(stackChunkPlanReviewValid),
	})
	started := startStackingRun(t, svc, nil)
	waitRun(t, engine, started.RunID)
	view := stackStatusView(t, svc, started.RunID)
	if view.Status != "succeeded" {
		t.Fatalf("status = %q; want succeeded (%+v)", view.Status, view.Attempts)
	}
	assertAttemptSequence(t, view, []wantAttempt{
		{"plan", "decompose"},
		{"decompose", "decompose"}, // repair loop re-entry, attempt stays Succeeded
		{"decompose", "chunk_plan_validate"},
		{"chunk_plan_validate", "success"},
	})
	assertLoopIterations(t, view, "decompose_repair", 1)
}

// TestStackingE2EChunkModeMissingReservedInputFailsAdmission pins the
// reserved-input admission rule: stack_mode=chunk without chunk/pr_base/
// stack_part is rejected before the run starts.
func TestStackingE2EChunkModeMissingReservedInputFailsAdmission(t *testing.T) {
	_, svc := scriptedStackingEngine(t, nil)
	err := startStackingRunErr(t, svc, map[string]string{
		"stack_mode": "chunk",
	})
	if err == nil {
		t.Fatal("expected admission to reject stack_mode=chunk without chunk/pr_base/stack_part")
	}
	// The engine accepted the reserved input as part of the contract; the
	// controller admission rejects the incomplete payload.
	for _, want := range []string{"requires reserved input", "chunk"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("admission error %q must mention %q", err, want)
		}
	}
}
