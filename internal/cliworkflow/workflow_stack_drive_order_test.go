package cliworkflow

// Drive-before-delivery ordering regressions (plan D2/D3/D8, §5a):
//
// A stacking-enabled delivery workflow's plan run settles at delivery_pending:
// its success terminal is delivery-policy active (multi -> chunk_plan_validate
// -> success), so the run parks waiting for delivery. Before this fix, the
// settled-delivery_pending branch in ExecuteWorkflowRun returned FIRST and the
// chunk stack was never driven: the plan PR got published and the per-chunk
// runs, their stacked PRs, the merge waits, and the final integration run
// never happened (deliver-before-drive ordering bug). The same gap existed on
// the session tool surface, whose auto-delivery loop delivered before any
// stack drive. These tests pin the fixed ordering on both entry points.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// multiChunkPlanOutput is a decompose step output that satisfies
// schemas/chunk-plan-v1.json and routes stack_mode=multi with two chunks.
const multiChunkPlanOutput = `{"stack_mode":"multi","chunk_plan":{"chunks":[
	{"id":"c1","title":"chunk one","files":["a.go"],"est_diff_lines":20,"tests":true,"depends_on":[]},
	{"id":"c2","title":"chunk two","files":["b.go"],"est_diff_lines":30,"tests":true,"depends_on":["c1"]}
]}}`

// miniStackWorkflowTOML is a minimal stacking-enabled delivery workflow: the
// plan run goes plan -> decompose -> chunk_plan_validate -> success and must
// settle at delivery_pending (delivery policy active), which is exactly the
// shape that used to return from the delivery branch before the stack drive.
const miniStackWorkflowTOML = `version = 1
name = "mini-stack"
description = "Minimal stacking + delivery workflow for the drive-ordering regression."
initial_step = "plan"

[inputs.task]
type = "string"
required = true

[[steps]]
id = "plan"
kind = "agent"
agent = "one"

[[steps]]
id = "implement"
kind = "agent"
agent = "two"

[[transitions]]
from = "plan"
to = "implement"
match = { status = "succeeded" }

[[transitions]]
from = "implement"
to = "success"
match = { status = "succeeded" }

[stacking]
plan_step = "plan"
implement_step = "implement"
merge_policy = "auto"

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"
title_template = "feat: {{ inputs.task }}"
`

// scriptedStackRunner settles every agent step as completed with a fixed
// output: plan succeeds, decompose emits a multi-chunk plan, and the
// chunk-plan gate approves. It stands in for the coordinator so the controller
// can execute the plan run of mini-stack without any agent servers.
type scriptedStackRunner struct{}

func (scriptedStackRunner) RunStep(_ context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	var output json.RawMessage
	switch req.StepID {
	case "plan":
		output = json.RawMessage(`{"summary":"ok"}`)
	case "decompose":
		output = json.RawMessage(multiChunkPlanOutput)
	case "chunk_plan_validate":
		output = json.RawMessage(`{"valid":true,"reasons":[]}`)
	default:
		output = json.RawMessage(`{}`)
	}
	return controller.AgentStepResult{TaskID: req.TaskID, Output: output, EvidenceJSON: []byte(`[]`), Status: "completed"}, nil
}

// recordingStackDrive stubs WorkflowStackDriveToCompletion and records the
// drive request so tests can assert the stack was driven with the plan run's
// chunk list (and that seeding happened before the drive).
type recordingStackDrive struct {
	called  bool
	stackID string
	chunks  []delivery.ChunkPlan
}

func (d *recordingStackDrive) Drive(_ context.Context, _ *PreparedWorkflowRun, _ *workflowledger.Store, stackID string, chunks []delivery.ChunkPlan, _ bool, _ bool, _ string, _ map[string]string, _ bool, _ io.Writer, _ io.Writer) error {
	d.called = true
	d.stackID = stackID
	d.chunks = chunks
	return nil
}

// orderRecorder shares a synchronous call-order log between the drive hook
// and the delivery git runner.
type orderRecorder struct {
	mu    sync.Mutex
	order []string
}

func (r *orderRecorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, s)
}

// orderGitRunner records that delivery reached the git runner, then delegates
// to the real git so a valid fixture delivery still succeeds.
type orderGitRunner struct {
	rec *orderRecorder
}

