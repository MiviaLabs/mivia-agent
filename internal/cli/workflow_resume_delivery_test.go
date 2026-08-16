package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

// casRunTo moves the fixture run through pending->running->delivery_pending
// under CAS on the versions observed from GetRun, mirroring the ledger's
// status transitions for a workflow body that finished and entered delivery.
func casRunToDeliveryPending(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID string) {
	t.Helper()
	for _, status := range []workflowledger.RunStatus{
		workflowledger.RunStatusRunning,
		workflowledger.RunStatusDeliveryPending,
	} {
		stored, err := repo.GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, status, nil); err != nil {
			t.Fatalf("CompareAndSetRunStatus(%q, %q): %v", runID, status, err)
		}
	}
}

// routeAttemptToSuccess records a completed attempt that routed the run to the
// reserved terminal step "success" without a run status CAS — the exact shape
// that an older classification would have settled to succeeded, skipping
// delivery for a delivery_pending run.
func routeAttemptToSuccess(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID, stepID string) {
	t.Helper()
	attempt := workflowledger.StepAttempt{
		AttemptID: "att-" + stepID, RunID: runID, StepID: stepID, AttemptNo: 1,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	outcome := workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, ToStepID: "success", MatchDigest: "match",
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, stored.Version, outcome); err != nil {
		t.Fatal(err)
	}
}

// TestResumeRefusesDeliveryPending: a delivery_pending run (whose derived
// active step routed to the reserved "success" terminal) must be refused by
// executeWorkflowResume BEFORE any terminal reconciliation — and left
// completely untouched: still delivery_pending, same version. Delivery is a
// separate host-owned step; resume must never CAS it (an older classification
// could settle delivery_pending->succeeded and skip delivery).
func TestResumeRefusesDeliveryPending(t *testing.T) {
	root, configPath, repo, run := newResumeFailureFixture(t)
	ctx := context.Background()
	casRunToDeliveryPending(t, ctx, repo, run.RunID)
	routeAttemptToSuccess(t, ctx, repo, run.RunID, "one")

	before, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("fixture status = %q, want %q", before.Status, workflowledger.RunStatusDeliveryPending)
	}

	originalOpen := workflowResumeOpenStore
	t.Cleanup(func() { workflowResumeOpenStore = originalOpen })
	workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}

	err = executeWorkflowResume(run.RunID, root, configPath, true, false, false, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "waiting for delivery") {
		t.Fatalf("executeWorkflowResume() error = %v, want a 'waiting for delivery' refusal", err)
	}

	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want %q (delivery_pending must not be settled)", after.Status, workflowledger.RunStatusDeliveryPending)
	}
	if after.Version != before.Version {
		t.Fatalf("run version = %d, want %d (delivery_pending must not be CASed)", after.Version, before.Version)
	}
}

// TestReconcileWorkflowTerminalSkipsDeliveryPendingCAS: the reconcile step
// reports a settled delivery_pending run as terminal WITHOUT error and WITHOUT
// touching it. CASing delivery_pending->delivery_pending is an invalid
// transition, so skipping the CAS is both required and the observable contract.
func TestReconcileWorkflowTerminalSkipsDeliveryPendingCAS(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-delivery-pending-cas", Status: workflowledger.RunStatusPending, ActiveStepID: "two",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	casRunToDeliveryPending(t, ctx, repo, run.RunID)
	routeAttemptToSuccess(t, ctx, repo, run.RunID, "two")
	before, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	terminal, err := reconcileWorkflowTerminal(ctx, repo, run.RunID, false, &stdout)
	if err != nil {
		t.Fatalf("reconcileWorkflowTerminal() error = %v, want nil (no delivery_pending->delivery_pending CAS)", err)
	}
	if !terminal {
		t.Fatal("reconcileWorkflowTerminal() = false, want true for a settled delivery_pending run")
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want %q (must not settle to succeeded)", after.Status, workflowledger.RunStatusDeliveryPending)
	}
	if after.Version != before.Version {
		t.Fatalf("run version = %d, want %d (no CAS on a settled delivery_pending run)", after.Version, before.Version)
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("reconcile output = %q, want status=delivery_pending", stdout.String())
	}
}

