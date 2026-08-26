package cliworkflow

// workflow_coverage_pass3_test.go covers the remaining uncovered statement
// lines reported by the diff-coverage gate across the workflow command,
// approval, gc, resume, snapshot, status, verifiers, and session-engine
// files. Fault-injection repos wrap the real ledger repository so failure
// branches that need a storage fault are reached without a live TUI or a
// full engine runtime.

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// faultRepo wraps a real repository and injects storage faults for coverage
// of failure branches that a healthy ledger never takes.
type faultRepo struct {
	workflowledger.Repository
	failCreateRun    bool
	failListAttempts bool
	// getRunFailFrom fails GetRun from the given 1-based call onward.
	getRunFailFrom int
	// getRunTerminalFrom forces a succeeded status from the given call onward.
	getRunTerminalFrom int
	// getRunFailCanceled fails GetRun when the stored status is canceled.
	getRunFailCanceled bool
	mu                 sync.Mutex
	getRunCalls        int
}

func (f *faultRepo) CreateRun(ctx context.Context, snap workflowledger.RunSnapshot, snapshotJSON []byte) error {
	if f.failCreateRun {
		return errors.New("scripted create-run failure")
	}
	return f.Repository.CreateRun(ctx, snap, snapshotJSON)
}

func (f *faultRepo) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	if f.failListAttempts {
		return nil, errors.New("scripted step-attempt read failure")
	}
	return f.Repository.ListStepAttempts(ctx, runID)
}

func (f *faultRepo) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	f.mu.Lock()
	f.getRunCalls++
	calls := f.getRunCalls
	f.mu.Unlock()
	snap, err := f.Repository.GetRun(ctx, runID)
	if err != nil {
		return snap, err
	}
	if f.getRunFailCanceled && snap.Status == workflowledger.RunStatusCanceled {
		return workflowledger.RunSnapshot{}, errors.New("scripted canceled-run read failure")
	}
	if f.getRunFailFrom > 0 && calls >= f.getRunFailFrom {
		return workflowledger.RunSnapshot{}, errors.New("scripted get-run failure")
	}
	if f.getRunTerminalFrom > 0 && calls >= f.getRunTerminalFrom {
		snap.Status = workflowledger.RunStatusSucceeded
	}
	return snap, nil
}

// TestWaitForSessionEngineIdleImmediateReturn covers the exported idle-wait
// helper on an engine with no active record: it must return at once.
func TestWaitForSessionEngineIdleImmediateReturn(t *testing.T) {
	e := NewSessionWorkflowEngine(t.TempDir(), "")
	WaitForSessionEngineIdle(t, e, "wfr-never-active")
}

// TestResolveWorkflowDialogApprovalFailureBranches covers the open, build,
// and claim failure branches of ResolveWorkflowDialogApproval.
func TestResolveWorkflowDialogApprovalFailureBranches(t *testing.T) {
	if err := ResolveWorkflowDialogApproval("wfr-x", "appr-x", filepath.Join(t.TempDir(), "absent"), "", "actor", false); err == nil {
		t.Fatal("ResolveWorkflowDialogApproval() on a missing workspace succeeded; want the open error")
	}

	root, configPath, _, runID := newGatedApprovalFixture(t)
	if err := ResolveWorkflowDialogApproval("wfr-cov-unknown-run", "appr-x", root, configPath, "actor", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ResolveWorkflowDialogApproval() on an unknown run error = %v, want a not-found error", err)
	}

	repo, closeFn, err := OpenWorkflowReportContext(root, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(context.Background(), runID, "foreign-live-executor"); err != nil {
		t.Fatal(err)
	}
	closeFn()
	err = ResolveWorkflowDialogApproval(runID, "appr-x", root, configPath, "actor", false)
	if err == nil || !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("ResolveWorkflowDialogApproval() under a foreign claim error = %v, want a claim refusal", err)
	}
}

// TestWorkflowRejectDefaultsActor covers the default-actor branch of the
// reject dispatcher: a reject without --actor must fall back to the default
// actor and surface the resolution error for an unknown approval.
func TestWorkflowRejectDefaultsActor(t *testing.T) {
	root, configPath, _, runID := newGatedApprovalFixture(t)
	var stdout, stderr strings.Builder
	err := RunWorkflowCommandReject([]string{runID, "appr-missing"}, root, configPath, &stdout, &stderr)
	if err == nil {
		t.Fatal("RunWorkflowCommandReject() on an unknown approval succeeded; want a resolution error")
	}
}