func (g orderGitRunner) Run(ctx context.Context, gctx delivery.GitContext, args ...string) (string, error) {
	g.rec.add("deliver")
	return delivery.RealGit{}.Run(ctx, gctx, args...)
}

// runMiniStackCLI executes the mini-stack workflow through ExecuteWorkflowRun
// (the drive-ordering test it backs is TestExecuteWorkflowRunDrivesSettledStackBeforeDelivery).
// with a scripted controller and a recorded stack drive, returning the run
// output, the run id, the drive recorder, and the store path.
func runMiniStackCLI(t *testing.T, toml string, allowPublish bool) (string, string, *recordingStackDrive, string) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "mini-stack.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled := compileWorkflowFile(t, filepath.Join(root, ".mivia", "workflows", "mini-stack.toml"))
	rawDefinition, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "mini-stack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := miniStackSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	drive := &recordingStackDrive{}
	originalDrive := WorkflowStackDriveToCompletion
	t.Cleanup(func() { WorkflowStackDriveToCompletion = originalDrive })
	WorkflowStackDriveToCompletion = drive.Drive

	var runID string
	originalBuild := WorkflowRunBuild
	t.Cleanup(func() { WorkflowRunBuild = originalBuild })
	WorkflowRunBuild = func(_ string, _ *config.Resolved, _ *storage.SQLite, repo workflowledger.Repository, _ *definition.CompiledWorkflow, _ string, _ map[string]any, _ map[string]string, _ []byte, id string, _ *workflowledger.Snapshot, _ []byte, _ *workflowledger.RunSnapshot, _ map[string]bool, _ *skills.Registry, _ string, _ ledger.LedgerRepository) (WorkflowControllerBuild, error) {
		runID = id
		synth, err := definition.SynthesizeStacking(compiled)
		if err != nil {
			return WorkflowControllerBuild{}, err
		}
		steps := scriptedMiniStackRuntimes(t, synth)
		ctrl, err := controller.NewLinearController(repo, scriptedStackRunner{}, synth, steps, map[string]any{"task": "x"}, id, rawSnapshot)
		if err != nil {
			return WorkflowControllerBuild{}, err
		}
		return WorkflowControllerBuild{
			Controller: ctrl,
			Dispatcher: workflowTestDispatcher{},
			Admission:  controller.Admission{InputDigest: workflowledger.InputDigest(map[string]string{"task": "x"})},
		}, nil
	}

	var stdout bytes.Buffer
	if err := ExecuteWorkflowRun("mini-stack", root, filepath.Join(root, "config.toml"), []string{"task=x"}, allowPublish, &stdout, io.Discard); err != nil {
		t.Fatalf("ExecuteWorkflowRun() error = %v; stdout = %q", err, stdout.String())
	}
	return stdout.String(), runID, drive, storePath
}

// TestExecuteWorkflowRunDrivesSettledStackBeforeDelivery pins the CLI ordering
// fix AND the default plan-run publication policy: a stacking plan run that
// settles at delivery_pending has its chunk stack driven BEFORE any delivery
// decision, and with delivery.deliver_plan_run unset (false) the plan run's
// own PR is not created - it settles succeeded and the plan and its artifacts
// stay recorded in the ledger.
func TestExecuteWorkflowRunDrivesSettledStackBeforeDelivery(t *testing.T) {
	stdout, runID, drive, storePath := runMiniStackCLI(t, miniStackWorkflowTOML, false)
	if !drive.called || drive.stackID != runID || len(drive.chunks) != 2 {
		t.Fatalf("stack drive = %+v, want stack %q with 2 chunks", drive, runID)
	}
	if !strings.Contains(stdout, "multi-chunk plan (2 chunks)") {
		t.Fatalf("stdout = %q, want the stack drive notice", stdout)
	}
	// The plan run parked at delivery_pending, then the skip branch settled it
	// succeeded WITHOUT publishing: the final run_id line carries the settled
	// status and the skip reason is printed.
	if !strings.Contains(stdout, "status=delivery_pending") {
		t.Fatalf("stdout = %q, want the intermediate status=delivery_pending", stdout)
	}
	if !strings.Contains(stdout, "status=succeeded plan PR not created (delivery.deliver_plan_run=false)") {
		t.Fatalf("stdout = %q, want the skipped-plan-run settle line", stdout)
	}
	if id, status := parseRunLine(stdout); id != runID || status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("parseRunLine(%q) = (%q, %q), want (%q, succeeded)", stdout, id, status, runID)
	}

	// The stack ledger must be seeded with the plan artifact's chunk tasks:
	// seeding happens inside maybeDriveSettledStack before the drive, so the
	// tasks existing proves the drive path ran to completion.
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seeded, err := workflowledger.NewStore(store).ListTasksByScope(delivery.Scope(runID))
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 2 {
		t.Fatalf("seeded chunk tasks = %d, want 2 (c1, c2)", len(seeded))
	}
	for _, task := range seeded {
		if task.ID != "c1" && task.ID != "c2" {
			t.Fatalf("seeded task = %q, want c1/c2", task.ID)
		}
	}
}