// TestReconcileWorkflowTerminalStillSettlesDerivedSuccess: regression guard
// against over-broadening the skip — a RUNNING run whose attempt routed to the
// reserved terminal step "success" (no status CAS yet) must still be settled
// to succeeded, bumping the version. Only the equal-status case (delivery
// pending settled) may skip the CAS.
func TestReconcileWorkflowTerminalStillSettlesDerivedSuccess(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-derived-success-still", Status: workflowledger.RunStatusPending, ActiveStepID: "one",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	routeAttemptToSuccess(t, ctx, repo, run.RunID, "one")

	var stdout bytes.Buffer
	terminal, err := reconcileWorkflowTerminal(ctx, repo, run.RunID, false, &stdout)
	if err != nil {
		t.Fatalf("reconcileWorkflowTerminal() error = %v", err)
	}
	if !terminal {
		t.Fatal("reconcileWorkflowTerminal() = false, want true for a run routed to success")
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want %q (derived success must still CAS)", after.Status, workflowledger.RunStatusSucceeded)
	}
	if after.Version <= stored.Version {
		t.Fatalf("run version = %d, want > %d (derived success must CAS and bump)", after.Version, stored.Version)
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("reconcile output = %q, want status=succeeded", stdout.String())
	}
}

// TestReconcileWorkflowTerminalDeliveryActiveSettlesDeliveryPending: a RUNNING
// run whose attempt durably routed to "success" under an active delivery
// policy must settle to delivery_pending (never succeeded): the durable route
// write happened but the delivery_pending CAS was lost to a crash. Delivery
// must not be skipped by the resume path.
func TestReconcileWorkflowTerminalDeliveryActiveSettlesDeliveryPending(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-derived-success-delivery", Status: workflowledger.RunStatusPending, ActiveStepID: "one",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	routeAttemptToSuccess(t, ctx, repo, run.RunID, "one")

	var stdout bytes.Buffer
	terminal, err := reconcileWorkflowTerminal(ctx, repo, run.RunID, true, &stdout)
	if err != nil {
		t.Fatalf("reconcileWorkflowTerminal() error = %v", err)
	}
	if !terminal {
		t.Fatal("reconcileWorkflowTerminal() = false, want true for a run routed to success")
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want %q (delivery policy must settle to delivery_pending, never succeeded)", after.Status, workflowledger.RunStatusDeliveryPending)
	}
	if after.Version <= stored.Version {
		t.Fatalf("run version = %d, want > %d (the settle CAS must bump the version once)", after.Version, stored.Version)
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("reconcile output = %q, want status=delivery_pending", stdout.String())
	}
	// A follow-up reconcile must be a no-op: settled status equals the plan's.
	before := after
	var again bytes.Buffer
	terminal, err = reconcileWorkflowTerminal(ctx, repo, run.RunID, true, &again)
	if err != nil {
		t.Fatalf("second reconcileWorkflowTerminal() error = %v", err)
	}
	if !terminal {
		t.Fatal("second reconcileWorkflowTerminal() = false, want true")
	}
	after, err = repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusDeliveryPending || after.Version != before.Version {
		t.Fatalf("second reconcile changed the run: status=%q version=%d, want delivery_pending/%d (no-op)", after.Status, after.Version, before.Version)
	}
}

// newExecuteResumeDeliveryFixture builds a real two-step workflow admitted
// with an active draft delivery policy, a real git origin, and a real
// worktree (via workflowspace.Ensure, the same call buildWorkflowController
// makes) - but stops short of running any step: the run is created RUNNING at
// its initial step with no recorded attempts, so PlanResume finds it
// non-terminal and executeWorkflowResume proceeds past
// reconcileWorkflowTerminal into the normal workflowResumeBuild/Run path.
// The returned repo and runID let the caller drive the run to
// delivery_pending either by seeding a routed-to-success attempt (the
// crash-recovery path, reconcileWorkflowTerminal settles it) or by stubbing
// workflowResumeRun to settle it directly (the normal resume path).
func newExecuteResumeDeliveryFixture(t *testing.T) (root, configPath string, repo *workflowledger.StorageRepository, runID string) {
	t.Helper()
	root = t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	setWorkflowAgentTools(t, root, "write_file")
	appendWorkflowDeliveryPolicy(t, root, "draft")
	initWorkflowGitRepoWithOrigin(t, root)

	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runID = "wfr-resume-delivery"
	identity, err := workflowspace.Ensure(ctx, root, runID, workflowspace.IsolationWorktree)
	if err != nil {
		t.Fatal(err)
	}
	remoteURL, originBaseCommit, err := workflowDeliveryAdmission(compiled, identity, true)
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
	store, err := openContextStorePath(storePath)
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
	return root, filepath.Join(root, "config.toml"), repo, runID
}