// TestWorkflowDeliverCompleteStackSkipsPlanPR covers the
// SkipParkedPlanRunPublication branch of executeWorkflowDeliver: a
// delivery_pending plan run of a COMPLETE stack whose workflow keeps
// deliver_plan_run=false settles succeeded with no plan PR.
func TestWorkflowDeliverCompleteStackSkipsPlanPR(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	completeParkedStackDrive(t, storePath, repo, planRunID)

	var stdout, stderr strings.Builder
	if err := executeWorkflowDeliver(context.Background(), planRunID, root, configPath, true, false, &stdout, &stderr); err != nil {
		t.Fatalf("executeWorkflowDeliver() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "plan PR not created") {
		t.Fatalf("stdout = %q, want the skipped-plan-PR notice", stdout.String())
	}
	run, err := repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded", run.Status)
	}
}

// TestWorkflowGCHappyPathAndArgRefusal covers the workflow gc command's
// argument refusal and its full happy path over a real workspace store.
func TestWorkflowGCHappyPathAndArgRefusal(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-gc-unrelated")
	closeFn()
	configPath := filepath.Join(root, "config.toml")

	var stdout, stderr strings.Builder
	if err := RunWorkflowCommandGC([]string{"extra"}, root, configPath, &stdout, &stderr); err == nil {
		t.Fatal("RunWorkflowCommandGC() with an extra argument succeeded; want an argument error")
	}
	stdout.Reset()
	if err := RunWorkflowCommandGC(nil, root, configPath, &stdout, &stderr); err != nil {
		t.Fatalf("RunWorkflowCommandGC() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "removed 0 orphaned content row(s)") {
		t.Fatalf("stdout = %q, want the removed-rows notice", stdout.String())
	}
}

// TestRunWorkflowDispatchGC covers the 'gc' dispatch arm of the workflow
// command router through the public RunWorkflowWithIO entry point.
//
// --config must be passed explicitly: openEventsFixtureWithRun writes its
// config to <root>/config.toml, not the namespaced <root>/.mivia/mivia.toml
// WorkflowConfigPath auto-discovers, so an omitted --config here falls
// through to config.Load's system-wide DefaultConfigCandidates search
// (env var, ./.mivia/mivia.toml relative to the test binary's working
// directory, then ~/.mivia/mivia.toml) instead of staying inside the
// fixture - on a machine whose real ~/.mivia/mivia.toml sets an env_file
// that doesn't happen to exist, that leaks out as an unrelated load
// failure. Every sibling test using this fixture already passes the
// config path explicitly; this one only omitted it because gc's dispatch
// doesn't take a positional workflow name to remind you.
func TestRunWorkflowDispatchGC(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-dispatch-gc-unrelated")
	closeFn()
	var stdout, stderr strings.Builder
	if err := RunWorkflowWithIO([]string{"gc", "--workspace", root, "--config", filepath.Join(root, "config.toml")}, &stdout, &stderr); err != nil {
		t.Fatalf("RunWorkflowWithIO(gc) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "removed 0 orphaned content row(s)") {
		t.Fatalf("stdout = %q, want the removed-rows notice", stdout.String())
	}
}

// TestPrepareWorkflowResumeAdmissionSkillAcceptError covers the
// ApplyAcceptedSkillChanges failure branch of prepareWorkflowResumeAdmission:
// a resume accepting skill changes for a skill the registry no longer
// declares must fail closed.
func TestPrepareWorkflowResumeAdmissionSkillAcceptError(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	wf := &definition.CompiledWorkflow{
		Name:  "cov-skill-accept",
		Steps: []definition.Step{{ID: "one", Kind: "agent", Skill: "absent-skill"}},
	}
	_, _, err := prepareWorkflowResumeAdmission(context.Background(), repo, t.TempDir(), wf, "wfr-cov-skill-accept", true, &workflowledger.Snapshot{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "absent-skill") {
		t.Fatalf("prepareWorkflowResumeAdmission() error = %v, want an error naming the missing skill", err)
	}
}

// TestFinishWorkflowResumeSettledDriveError covers the drive-error branch of
// finishWorkflowResumeSettled: a stacking plan run whose stack drive fails
// must surface the drive error, not settle or publish.
func TestFinishWorkflowResumeSettledDriveError(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedGrantPolicyParkedStackingPlanRun(t, root, storePath, repo)
	seedSucceededDecomposeAttempt(t, repo, planRunID, []byte(multiChunkPlanOutput))

	grantTOML := strings.Replace(miniStackWorkflowTOML, "merge_policy = \"auto\"\n", "", 1)
	wf, _, err := definition.ParseWorkflowTOML([]byte(grantTOML), "mini-stack.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	prevLoader := LoadAllStackChunksForDriveFunc
	LoadAllStackChunksForDriveFunc = func(*PreparedWorkflowRun, string, []byte, map[string]string, io.Writer, io.Writer) ([]delivery.ChunkPlan, bool, bool, string, error) {
		return nil, false, false, "", errors.New("scripted stack load failure")
	}
	t.Cleanup(func() { LoadAllStackChunksForDriveFunc = prevLoader })

	var stdout, stderr strings.Builder
	err = finishWorkflowResumeSettled(context.Background(), root, configPath, res, store, repo, planRunID, "mini-stack", compiled, false, func() {}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "stack drive") {
		t.Fatalf("finishWorkflowResumeSettled() error = %v, want the wrapped drive error", err)
	}
}

// TestWorkflowModuleBaselineResumeBranches covers the resume branches of
// workflowModuleBaseline: an invalid or missing pinned baseline fails closed,
// and a valid pin restores the baseline bytes.
func TestWorkflowModuleBaselineResumeBranches(t *testing.T) {
	root := t.TempDir()
	prior := &workflowledger.Snapshot{Verifiers: map[string]workflowledger.RefSnapshot{}}
	if _, err := workflowModuleBaseline(true, root, prior); err == nil || !strings.Contains(err.Error(), "module baseline is invalid") {
		t.Fatalf("workflowModuleBaseline() without a go.mod pin error = %v, want the invalid-baseline error", err)
	}

	goMod := []byte("module example.com/cov\n")
	prior.Verifiers[workflowGoModBaselineRef] = workflowledger.RefSnapshot{Digest: DigestBytes(goMod), Bytes: goMod}
	if _, err := workflowModuleBaseline(true, root, prior); err == nil || !strings.Contains(err.Error(), "checksum baseline is invalid") {
		t.Fatalf("workflowModuleBaseline() without a go.sum pin error = %v, want the invalid-checksum error", err)
	}

	goSum := []byte("example.com/dep v1.0.0\n")
	prior.Verifiers[workflowGoSumBaselineRef] = workflowledger.RefSnapshot{Digest: DigestBytes(goSum), Bytes: goSum}
	baseline, err := workflowModuleBaseline(true, root, prior)
	if err != nil {
		t.Fatalf("workflowModuleBaseline() with valid pins error = %v", err)
	}
	if string(baseline.GoMod) != string(goMod) || string(baseline.GoSum) != string(goSum) {
		t.Fatalf("restored baseline = %q / %q, want the pinned bytes", baseline.GoMod, baseline.GoSum)
	}
}

// TestWorkflowStatusBaseRefAndDeliveryCommit covers the base-ref line and
// the delivery-record commit line of the status report.
func TestWorkflowStatusBaseRefAndDeliveryCommit(t *testing.T) {
	root, store, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-cov-status-fields")
	defer closeFn()
	_ = store
	baseRunID := "wfr-cov-status-base"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: baseRunID, WorkflowName: "test-wf", Status: workflowledger.RunStatusPending,
		BaseRef: "main", BaseCommit: "beefcafebeefcafebeefcafebeefcafebeefcafe",
	}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: baseRunID, IdempotencyKey: "cov-key", Mode: "draft", BaseRef: "main",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", URL: "https://example.com/pr/1", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if err := RunWorkflowCommandStatus([]string{baseRunID}, root, filepath.Join(root, "config.toml"), &stdout, &stderr); err != nil {
		t.Fatalf("RunWorkflowCommandStatus() error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "base: main") || !strings.Contains(out, "commit 0123456") {
		t.Fatalf("status output = %q, want the base and commit lines", out)
	}
}

// TestWorkflowStatusDeliveryCommitLine covers the delivery-record commit and
// URL lines of the status report for a run without a base ref.
func TestWorkflowStatusDeliveryCommitLine(t *testing.T) {
	root, _, repo, closeFn, ctx, runID := openEventsFixtureWithRun(t, "wfr-cov-status-commit")
	defer closeFn()
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: runID, IdempotencyKey: "cov-key", Mode: "draft", BaseRef: "main",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", URL: "https://example.com/pr/1", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if err := RunWorkflowCommandStatus([]string{runID}, root, filepath.Join(root, "config.toml"), &stdout, &stderr); err != nil {
		t.Fatalf("RunWorkflowCommandStatus() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "commit 0123456") {
		t.Fatalf("status output = %q, want the commit line", stdout.String())
	}
}

// TestBuildWorkflowControllerSynthesisFailure covers the stacking-synthesis
// failure branch of buildWorkflowController: a stacking workflow whose plan
// step has no agent and declares no stacking agent must fail before any
// resource is built.
func TestBuildWorkflowControllerSynthesisFailure(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	wf := &definition.CompiledWorkflow{
		Name:     "cov-synth-fail",
		Stacking: &definition.StackingConfig{PlanStep: "gate", ImplementStep: "impl"},
		StepIDs:  map[string]bool{"gate": true, "impl": true},
		Steps:    []definition.Step{{ID: "gate", Kind: "human_gate"}, {ID: "impl", Kind: "agent", Agent: "one"}},
	}
	_, err := buildWorkflowController(t.TempDir(), &config.Resolved{}, nil, repo, wf, "", nil, nil, nil, "wfr-cov-synth-fail", nil, nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "synthesize stacking run graph") {
		t.Fatalf("buildWorkflowController() error = %v, want the wrapped synthesis error", err)
	}
}

// TestKeyedRunIDInputsMatchReadFailure covers the inputs-match read failure
// branch of keyedRunID: a ledger fault while re-reading the bound run must
// surface the read error and close the admission store.
func TestKeyedRunIDInputsMatchReadFailure(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	key := "cov-inputs-read-failure"
	runID := string(workflowledger.InvocationRunID(key))
	ctx := context.Background()
	if err := prepared.Repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, InvocationKey: key, WorkflowName: "two-step", Status: workflowledger.RunStatusPending,
	}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	realRepo := prepared.Repo
	prepared.Repo = &faultRepo{Repository: realRepo, getRunFailFrom: 2}
	_, _, err := e.keyedRunID(ctx, prepared, workflowledger.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "x"}, InvocationKey: key,
	})
	if err == nil || !strings.Contains(err.Error(), "scripted get-run failure") {
		t.Fatalf("keyedRunID() error = %v, want the scripted read failure", err)
	}
}