// TestExecuteWorkflowRunDeliversPlanRunWhenEnabled pins the opt-in: with
// delivery.deliver_plan_run = true the delivery branch still runs AFTER the
// stack drive (the ordering fix is preserved), so without --allow-publish the
// run prints the publication explanation and stays delivery_pending.
func TestExecuteWorkflowRunDeliversPlanRunWhenEnabled(t *testing.T) {
	enabledTOML := strings.Replace(miniStackWorkflowTOML, "kind = \"pull_request\"", "deliver_plan_run = true\nkind = \"pull_request\"", 1)
	stdout, runID, drive, storePath := runMiniStackCLI(t, enabledTOML, false)
	if !drive.called || drive.stackID != runID || len(drive.chunks) != 2 {
		t.Fatalf("stack drive = %+v, want stack %q with 2 chunks", drive, runID)
	}
	if !strings.Contains(stdout, "multi-chunk plan (2 chunks)") {
		t.Fatalf("stdout = %q, want the stack drive notice", stdout)
	}
	if !strings.Contains(stdout, "requires --allow-publish") {
		t.Fatalf("stdout = %q, want the delivery branch to still run after the drive", stdout)
	}
	// The run stays delivery_pending: publication is enabled but not granted.
	repo := openWorkflowTestStore(t, storePath)
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending when publication is enabled but not granted", run.Status)
	}
}

// TestSessionAutoDeliveryRepairLoopDrivesStackBeforeDelivery pins the session
// ordering fix: the auto-delivery repair loop must invoke the stack drive hook
// before it delivers a delivery_pending run, and a successful drive + delivery
// must settle the run succeeded.
func TestSessionAutoDeliveryRepairLoopDrivesStackBeforeDelivery(t *testing.T) {
	_, repo, p, runID, prRecorder := newSessionAutoDeliveryRepairFixture(t)
	rec := &orderRecorder{}
	prevGit := WorkflowDeliverGit
	t.Cleanup(func() { WorkflowDeliverGit = prevGit })
	WorkflowDeliverGit = orderGitRunner{rec: rec}

	SessionAutoDeliveryRepairLoopFunc(context.Background(), repo, p.root, p.res, p.store, runID,
		func(ctx context.Context) (workflowledger.RunSnapshot, error) {
			return repo.GetRun(ctx, runID)
		},
		func(context.Context) (bool, error) {
			rec.add("drive")
			return false, nil // the fixture workflow is not a stacking plan run
		}, false)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after the drive + delivery", run.Status)
	}
	got := strings.Join(rec.order, ",")
	if len(rec.order) < 2 || rec.order[0] != "drive" {
		t.Fatalf("call order = %q, want the stack drive to run before any delivery git call", got)
	}
	for i, step := range rec.order[1:] {
		if step != "deliver" {
			t.Fatalf("call order = %q: entry %d = %q, want deliver after the drive", got, i+1, step)
		}
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each", creates, finds)
	}
}