// wireExecuteResumeDeliveryStubs bypasses real controller execution the same
// way TestExecuteWorkflowResumeInjectedFailures does: workflowResumeBuild
// returns a Dispatcher-only build with a nil Controller (fine, since the
// fixture run carries no in-flight attempts to join), and workflowResumeRun
// is replaced so the test controls exactly how - and whether - the run
// settles to delivery_pending. deliverRunWithStore itself (reached through
// finishWorkflowRunDelivery) is NOT stubbed: it runs for real against the
// fixture's real worktree and origin.
func wireExecuteResumeDeliveryStubs(t *testing.T, repo workflowledger.Repository, runID string, resumeRun func(ctx context.Context, b workflowControllerBuild) (workflowledger.RunSnapshot, error)) {
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
		return nil, repo, func() {}, nil
	}
	workflowResumeInstallHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, []byte, *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
		return workflowControllerBuild{Dispatcher: workflowTestDispatcher{}}, nil
	}
	workflowResumeSetAdmission = func(workflowControllerBuild) error { return nil }
	workflowResumeSetForce = func(workflowControllerBuild) error { return nil }
	workflowResumeRun = resumeRun
}

// settleResumeFixtureToDeliveryPending is the workflowResumeRun stub used by
// tests that exercise the normal resume-settle path: it stands in for a real
// controller.Run() that just finished the workflow body and CASed the run to
// delivery_pending.
func settleResumeFixtureToDeliveryPending(repo workflowledger.Repository, runID string) func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
	return func(ctx context.Context, _ workflowControllerBuild) (workflowledger.RunSnapshot, error) {
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

// TestExecuteWorkflowResumeAllowPublishDeliversNormalPath: a resume that
// settles to delivery_pending through the normal workflowResumeRun path (the
// workflow body just finished) triggers delivery when allowPublish is true -
// the CLI resume path must not leave the run stuck delivery_pending like
// `workflow deliver` never having been asked to run.
func TestExecuteWorkflowResumeAllowPublishDeliversNormalPath(t *testing.T) {
	root, configPath, repo, runID := newExecuteResumeDeliveryFixture(t)
	recorder := &recordingPRClient{}
	originalNewPR := workflowDeliverNewPR
	workflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { workflowDeliverNewPR = originalNewPR })
	wireExecuteResumeDeliveryStubs(t, repo, runID, settleResumeFixtureToDeliveryPending(repo, runID))

	var stdout bytes.Buffer
	err := executeWorkflowResume(runID, root, configPath, false, true, false, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("executeWorkflowResume() error = %v; stdout = %q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("resume stdout = %q, want a settled status=succeeded line from delivery", stdout.String())
	}
	if creates, finds := recorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each", creates, finds)
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after delivery", run.Status)
	}
}

// TestExecuteWorkflowResumeWithoutAllowPublishSkipsDeliverNormalPath: the
// same settle-to-delivery_pending scenario without --allow-publish must NOT
// deliver, and must print the same non-publication explanation
// finishWorkflowRunDelivery prints for `workflow run`.
func TestExecuteWorkflowResumeWithoutAllowPublishSkipsDeliverNormalPath(t *testing.T) {
	root, configPath, repo, runID := newExecuteResumeDeliveryFixture(t)
	recorder := &recordingPRClient{}
	originalNewPR := workflowDeliverNewPR
	workflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { workflowDeliverNewPR = originalNewPR })
	wireExecuteResumeDeliveryStubs(t, repo, runID, settleResumeFixtureToDeliveryPending(repo, runID))

	var stdout bytes.Buffer
	err := executeWorkflowResume(runID, root, configPath, false, false, false, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("executeWorkflowResume() error = %v; stdout = %q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "requires --allow-publish") {
		t.Fatalf("resume stdout = %q, want the non-publication explanation", stdout.String())
	}
	if creates, finds := recorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero", creates, finds)
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending (delivery not attempted)", run.Status)
	}
}

