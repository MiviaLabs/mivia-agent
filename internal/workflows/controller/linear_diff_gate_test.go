package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// stackingGateFixture compiles a stacking workflow that declares a diff-size
// repair step (delivery.on_diff_size_failure = "repair_size"), so the
// controller's post-implement diff-size gate has a declared non-terminal
// target to reroute an oversized implement to. Delivery stays inactive
// (kind ""), so the run settles at its success terminal instead of
// delivery_pending; only the gate's OnDiffSizeFailure knob is exercised.
func stackingGateFixture(t *testing.T, repairContext []definition.ContextBinding) *definition.CompiledWorkflow {
	t.Helper()
	enabled := true
	wf := &definition.WorkflowFile{
		Version: 1, Name: "stacked-gate", InitialStep: "plan",
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
		Delivery: &definition.Delivery{OnDiffSizeFailure: "repair_size"},
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "eng", OnFailure: "failure"},
			{ID: "implement", Kind: "agent", Agent: "dev", OnFailure: "failure"},
			{ID: "verify", Kind: "agent_gate", Agent: "rev", OnFailure: "failure"},
			{ID: "repair_size", Kind: "agent", Agent: "dev", OnFailure: "failure", Context: repairContext},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "implement", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "implement", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "repair_size", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "repair_size", To: "verify", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// gateGitRepo initializes a real git repository with one committed base file,
// so the gate's measurement (RealGit, git add -A + numstat vs base) runs
// against a genuine worktree like delivery-time enforcement does.
func gateGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	git("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-q", "-m", "init")
	return dir
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func writeLines(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gateRuntimes extends the stacking fixture runtimes with the diff-size
// repair step the gate may reroute to.
func gateRuntimes() map[string]StepRuntime {
	rt := stackingRuntimes()
	rt["repair_size"] = StepRuntime{Agent: agents.ResolvedAgent{Name: "dev"}, Digest: "sha256:dev"}
	return rt
}

// gateController wires a stacking controller with the run's pinned git
// context, exactly like the CLI and the local engine do for production runs.
func gateController(t *testing.T, runner AgentStepRunner, wf *definition.CompiledWorkflow, inputs map[string]any, dir string) (*LinearController, error) {
	t.Helper()
	ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), runner, wf, gateRuntimes(), inputs, "wfr-stacking", []byte("snap"))
	if err != nil {
		return nil, err
	}
	if dir != "" {
		if err := ctrl.SetAdmission(Admission{BaseRef: "main", BaseCommit: gitHead(t, dir), WorktreeName: "workflow-gate", InputDigest: "d"}); err != nil {
			return nil, err
		}
		if err := ctrl.SetGitContext(delivery.GitContext{Dir: dir, GitDir: filepath.Join(dir, ".git")}); err != nil {
			return nil, err
		}
	}
	return ctrl, nil
}

// assertStepRoute asserts the LATEST attempt of stepID settled with the given
// durable route.
func assertStepRoute(t *testing.T, ctrl *LinearController, stepID, want string) {
	t.Helper()
	attempts, err := ctrl.Repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var latest workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == stepID && a.AttemptNo > latest.AttemptNo {
			latest = a
		}
	}
	if latest.ToStepID != want {
		t.Fatalf("step %q route = %q; want %q", stepID, latest.ToStepID, want)
	}
}

func stepCall(t *testing.T, runner *scriptedRunner, stepID string) *AgentStepRequest {
	t.Helper()
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var found *AgentStepRequest
	for i := range runner.calls {
		if runner.calls[i].StepID == stepID {
			found = &runner.calls[i]
		}
	}
	return found
}