// TestBuildAndStartStartNewFailure covers the StartNew failure branch of
// buildAndStart: an admission write the ledger refuses with a non-duplicate
// fault must clean up the built controller and dispatcher before returning.
func TestBuildAndStartStartNewFailure(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	realRepo := prepared.Repo
	prepared.Repo = &faultRepo{Repository: realRepo, failCreateRun: true}
	_, err := e.buildAndStart(context.Background(), prepared, workflowledger.StartRequest{Workflow: "two-step"}, "wfr-cov-startnew-fail")
	if err == nil || !strings.Contains(err.Error(), "scripted create-run failure") {
		t.Fatalf("buildAndStart() error = %v, want the scripted create-run failure", err)
	}
}

// TestBuildAndStartExistingRunReadFailure covers the created=false re-read
// failure branch of buildAndStart: after StartNew observes an existing run,
// a ledger fault on the follow-up read must surface the read error.
func TestBuildAndStartExistingRunReadFailure(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	runID := "wfr-cov-existing-read-fail"
	ctx := context.Background()

	built, err := WorkflowRunBuild(prepared.Root, prepared.Res, prepared.Store, prepared.Repo, prepared.Compiled, prepared.RefBase, prepared.Inputs, prepared.InputSnapshot, prepared.Raw, runID, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("WorkflowRunBuild() error = %v", err)
	}
	if err := built.Controller.SetAdmission(built.Admission); err != nil {
		t.Fatalf("SetAdmission() error = %v", err)
	}
	if _, err := built.Controller.StartNew(ctx); err != nil {
		t.Fatalf("first StartNew() error = %v", err)
	}
	built.Cleanup()
	built.Dispatcher.Close()

	realRepo := prepared.Repo
	prepared.Repo = &faultRepo{Repository: realRepo, getRunFailFrom: 2}
	_, err = e.buildAndStart(ctx, prepared, workflowledger.StartRequest{Workflow: "two-step"}, runID)
	if err == nil || !strings.Contains(err.Error(), "scripted get-run failure") {
		t.Fatalf("buildAndStart() error = %v, want the scripted re-read failure", err)
	}
}

