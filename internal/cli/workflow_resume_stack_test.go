package cli

// Resume drive-before-delivery regressions (plan D2/D3/D8, §5a) on the CLI
// resume surface:
//
// A stacking-enabled delivery workflow's plan run settles at delivery_pending
// (its success terminal is delivery-policy active). Before this fix the CLI
// resume path called finishWorkflowRunDelivery directly on that settlement -
// the plan PR got published and the chunk stack was never driven
// (deliver-before-drive ordering bug, F1). These tests pin the fixed ordering
// on both resume settle points: the normal resume-settle path
// (runWorkflowResumeAndSettle) and the crash-recovery path
// (finishWorkflowResumeTerminal). An interrupted multi-chunk plan run resumed
// with --allow-publish must drive its stack before any delivery decision, and
// with delivery.deliver_plan_run unset (false) the plan run's own PR is never
// created - it settles succeeded and the plan and its artifacts stay recorded
// in the ledger. With deliver_plan_run=true the plan run still delivers AFTER
// the drive.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

// newExecuteResumeStackFixture builds a real stacking-enabled delivery
// workflow (mini-stack: plan -> implement -> success with an active draft
// pull_request policy and [stacking] plan_step=plan), a real git origin, and
// a real worktree - the same shape as newExecuteResumeDeliveryFixture but
// with stacking enabled, so a settled plan run must drive its chunk stack
// before the plan run is delivered. The run is created RUNNING at its initial
// step with no recorded attempts, so executeWorkflowResume proceeds past
// reconcileWorkflowTerminal into the normal workflowResumeBuild/Run path and
// the caller controls the settlement.
func newExecuteResumeStackFixture(t *testing.T, deliverPlanRun bool) (root, configPath string, store *storage.SQLite, repo *workflowledger.StorageRepository, runID string) {
	t.Helper()
	root = t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	setWorkflowAgentTools(t, root, "write_file")
	toml := strings.Replace(miniStackWorkflowTOML, "name = \"mini-stack\"", "name = \"two-step\"", 1)
	if deliverPlanRun {
		toml = strings.Replace(toml, "kind = \"pull_request\"", "deliver_plan_run = true\nkind = \"pull_request\"", 1)
	}
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "two-step.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepoWithOrigin(t, root)

	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runID = "wfr-resume-stack"
	identity, err := workflowspace.Ensure(ctx, root, runID, workflowspace.IsolationWorktree)
	if err != nil {
		t.Fatal(err)
	}
	remoteURL, originBaseCommit, err := workflowDeliveryAdmission(compiled, identity, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(identity.Root, "change.txt"), []byte("seeded change\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot), InputDigest: workflowledger.InputDigest(snapshot.Inputs),
		Status: workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
		BaseRef: identity.BaseRef, BaseCommit: identity.BaseCommit, OriginBaseCommit: originBaseCommit,
		WorktreeName: identity.WorktreeName, RemoteURL: remoteURL,
	}
	store, err = openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo = workflowledger.NewStorageRepository(store)
	if err := repo.CreateRun(ctx, run, rawSnapshot); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "config.toml"), store, repo, runID
}

// wireExecuteResumeStackStubs bypasses real controller execution the same way
// wireExecuteResumeDeliveryStubs does (Dispatcher-only build, stubbed
// workflowResumeRun), but keeps workflowResumeOpenStore pointing at a REAL
// store: the stack drive seeds the stack ledger through prepared.store, which
// delivery-only resume tests never touch. The stub returns the same repo the
// test uses plus a second handle to the fixture's sqlite file.
func wireExecuteResumeStackStubs(t *testing.T, storePath string, repo workflowledger.Repository, runID string, resumeRun func(ctx context.Context, b workflowControllerBuild) (workflowledger.RunSnapshot, error)) {
	t.Helper()
	originalOpen := workflowResumeOpenStore
	originalHooks := workflowResumeInstallHooks
	originalBuild := workflowResumeBuild
	originalAdmission := workflowResumeSetAdmission
	originalForce := workflowResumeSetForce
	originalRun := workflowResumeRun
	t.Cleanup(func() {
		workflowResumeOpenStore = originalOpen
		workflowResumeInstallHooks = originalHooks
		workflowResumeBuild = originalBuild
		workflowResumeSetAdmission = originalAdmission
		workflowResumeSetForce = originalForce
		workflowResumeRun = originalRun
	})
	workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		store, err := openContextStorePath(storePath)
		if err != nil {
			return nil, nil, nil, err
		}
		return store, repo, func() { _ = store.Close() }, nil
	}
	workflowResumeInstallHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, []byte, *workflowledger.RunSnapshot, map[string]bool, *skills.Registry) (workflowControllerBuild, error) {
		return workflowControllerBuild{Dispatcher: workflowTestDispatcher{}}, nil
	}
	workflowResumeSetAdmission = func(workflowControllerBuild) error { return nil }
	workflowResumeSetForce = func(workflowControllerBuild) error { return nil }
	workflowResumeRun = resumeRun
}