// TestSessionAutoDeliveryRepairLoopSkipsPlanRunWhenDisabled pins the default
// plan-run publication policy on the session surface: when the drive hook
// drove a multi-chunk stack and delivery.deliver_plan_run is false, the loop
// settles the plan run succeeded and NEVER calls delivery for it.
func TestSessionAutoDeliveryRepairLoopSkipsPlanRunWhenDisabled(t *testing.T) {
	_, repo, p, runID, prRecorder := newSessionAutoDeliveryRepairFixture(t)

	SessionAutoDeliveryRepairLoopFunc(context.Background(), repo, p.root, p.res, p.store, runID,
		func(ctx context.Context) (workflowledger.RunSnapshot, error) {
			return repo.GetRun(ctx, runID)
		},
		func(context.Context) (bool, error) {
			return true, nil // a multi-chunk stack was driven
		}, false)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (plan run settled without publication)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want none for a skipped plan run", creates, finds)
	}
}

// TestSessionAutoDeliveryRepairLoopPublishesPlanRunWhenEnabled pins the
// opt-in on the session surface: with delivery.deliver_plan_run true, a plan
// run whose stack drove still proceeds to delivery.
func TestSessionAutoDeliveryRepairLoopPublishesPlanRunWhenEnabled(t *testing.T) {
	_, repo, p, runID, prRecorder := newSessionAutoDeliveryRepairFixture(t)
	prevGit := WorkflowDeliverGit
	t.Cleanup(func() { WorkflowDeliverGit = prevGit })
	WorkflowDeliverGit = orderGitRunner{rec: &orderRecorder{}}

	SessionAutoDeliveryRepairLoopFunc(context.Background(), repo, p.root, p.res, p.store, runID,
		func(ctx context.Context) (workflowledger.RunSnapshot, error) {
			return repo.GetRun(ctx, runID)
		},
		func(context.Context) (bool, error) {
			return true, nil // a multi-chunk stack was driven
		}, true)

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after the drive + delivery", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each", creates, finds)
	}
}

// TestMaybeDriveSettledStackDrivesMultiChunkPlan pins the core drive contract:
// a plan run whose decompose step succeeded with a multi-chunk plan gets its
// stack ledger seeded and the stack driven to completion.
func TestMaybeDriveSettledStackDrivesMultiChunkPlan(t *testing.T) {
	root, res, store, repo, _ := newWorkflowBuildFixture(t)
	compiled := compileFeatureDeliveryWorkflow(t)
	ctx := context.Background()
	runID := "wfr-stack-multi"
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, runID, []byte(multiChunkPlanOutput))

	drive := &recordingStackDrive{}
	original := WorkflowStackDriveToCompletion
	t.Cleanup(func() { WorkflowStackDriveToCompletion = original })
	WorkflowStackDriveToCompletion = drive.Drive

	prepared := &PreparedWorkflowRun{Root: root, Res: res, Store: store, Repo: repo, Compiled: compiled, InputSnapshot: map[string]string{"task": "x"}}
	var stdout bytes.Buffer
	drove, err := maybeDriveSettledStack(context.Background(), prepared, runID, false, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("maybeDriveSettledStack() error = %v", err)
	}
	if !drove {
		t.Fatal("maybeDriveSettledStack() drove = false, want true for a multi-chunk plan")
	}
	if !drive.called || drive.stackID != runID || len(drive.chunks) != 2 {
		t.Fatalf("stack drive = %+v, want stack %q with 2 chunks", drive, runID)
	}
	if !strings.Contains(stdout.String(), "multi-chunk plan (2 chunks)") {
		t.Fatalf("stdout = %q, want the stack drive notice", stdout.String())
	}
	seeded, err := workflowledger.NewStore(store).ListTasksByScope(delivery.Scope(runID))
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 2 {
		t.Fatalf("seeded chunk tasks = %d, want 2", len(seeded))
	}
}

// TestMaybeDriveSettledStackNoopForSingleMode pins the no-op contract: a
// single-chunk plan has nothing to stack, so neither the seed nor the drive
// may run.
func TestMaybeDriveSettledStackNoopForSingleMode(t *testing.T) {
	root, res, store, repo, _ := newWorkflowBuildFixture(t)
	compiled := compileFeatureDeliveryWorkflow(t)
	ctx := context.Background()
	runID := "wfr-stack-single"
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, runID, []byte(`{"stack_mode":"single","chunk_plan":{"chunks":[]}}`))

	drive := &recordingStackDrive{}
	original := WorkflowStackDriveToCompletion
	t.Cleanup(func() { WorkflowStackDriveToCompletion = original })
	WorkflowStackDriveToCompletion = drive.Drive

	prepared := &PreparedWorkflowRun{Root: root, Res: res, Store: store, Repo: repo, Compiled: compiled, InputSnapshot: map[string]string{"task": "x"}}
	drove, err := maybeDriveSettledStack(context.Background(), prepared, runID, false, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("maybeDriveSettledStack() error = %v", err)
	}
	if drove {
		t.Fatal("stack drive ran for a single-chunk plan; nothing to stack")
	}
	if drive.called {
		t.Fatal("stack drive ran for a single-chunk plan; nothing to stack")
	}
	if seeded, err := workflowledger.NewStore(store).ListTasksByScope(delivery.Scope(runID)); err != nil || len(seeded) != 0 {
		t.Fatalf("seeded chunk tasks = %v, %v; want zero for a single plan", seeded, err)
	}
}