// TestLaunchStartedWorkflowReadFailure covers the post-launch run re-read
// failure branch of LaunchStartedWorkflow.
func TestLaunchStartedWorkflowReadFailure(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	runID := "wfr-cov-launch-read-fail"
	ctx := context.Background()

	built, err := WorkflowRunBuild(prepared.Root, prepared.Res, prepared.Store, prepared.Repo, prepared.Compiled, prepared.RefBase, prepared.Inputs, prepared.InputSnapshot, prepared.Raw, runID, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("WorkflowRunBuild() error = %v", err)
	}
	if err := built.Controller.SetAdmission(built.Admission); err != nil {
		t.Fatalf("SetAdmission() error = %v", err)
	}
	if _, err := built.Controller.StartNew(ctx); err != nil {
		t.Fatalf("StartNew() error = %v", err)
	}

	realRepo := prepared.Repo
	prepared.Repo = &faultRepo{Repository: realRepo, getRunFailFrom: 1}
	_, err = e.LaunchStartedWorkflow(ctx, prepared, built, runID, "two-step", func() {})
	if err == nil || !strings.Contains(err.Error(), "scripted get-run failure") {
		t.Fatalf("LaunchStartedWorkflow() error = %v, want the scripted read failure", err)
	}
	e.mu.Lock()
	active := e.active[runID]
	e.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-time.After(2 * time.Second):
		}
	}
}