// settleResumeStackFixtureToDeliveryPending is the workflowResumeRun stub for
// the normal resume-settle path: it stands in for a real controller.Run()
// that just finished the plan-mode body - the decompose step recorded its
// multi-chunk plan output, then the run CASed to delivery_pending. Seeding
// the decompose output inside the stub (after joinInFlightAttempts has run)
// keeps PlanResume's pre-Run view attempt-free while still leaving the
// recorded plan the drive hook reads.
func settleResumeStackFixtureToDeliveryPending(t *testing.T, repo workflowledger.Repository, runID string) func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
	return func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		seedSucceededDecomposeAttempt(t, repo, runID, []byte(multiChunkPlanOutput))
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			return workflowledger.RunSnapshot{}, err
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
			return workflowledger.RunSnapshot{}, err
		}
		return repo.GetRun(ctx, runID)
	}
}

// orderRecordingDrive records the drive into a shared call-order log (before
// delegating to recordingStackDrive) so tests can pin drive-before-delivery.
type orderRecordingDrive struct {
	rec   *orderRecorder
	inner *recordingStackDrive
}

func (d *orderRecordingDrive) Drive(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, stackID string, chunks []ChunkPlan, hasMore bool, remainingScope string, planInputs map[string]string, allowPublish bool, stdout, stderr io.Writer) error {
	d.rec.add("drive")
	return d.inner.Drive(ctx, prepared, ledger, stackID, chunks, hasMore, remainingScope, planInputs, allowPublish, stdout, stderr)
}

// TestExecuteWorkflowResumeDrivesSettledStackBeforeDeliverySkippedPlanRun:
// an interrupted multi-chunk plan run resumed with --allow-publish through
// the NORMAL resume-settle path (workflowResumeRun settles delivery_pending)
// drives its chunk stack before any delivery decision, and with
// delivery.deliver_plan_run unset (false) the plan run's own PR is never
// created - the run settles succeeded with the plan and its artifacts
// recorded in the ledger.
func TestExecuteWorkflowResumeDrivesSettledStackBeforeDeliverySkippedPlanRun(t *testing.T) {
	root, configPath, _, repo, runID := newExecuteResumeStackFixture(t, false)
	storePath := filepath.Join(root, "workflow.db")
	rec := &orderRecorder{}
	drive := &orderRecordingDrive{rec: rec, inner: &recordingStackDrive{}}
	originalDrive := workflowStackDriveToCompletion
	t.Cleanup(func() { workflowStackDriveToCompletion = originalDrive })
	workflowStackDriveToCompletion = drive.Drive

	recorder := &recordingPRClient{}
	originalNewPR := workflowDeliverNewPR
	workflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { workflowDeliverNewPR = originalNewPR })

	wireExecuteResumeStackStubs(t, storePath, repo, runID, settleResumeStackFixtureToDeliveryPending(t, repo, runID))

	var stdout bytes.Buffer
	if err := executeWorkflowResume(runID, root, configPath, false, true, false, false, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowResume() error = %v; stdout = %q", err, stdout.String())
	}
	if !drive.inner.called || drive.inner.stackID != runID || len(drive.inner.chunks) != 2 {
		t.Fatalf("stack drive = %+v, want stack %q with 2 chunks", drive.inner, runID)
	}
	if got := strings.Join(rec.order, ","); got != "drive" {
		t.Fatalf("call order = %q, want exactly the drive (no delivery for a skipped plan run)", got)
	}
	if creates, finds := recorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want none for a skipped plan run", creates, finds)
	}
	if !strings.Contains(stdout.String(), "multi-chunk plan (2 chunks)") {
		t.Fatalf("stdout = %q, want the stack drive notice", stdout.String())
	}
	if !strings.Contains(stdout.String(), "plan PR not created (delivery.deliver_plan_run=false)") {
		t.Fatalf("stdout = %q, want the skipped-plan-run notice", stdout.String())
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (plan run settled without publication)", run.Status)
	}
}