// TestExecuteWorkflowResumeNonDeliveryPendingTerminalIgnoresAllowPublish is
// the regression guard: a resume that settles to a NON-delivery-pending
// terminal status must behave the same whether or not --allow-publish is
// passed - no delivery attempt, no delivery-related output.
func TestExecuteWorkflowResumeNonDeliveryPendingTerminalIgnoresAllowPublish(t *testing.T) {
	for _, allowPublish := range []bool{false, true} {
		t.Run(fmt.Sprintf("allowPublish=%v", allowPublish), func(t *testing.T) {
			root, run := newForcedResumeFixture(t)
			configPath := filepath.Join(root, "config.toml")
			var stdout bytes.Buffer
			if err := executeWorkflowResume(run.RunID, root, configPath, false, allowPublish, false, &stdout, io.Discard); err != nil {
				t.Fatalf("executeWorkflowResume() error = %v", err)
			}
			if !strings.Contains(stdout.String(), "status=succeeded") {
				t.Fatalf("resume stdout = %q, want status=succeeded", stdout.String())
			}
			if strings.Contains(stdout.String(), "--allow-publish") {
				t.Fatalf("resume stdout = %q, want no delivery explanation for a non-delivery workflow", stdout.String())
			}
		})
	}
}

// TestExecuteWorkflowResumeAllowPublishPropagatesDeliveryFailure: when
// delivery itself fails during a resume with --allow-publish, the failure
// must propagate as a non-nil error from executeWorkflowResume - mirroring
// `workflow run`'s synchronous behavior. (The async session/tool-engine
// resume path instead swallows the error into a recorded delivery_failed
// state; the CLI resume path is synchronous like `run`, so it must not.)
func TestExecuteWorkflowResumeAllowPublishPropagatesDeliveryFailure(t *testing.T) {
	root, configPath, repo, runID := newExecuteResumeDeliveryFixture(t)
	recorder := &recordingPRClient{}
	originalNewPR := workflowDeliverNewPR
	workflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { workflowDeliverNewPR = originalNewPR })
	wireExecuteResumeDeliveryStubs(t, repo, runID, settleResumeFixtureToDeliveryPending(repo, runID))

	// Break the origin remote AFTER admission (which already resolved and
	// recorded a valid RemoteURL): the real git push inside delivery.Deliver
	// now fails with a transient execution fault, not a refusal, and the
	// workflow's delivery policy carries no on_failure repair step - the
	// error must propagate straight out of executeWorkflowResume.
	if out, err := exec.Command("git", "-C", root, "remote", "remove", "origin").CombinedOutput(); err != nil {
		t.Fatalf("git remote remove origin: %v: %s", err, out)
	}

	var stdout bytes.Buffer
	err := executeWorkflowResume(runID, root, configPath, false, true, false, &stdout, io.Discard)
	if err == nil {
		t.Fatalf("executeWorkflowResume() error = nil, want a propagated delivery failure; stdout = %q", stdout.String())
	}
	if creates, finds := recorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero on a delivery failure", creates, finds)
	}
}

// TestExecuteWorkflowResumeAllowPublishDeliversCrashRecoveryPath: a run that
// reconciles to terminal through reconcileWorkflowTerminal (the crash-window
// path: the derived route already reached the reserved "success" step but
// the delivery_pending status CAS was never recorded) also triggers delivery
// when allowPublish is true - the SAME resume call must deliver, not just
// settle the status and leave it for a separate `workflow deliver`.
func TestExecuteWorkflowResumeAllowPublishDeliversCrashRecoveryPath(t *testing.T) {
	root, configPath, repo, runID := newExecuteResumeDeliveryFixture(t)
	ctx := context.Background()
	// Simulate the crash window: the workflow body durably routed step "one"
	// to the reserved "success" terminal, but the process died before the
	// delivery_pending status CAS landed. No workflowResumeRun stub is wired:
	// reconcileWorkflowTerminal must be the one that settles the run.
	routeAttemptToSuccess(t, ctx, repo, runID, "one")

	recorder := &recordingPRClient{}
	originalNewPR := workflowDeliverNewPR
	workflowDeliverNewPR = func() delivery.PRClient { return recorder }
	t.Cleanup(func() { workflowDeliverNewPR = originalNewPR })

	var stdout bytes.Buffer
	err := executeWorkflowResume(runID, root, configPath, false, true, false, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("executeWorkflowResume() error = %v; stdout = %q", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("resume stdout = %q, want the crash-recovery settle line status=delivery_pending", stdout.String())
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("resume stdout = %q, want a settled status=succeeded line from delivery", stdout.String())
	}
	if creates, finds := recorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one of each", creates, finds)
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded after delivery", run.Status)
	}
}