// TestSettleSessionCancelDeliveryPendingRefused covers the cancel error
// branch for a run that is not terminal: a delivery_pending run must refuse
// the cancel with the waiting-for-delivery error.
func TestSettleSessionCancelDeliveryPendingRefused(t *testing.T) {
	root, store, repo, closeFn, ctx, runID := openEventsFixtureWithRun(t, "wfr-cov-cancel-dp")
	defer closeFn()
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}

	e := NewSessionWorkflowEngine(root, filepath.Join(root, "config.toml"))
	_, err = e.settleSessionCancel(ctx, nil, repo, store, runID)
	if err == nil || !strings.Contains(err.Error(), "waiting for delivery") {
		t.Fatalf("settleSessionCancel() on a delivery_pending run error = %v, want the delivery refusal", err)
	}
}

// TestSettleSessionCancelErrorWithTerminalRun covers the idempotent branch:
// a cancel fault observed on a run that is already terminal must return the
// settled result, not the fault.
func TestSettleSessionCancelErrorWithTerminalRun(t *testing.T) {
	root, store, repo, closeFn, ctx, runID := openEventsFixtureWithRun(t, "wfr-cov-cancel-terminal")
	defer closeFn()
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	e := NewSessionWorkflowEngine(root, filepath.Join(root, "config.toml"))
	scripted := &faultRepo{Repository: repo, failListAttempts: true, getRunTerminalFrom: 2}
	result, err := e.settleSessionCancel(ctx, nil, scripted, store, runID)
	if err != nil {
		t.Fatalf("settleSessionCancel() on a terminal run error = %v, want the idempotent settled result", err)
	}
	if result.Status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("result status = %q, want succeeded", result.Status)
	}
}

// TestSettleSessionCancelReadFailureAfterSettle covers the post-settle read
// failure branch: a ledger fault re-reading the run after a successful cancel
// must surface the read error.
func TestSettleSessionCancelReadFailureAfterSettle(t *testing.T) {
	root, store, repo, closeFn, ctx, runID := openEventsFixtureWithRun(t, "wfr-cov-cancel-read-fail")
	defer closeFn()
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	e := NewSessionWorkflowEngine(root, filepath.Join(root, "config.toml"))
	scripted := &faultRepo{Repository: repo, getRunFailCanceled: true}
	_, err = e.settleSessionCancel(ctx, nil, scripted, store, runID)
	if err == nil || !strings.Contains(err.Error(), "scripted canceled-run read failure") {
		t.Fatalf("settleSessionCancel() error = %v, want the post-settle read failure", err)
	}
}