// TestExecuteWorkflowResumeCrashRecoveryDrivesStackBeforeDeliverySkippedPlanRun
// pins the same drive-before-delivery ordering on the CRASH-RECOVERY settle
// point (finishWorkflowResumeTerminal): the plan body durably routed step
// "plan" to the reserved "success" terminal and recorded the multi-chunk
// decompose output, but the delivery_pending status CAS was lost to a crash.
// reconcileWorkflowTerminal settles the run, and the resume call still drives
// the stack and skips the plan run's PR (deliver_plan_run=false).
func TestExecuteWorkflowResumeCrashRecoveryDrivesStackBeforeDeliverySkippedPlanRun(t *testing.T) {
	root, configPath, _, repo, runID := newExecuteResumeStackFixture(t, false)
	ctx := context.Background()
	// Seed the decompose output FIRST so the routed-to-success completion
	// stays the newest step-bearing event (derivedActiveStep) and
	// reconcileWorkflowTerminal settles the run to delivery_pending.
	seedSucceededDecomposeAttempt(t, repo, runID, []byte(multiChunkPlanOutput))
	routeAttemptToSuccess(t, ctx, repo, runID, "plan")
	storePath := filepath.Join(root, "workflow.db")

	rec := &orderRecorder{}
	drive := &orderRecordingDrive{rec: rec, inner: &recordingStackDrive{}}
	originalDrive := workflowStackDriveToCompletion
	t.Cleanup(func() { workflowStackDriveToCompletion = originalDrive })
	workflowStackDriveToCompletion = drive.Drive

	recorder := &recordingPRClient{}
	originalNewPR := workflowDeliverNewPR
	workflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { workflowDeliverNewPR = originalNewPR })

	// No workflowResumeRun stub: reconcileWorkflowTerminal must be the one
	// that settles the run to delivery_pending.
	wireExecuteResumeStackStubs(t, storePath, repo, runID, func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		t.Fatal("workflowResumeRun must not run when reconcileWorkflowTerminal settles the run")
		return workflowledger.RunSnapshot{}, nil
	})

	var stdout bytes.Buffer
	if err := executeWorkflowResume(runID, root, configPath, false, true, false, false, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowResume() error = %v; stdout = %q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("resume stdout = %q, want the crash-recovery settle line status=delivery_pending", stdout.String())
	}
	if !drive.inner.called || drive.inner.stackID != runID || len(drive.inner.chunks) != 2 {
		t.Fatalf("stack drive = %+v, want stack %q with 2 chunks", drive.inner, runID)
	}
	if got := strings.Join(rec.order, ","); got != "drive" {
		t.Fatalf("call order = %q, want exactly the drive (no delivery for a skipped plan run)", got)
	}
	if creates, finds := recorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want none for a skipped plan run", creates, finds)
	}
	if !strings.Contains(stdout.String(), "plan PR not created (delivery.deliver_plan_run=false)") {
		t.Fatalf("stdout = %q, want the skipped-plan-run notice", stdout.String())
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (plan run settled without publication)", run.Status)
	}
}

// TestExecuteWorkflowResumeDrivesSettledStackBeforeDeliveryPublishesPlanRun
// pins the opt-in on the resume surface: with delivery.deliver_plan_run=true
// the delivery branch still runs AFTER the stack drive (the ordering fix is
// preserved), so a resumed plan run publishes its own PR and settles
// succeeded.
func TestExecuteWorkflowResumeDrivesSettledStackBeforeDeliveryPublishesPlanRun(t *testing.T) {
	root, configPath, _, repo, runID := newExecuteResumeStackFixture(t, true)
	storePath := filepath.Join(root, "workflow.db")
	rec := &orderRecorder{}
	drive := &orderRecordingDrive{rec: rec, inner: &recordingStackDrive{}}
	originalDrive := workflowStackDriveToCompletion
	t.Cleanup(func() { workflowStackDriveToCompletion = originalDrive })
	workflowStackDriveToCompletion = drive.Drive

	recorder := &recordingPRClient{}
	originalNewPR := workflowDeliverNewPR
	workflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { workflowDeliverNewPR = originalNewPR })

	prevGit := workflowDeliverGit
	t.Cleanup(func() { workflowDeliverGit = prevGit })
	workflowDeliverGit = orderGitRunner{rec: rec}

	wireExecuteResumeStackStubs(t, storePath, repo, runID, settleResumeStackFixtureToDeliveryPending(t, repo, runID))

	var stdout bytes.Buffer
	if err := executeWorkflowResume(runID, root, configPath, false, true, false, false, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowResume() error = %v; stdout = %q", err, stdout.String())
	}
	if !drive.inner.called || drive.inner.stackID != runID || len(drive.inner.chunks) != 2 {
		t.Fatalf("stack drive = %+v, want stack %q with 2 chunks", drive.inner, runID)
	}
	if len(rec.order) < 2 || rec.order[0] != "drive" {
		t.Fatalf("call order = %q, want the stack drive to run before any delivery git call", strings.Join(rec.order, ","))
	}
	for i, step := range rec.order[1:] {
		if step != "deliver" {
			t.Fatalf("call order = %q: entry %d = %q, want deliver after the drive", strings.Join(rec.order, ","), i+1, step)
		}
	}
	if creates, finds := recorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each", creates, finds)
	}
	if !strings.Contains(stdout.String(), "multi-chunk plan (2 chunks)") {
		t.Fatalf("stdout = %q, want the stack drive notice", stdout.String())
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after the drive + delivery", run.Status)
	}
}