// continuationDriveRecorder stubs WorkflowStackDriveToCompletion and captures
// every argument the stack driver is invoked with, so tests can verify that a
// re-entered `workflow run` drive sees the full chunk list across all admitted
// decompose waves and the latest wave's hasMore/remainingScope.
type continuationDriveRecorder struct {
	called         bool
	stackID        string
	chunks         []delivery.ChunkPlan
	hasMore        bool
	remainingScope string
	inputs         map[string]string
}

func (d *continuationDriveRecorder) Drive(_ context.Context, _ *PreparedWorkflowRun, _ *workflowledger.Store, stackID string, chunks []delivery.ChunkPlan, hasMore bool, _ bool, remainingScope string, inputs map[string]string, _ bool, _ io.Writer, _ io.Writer) error {
	d.called = true
	d.stackID = stackID
	d.chunks = append([]delivery.ChunkPlan(nil), chunks...)
	d.hasMore = hasMore
	d.remainingScope = remainingScope
	d.inputs = inputs
	return nil
}

// TestMaybeDriveSettledStackReconstructsAdmittedContinuationWave pins F5:
// when a prior process already admitted a decompose continuation wave,
// maybeDriveSettledStack must reconstruct the full chunk list (wave 0 + wave N)
// and drive with the latest wave's hasMore/remainingScope. Driving with only
// wave 0 leaves the wave-N chunks unadmissible and wedges the stack.
func TestMaybeDriveSettledStackReconstructsAdmittedContinuationWave(t *testing.T) {
	root, res, store, repo, _ := newWorkflowBuildFixture(t)
	compiled := compileFeatureDeliveryWorkflow(t)
	ctx := context.Background()
	runID := "wfr-stack-continue"
	planSnap := workflowledger.Snapshot{
		SchemaVersion: workflowledger.SnapshotSchemaVersion,
		Inputs:        map[string]string{"task": "x"},
	}
	rawPlanSnap, err := workflowledger.MarshalSnapshot(planSnap)
	if err != nil {
		t.Fatal(err)
	}
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, rawPlanSnap); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, runID, []byte(wave0DecomposeOutput))
	// Admit and complete wave 1.
	createContinuationRun(t, repo, runID, 1, "wfr-wave1-ok", workflowledger.RunStatusSucceeded, time.Now())
	seedSucceededDecomposeAttempt(t, repo, "wfr-wave1-ok", []byte(wave1DecomposeOutput))

	rec := &continuationDriveRecorder{}
	original := WorkflowStackDriveToCompletion
	t.Cleanup(func() { WorkflowStackDriveToCompletion = original })
	WorkflowStackDriveToCompletion = rec.Drive

	prepared := &PreparedWorkflowRun{Root: root, Res: res, Store: store, Repo: repo, Compiled: compiled}
	drove, err := maybeDriveSettledStack(ctx, prepared, runID, false, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("maybeDriveSettledStack() error = %v", err)
	}
	if !drove {
		t.Fatal("maybeDriveSettledStack() drove = false, want true for a multi-chunk plan with continuation wave")
	}
	if !rec.called || rec.stackID != runID || len(rec.chunks) != 4 {
		t.Fatalf("stack drive = %+v, want stack %q with 4 chunks", rec, runID)
	}
	if !chunkIDsEqual(rec.chunks, "c1", "c2", "c3", "c4") {
		t.Fatalf("drive chunks = %v, want [c1 c2 c3 c4]", chunkIDs(rec.chunks))
	}
	if rec.hasMore {
		t.Fatalf("drive hasMore = true, want false (wave 1 closed the stack)")
	}
	if rec.remainingScope != "" {
		t.Fatalf("drive remainingScope = %q, want empty", rec.remainingScope)
	}
	seeded, err := workflowledger.NewStore(store).ListTasksByScope(delivery.Scope(runID))
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 4 {
		t.Fatalf("seeded chunk tasks = %d, want 4", len(seeded))
	}
}