// TestSessionLaunchResumeDriveHookInvoked covers the resume goroutine's
// drive-before-delivery hook body: the hook must construct the prepared run
// from the resume payload and route it through maybeDriveSettledStack.
func TestSessionLaunchResumeDriveHookInvoked(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-cov-resume-drive-hook"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	hookCalled := make(chan struct{})
	originalLoop := SessionAutoDeliveryRepairLoopFunc
	SessionAutoDeliveryRepairLoopFunc = func(runCtx context.Context, _ workflowledger.Repository, _ string, _ *config.Resolved, _ *storage.SQLite, _ string, _ func(context.Context) (workflowledger.RunSnapshot, error), driveStack func(context.Context) (bool, error), _ bool) {
		if _, err := driveStack(runCtx); err != nil {
			t.Errorf("driveStack hook error = %v", err)
		}
		close(hookCalled)
	}
	t.Cleanup(func() { SessionAutoDeliveryRepairLoopFunc = originalLoop })

	engine := NewSessionWorkflowEngine(".", "")
	prepared := resumePrepared{
		runID: runID, workflow: "test",
		built:      WorkflowControllerBuild{Controller: &controller.LinearController{Holder: "cov-resume-hook"}, Dispatcher: workflowTestDispatcher{}},
		closeFn:    func() {},
		finishExec: func() {},
		repo:       repo,
		compiled:   &definition.CompiledWorkflow{},
	}
	if _, err := engine.launchResume(ctx, prepared); err != nil {
		t.Fatalf("launchResume() error = %v", err)
	}
	select {
	case <-hookCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("driveStack hook was not invoked by the resume goroutine")
	}
	engine.mu.Lock()
	active := engine.active[runID]
	engine.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-time.After(2 * time.Second):
		}
	}
}

// TestSessionLaunchResumeReadFailure covers the post-launch run re-read
// failure branch of launchResume.
func TestSessionLaunchResumeReadFailure(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-cov-resume-read-fail"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		t.Fatal(err)
	}

	originalLoop := SessionAutoDeliveryRepairLoopFunc
	SessionAutoDeliveryRepairLoopFunc = func(context.Context, workflowledger.Repository, string, *config.Resolved, *storage.SQLite, string, func(context.Context) (workflowledger.RunSnapshot, error), func(context.Context) (bool, error), bool) {
		return
	}
	t.Cleanup(func() { SessionAutoDeliveryRepairLoopFunc = originalLoop })

	engine := NewSessionWorkflowEngine(".", "")
	prepared := resumePrepared{
		runID: runID, workflow: "test",
		built:      WorkflowControllerBuild{Controller: &controller.LinearController{Holder: "cov-resume-read"}, Dispatcher: workflowTestDispatcher{}},
		closeFn:    func() {},
		finishExec: func() {},
		repo:       &faultRepo{Repository: repo, getRunFailFrom: 1},
		compiled:   &definition.CompiledWorkflow{},
	}
	_, err := engine.launchResume(ctx, prepared)
	if err == nil || !strings.Contains(err.Error(), "scripted get-run failure") {
		t.Fatalf("launchResume() error = %v, want the scripted read failure", err)
	}
	engine.mu.Lock()
	active := engine.active[runID]
	engine.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-time.After(2 * time.Second):
		}
	}
}

// TestApplyAcceptedVerifierChangesNoDrift covers the acceptance report for a
// resume whose verifier definitions did not drift.
func TestApplyAcceptedVerifierChangesNoDrift(t *testing.T) {
	var stderr strings.Builder
	if err := applyAcceptedVerifierChanges(&workflowledger.Snapshot{}, &definition.CompiledWorkflow{}, map[string]config.VerifierProfile{}, &stderr); err != nil {
		t.Fatalf("applyAcceptedVerifierChanges() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "no verifier definitions drifted") {
		t.Fatalf("stderr = %q, want the no-drift notice", stderr.String())
	}
}