// stackingResumeSnapshot builds the run+snapshot pair validateWorkflowResumeSnapshot
// consumes for a stacking-enabled workflow (mini-stack: plan -> implement ->
// success with [stacking] plan_step=plan, implement_step=implement). The
// snapshot's Inputs carry the engine-reserved chunk-mode inputs a real
// chunk-mode run records, exactly like the admission payload applyStackingInputs
// merges at start time.
func stackingResumeSnapshot(t *testing.T, toml string, inputs map[string]string) (workflowledger.RunSnapshot, []byte) {
	t.Helper()
	name := workflowTOMLName(t, toml)
	wf, _, err := definition.ParseWorkflowTOML([]byte(toml), name+".toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(toml),
		DefinitionDigest: compiled.Digest,
		Inputs:           inputs,
		Agents: map[string]workflowledger.AgentSnapshot{
			"one": {Digest: "agent-one"},
			"two": {Digest: "agent-two"},
		},
	}
	raw, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		WorkflowName:   compiled.Name,
		WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(raw),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
	}
	return run, raw
}

// workflowTOMLName extracts the workflow's declared name from its TOML text
// so the snapshot filename matches the definition's in-file name.
func workflowTOMLName(t *testing.T, toml string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
	m := re.FindStringSubmatch(toml)
	if len(m) != 2 {
		t.Fatalf("workflow TOML has no name: %s", toml)
	}
	return m[1]
}

// TestValidateWorkflowResumeSnapshotAcceptsReservedStackingInputs pins the
// resume-side half of decision D3: admission merges the engine-reserved
// stacking inputs (stack_mode, chunk, pr_base, stack_part, chunk_plan) into a
// stacking run's input contract BEFORE input validation, so a chunk-mode run
// records them in its snapshot. Resume must accept the same contract; before
// the fix it rejected the snapshot with 'snapshot contains unknown workflow
// input "chunk"' and no chunk-mode run could be delivered.
func TestValidateWorkflowResumeSnapshotAcceptsReservedStackingInputs(t *testing.T) {
	run, raw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{
		"task":       "add feature",
		"stack_mode": "chunk",
		"chunk":      "c1",
	})
	if _, _, _, err := validateWorkflowResumeSnapshot(run, raw); err != nil {
		t.Fatalf("resume validation rejected engine-reserved stacking inputs: %v", err)
	}
}

// TestValidateWorkflowResumeSnapshotRejectsReservedInputsOnNonStacking guards
// the other direction: a NON-stacking workflow has no synthesized inputs, so a
// snapshot carrying an engine-reserved name is still an unknown input and must
// fail exactly as before. The fix must not widen the accepted contract beyond
// stacking runs.
func TestValidateWorkflowResumeSnapshotRejectsReservedInputsOnNonStacking(t *testing.T) {
	const plainTOML = `version = 1
name = "plain"
initial_step = "one"

[inputs.task]
type = "string"
required = true

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	wf, _, err := definition.ParseWorkflowTOML([]byte(plainTOML), "plain.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if compiled.Stacking != nil {
		t.Fatal("plain workflow resolved stacking, want nil")
	}
	run, raw := stackingResumeSnapshot(t, plainTOML, map[string]string{
		"task":  "x",
		"chunk": "c1",
	})
	_, _, _, err = validateWorkflowResumeSnapshot(run, raw)
	if err == nil || !strings.Contains(err.Error(), "snapshot contains unknown workflow input") {
		t.Fatalf("non-stacking resume error = %v, want 'snapshot contains unknown workflow input'", err)
	}
}