// TestChunkDiffSizeGateReroutesOversizedDiff pins the fail-fast gate: an
// implement step whose ACTUAL worktree diff exceeds the stacking hard limit is
// rerouted to delivery.on_diff_size_failure BEFORE the panel and preflight
// pipeline run, instead of after a full pipeline pass and a delivery
// rejection. The attempt stays Succeeded, like the chunk-plan repair loop's
// reroute.
func TestChunkDiffSizeGateReroutesOversizedDiff(t *testing.T) {
	wf := stackingGateFixture(t, nil)
	dir := gateGitRepo(t)
	writeLines(t, filepath.Join(dir, "big.go"), 41) // 41 added lines > hard limit 40
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1":   json.RawMessage(`{"files_changed":["big.go"]}`),
		"repair_size#1": json.RawMessage(`{"files_changed":["big.go"]}`),
		"verify#1":      json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := gateController(t, runner, wf, chunkInputs(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	assertStepRoute(t, ctrl, "implement", "repair_size")
	if c := stepCall(t, runner, "repair_size"); c == nil {
		t.Fatal("repair_size step did not run after the gate reroute")
	}
}

// TestChunkDiffSizeGateLeavesSmallDiff pins the pass-through: a diff within
// the hard limit keeps the normal implement -> verify route.
func TestChunkDiffSizeGateLeavesSmallDiff(t *testing.T) {
	wf := stackingGateFixture(t, nil)
	dir := gateGitRepo(t)
	writeLines(t, filepath.Join(dir, "small.go"), 5)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["small.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := gateController(t, runner, wf, chunkInputs(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	assertStepRoute(t, ctrl, "implement", "verify")
	if c := stepCall(t, runner, "repair_size"); c != nil {
		t.Fatal("repair_size must not run when the diff fits the hard limit")
	}
}

// TestChunkDiffSizeGateOffWithoutRepairTarget pins the opt-in contract: a
// stacking workflow that declares no delivery.on_diff_size_failure keeps the
// gate off even for an oversized diff, and delivery-time enforcement (which
// falls back to delivery.on_failure) remains the single authority.
func TestChunkDiffSizeGateOffWithoutRepairTarget(t *testing.T) {
	wf := stackingFixture(t) // no delivery.on_diff_size_failure
	dir := gateGitRepo(t)
	writeLines(t, filepath.Join(dir, "big.go"), 41)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["big.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := gateController(t, runner, wf, chunkInputs(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	assertStepRoute(t, ctrl, "implement", "verify")
}

// TestChunkDiffSizeGateSkipsUnresolvableRepairContext pins the gate's
// conservative contract: when the repair step MANDATORY-binds a post-implement
// step's output (steps.verify.output) that has not run at implement-success
// time, and the chunk-mode grace does not cover it (a SINGLE-mode run), the
// gate must leave the route untouched instead of rerouting into a step whose
// context would hard-fail with "missing prior output". Enforcement is not
// lost: the delivery-time gate re-enters the SAME step after the pipeline ran,
// when the binding resolves.
func TestChunkDiffSizeGateSkipsUnresolvableRepairContext(t *testing.T) {
	wf := stackingGateFixture(t, []definition.ContextBinding{{From: "steps.verify.output", As: "failed_evidence"}})
	dir := gateGitRepo(t)
	writeLines(t, filepath.Join(dir, "big.go"), 41)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"plan#1":      json.RawMessage(`{"summary":"s"}`),
		"decompose#1": json.RawMessage(`{"stack_mode":"single"}`),
		"implement#1": json.RawMessage(`{"files_changed":["big.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := gateController(t, runner, wf, map[string]any{"task": "build", "stack_mode": "single"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	assertStepRoute(t, ctrl, "implement", "verify")
	if c := stepCall(t, runner, "repair_size"); c != nil {
		t.Fatal("gate must stay off when the repair step's mandatory context cannot resolve")
	}
}

// TestChunkDiffSizeGateSkipsMandatoryEnvelopeOnlyBinding pins that
// envelope_only does NOT make a mandatory binding resolvable at
// implement-success time (resolveBindingOutput hard-fails it with "missing
// prior output"), so the gate stays off exactly as for a plain mandatory
// binding in single mode.
func TestChunkDiffSizeGateSkipsMandatoryEnvelopeOnlyBinding(t *testing.T) {
	wf := stackingGateFixture(t, []definition.ContextBinding{{From: "steps.verify.output", As: "failed_evidence", EnvelopeOnly: true}})
	dir := gateGitRepo(t)
	writeLines(t, filepath.Join(dir, "big.go"), 41)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"plan#1":      json.RawMessage(`{"summary":"s"}`),
		"decompose#1": json.RawMessage(`{"stack_mode":"single"}`),
		"implement#1": json.RawMessage(`{"files_changed":["big.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := gateController(t, runner, wf, map[string]any{"task": "build", "stack_mode": "single"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	assertStepRoute(t, ctrl, "implement", "verify")
	if c := stepCall(t, runner, "repair_size"); c != nil {
		t.Fatal("gate must stay off when the repair step's mandatory envelope_only binding cannot resolve")
	}
}

// TestChunkDiffSizeGateFiresInSingleMode pins that the gate is not chunk-only:
// a SINGLE-mode run whose whole diff exceeds the hard limit is rerouted to the
// diff-size repair step before the pipeline continues, because the repair
// step's mandatory plan binding IS resolvable (the plan phase ran in this
// run). Fail-fast applies to single runs exactly like chunk runs.
func TestChunkDiffSizeGateFiresInSingleMode(t *testing.T) {
	wf := stackingGateFixture(t, []definition.ContextBinding{{From: "steps.plan.output", As: "plan"}})
	dir := gateGitRepo(t)
	writeLines(t, filepath.Join(dir, "big.go"), 41)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"plan#1":        json.RawMessage(`{"summary":"s"}`),
		"decompose#1":   json.RawMessage(`{"stack_mode":"single"}`),
		"implement#1":   json.RawMessage(`{"files_changed":["big.go"]}`),
		"repair_size#1": json.RawMessage(`{"files_changed":["big.go"]}`),
		"verify#1":      json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := gateController(t, runner, wf, map[string]any{"task": "build", "stack_mode": "single"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	assertStepRoute(t, ctrl, "implement", "repair_size")
	if c := stepCall(t, runner, "repair_size"); c == nil {
		t.Fatal("repair_size step did not run after the gate reroute in single mode")
	}
}

// TestChunkDiffSizeGateFiresWithGraceCoveredRepairContext pins the mirror
// case: the SAME repair step in a CHUNK-mode run is covered by the chunk-mode
// grace (the bound step never runs in a chunk run), so the gate fires and the
// repair step runs with the mandatory binding resolved absent ("") instead of
// failing - and the run still converges to success.
func TestChunkDiffSizeGateFiresWithGraceCoveredRepairContext(t *testing.T) {
	wf := stackingGateFixture(t, []definition.ContextBinding{{From: "steps.verify.output", As: "failed_evidence"}})
	dir := gateGitRepo(t)
	writeLines(t, filepath.Join(dir, "big.go"), 41)
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1":   json.RawMessage(`{"files_changed":["big.go"]}`),
		"repair_size#1": json.RawMessage(`{"files_changed":["big.go"]}`),
		"verify#1":      json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := gateController(t, runner, wf, chunkInputs(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	assertStepRoute(t, ctrl, "implement", "repair_size")
	repair := stepCall(t, runner, "repair_size")
	if repair == nil {
		t.Fatal("repair_size step did not run")
	}
	if ev, ok := repair.Evidence["failed_evidence"]; !ok || ev != "" {
		t.Fatalf("failed_evidence resolved to %v; want empty string via chunk-mode grace", repair.Evidence["failed_evidence"])
	}
}

// TestChunkDiffSizeGateMeasurementFailureLeavesRoute pins the fail-open
// contract: a measurement failure (here a worktree that is not a git repo)
// leaves the route unchanged, and delivery-time enforcement remains the guard.
func TestChunkDiffSizeGateMeasurementFailureLeavesRoute(t *testing.T) {
	wf := stackingGateFixture(t, nil)
	dir := t.TempDir() // not a git repository
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"files_changed":["a.go"]}`),
		"verify#1":    json.RawMessage(`{"verdict":"approved"}`),
	}}
	ctrl, err := newStackingController(t, runner, wf, chunkInputs())
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetAdmission(Admission{BaseRef: "main", BaseCommit: "deadbeef", WorktreeName: "workflow-gate", InputDigest: "d"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetGitContext(delivery.GitContext{Dir: dir, GitDir: filepath.Join(dir, ".git")}); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v err=%v", got, err)
	}
	assertStepRoute(t, ctrl, "implement", "verify")
}