// compileWorkflowFile parses and compiles a workflow TOML file.
func compileWorkflowFile(t *testing.T, path string) *definition.CompiledWorkflow {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wf, _, err := definition.ParseWorkflowTOML(raw, filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// compileFeatureDeliveryWorkflow compiles the SHIPPED feature-delivery
// workflow (stacking enabled + delivery policy active) from the repo root.
func compileFeatureDeliveryWorkflow(t *testing.T) *definition.CompiledWorkflow {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Dir(filepath.Dir(cwd)) // internal/cli -> repo root
	path := filepath.Join(repoRoot, ".mivia", "workflows", "feature-delivery.toml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("workflow not present: %v", err)
	}
	return compileWorkflowFile(t, path)
}

// miniStackSnapshot builds a valid ledger snapshot for the mini-stack
// workflow: the definition plus the fixture agents one/two.
func miniStackSnapshot(t *testing.T, root string, compiled *definition.CompiledWorkflow, rawDefinition []byte) workflowledger.Snapshot {
	t.Helper()
	skills, err := LoadChatSkillsFunc(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAgentDefinitionsLocal(root, "", skills)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:  workflowledger.SnapshotSchemaVersion,
		DefinitionTOML: rawDefinition, DefinitionDigest: compiled.Digest,
		Inputs: map[string]string{"task": "x"},
		Agents: map[string]workflowledger.AgentSnapshot{},
	}
	for _, name := range []string{"one", "two"} {
		agent, ok := loaded.Registry.Get(name)
		if !ok {
			t.Fatalf("agent %q is missing", name)
		}
		digest, digestErr := agent.DefinitionDigest()
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		snapshot.Agents[name] = workflowledger.AgentSnapshot{
			Digest: digest, ProviderName: "openrouter", Model: "test/model",
		}
	}
	return snapshot
}

// scriptedMiniStackRuntimes wires StepRuntime for every synthesized step: the
// decompose and chunk-plan gate carry their real template content so the
// controller's prompt renderer can render them, and every step names a
// fixture agent with a digest (the synthesized steps reuse the plan agent).
func scriptedMiniStackRuntimes(t *testing.T, synth *definition.CompiledWorkflow) map[string]controller.StepRuntime {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Dir(filepath.Dir(cwd)) // internal/cli -> repo root
	templatesDir := filepath.Join(repoRoot, ".mivia", "workflows", "templates")
	steps := make(map[string]controller.StepRuntime, len(synth.Steps))
	for _, s := range synth.Steps {
		runtime := controller.StepRuntime{Agent: agents.ResolvedAgent{Name: "one"}, Digest: "digest-" + s.ID}
		if s.Template != "" {
			data, readErr := os.ReadFile(filepath.Join(templatesDir, filepath.Base(s.Template)))
			if readErr != nil {
				t.Fatalf("read template %s: %v", s.Template, readErr)
			}
			runtime.Template = string(data)
		}
		if s.ID == "implement" {
			runtime.Agent = agents.ResolvedAgent{Name: "two"}
		}
		steps[s.ID] = runtime
	}
	return steps
}

// seedSucceededDecomposeAttempt records a succeeded decompose step attempt
// with a stored output on the given run, the shape LoadStackPlanOutputFunc reads.
func seedSucceededDecomposeAttempt(t *testing.T, repo workflowledger.Repository, runID string, output []byte) {
	t.Helper()
	ctx := context.Background()
	ref := sdkadapter.Mint(sdkadapter.KindOutput, output)
	if err := repo.StoreContent(ctx, ref, output); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-decompose-1", RunID: runID, StepID: delivery.DecomposeStepID, AttemptNo: 1}
	if err := repo.RecordStepAttemptOutcome(ctx, attempt, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref, OutputDigest: workflowledger.DigestHex(output),
	}); err != nil {
		t.Fatal(err)
	}
}
