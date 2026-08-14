package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// failWhenRunningRepository wraps a repository and fails GetRun the first time
// it observes the run already in running status: a storage fault inside the
// controller's Advance, after the run left pending but before any step work.
// That is exactly the failure the CLI run path must settle: the controller
// stops with a raw non-deadline error and the run row would otherwise stay
// running with no cause.
type failWhenRunningRepository struct {
	workflowledger.Repository
	mu     sync.Mutex
	failed bool
	err    error
}

func (r *failWhenRunningRepository) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	run, err := r.Repository.GetRun(ctx, runID)
	if err != nil {
		return run, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.Status == workflowledger.RunStatusRunning && !r.failed {
		r.failed = true
		return workflowledger.RunSnapshot{}, r.err
	}
	return run, nil
}

// TestExecuteWorkflowResumeSettlesRunFailureBeforeFirstAdvance: the CLI resume
// path must settle a run whose controller stopped with a genuine (non-deadline)
// error before its first Advance. The handoff claim is released before the
// settle, so the settle's own claim succeeds and the run reaches failed with
// the cause logged, instead of staying running with no explanation (DC-9, the
// 6c5419d class on the CLI resume entry point).
func TestExecuteWorkflowResumeSettlesRunFailureBeforeFirstAdvance(t *testing.T) {
	root, _ := newForcedResumeFixture(t)
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-resume-settle-fail", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	if err := repo.CreateRun(ctx, run, rawSnapshot); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	ctrl := &controller.LinearController{Holder: "resume-settle-fail-holder"}

	originalOpen := workflowResumeOpenStore
	originalBuild := workflowResumeBuild
	originalAdmission := workflowResumeSetAdmission
	originalForce := workflowResumeSetForce
	originalHooks := workflowResumeInstallHooks
	originalRun := workflowResumeRun
	t.Cleanup(func() {
		workflowResumeOpenStore = originalOpen
		workflowResumeBuild = originalBuild
		workflowResumeSetAdmission = originalAdmission
		workflowResumeSetForce = originalForce
		workflowResumeInstallHooks = originalHooks
		workflowResumeRun = originalRun
	})
	workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
		return nil, repo, func() {}, nil
	}
	workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
		return workflowControllerBuild{
			Controller: ctrl,
			Dispatcher: workflowTestDispatcher{},
			Admission:  controller.Admission{InputDigest: workflowledger.InputDigest(snapshot.Inputs)},
		}, nil
	}
	workflowResumeSetAdmission = func(workflowControllerBuild) error { return nil }
	workflowResumeSetForce = func(workflowControllerBuild) error { return nil }
	workflowResumeInstallHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	workflowResumeRun = func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		return workflowledger.RunSnapshot{}, errors.New("ledger read: database is locked")
	}

	err = executeWorkflowResume(run.RunID, root, filepath.Join(root, "config.toml"), false, false, false, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("executeWorkflowResume() error = %v, want the injected run failure", err)
	}
	after, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status after failed resume = %q, want failed: a run whose controller stopped must not stay running", after.Status)
	}
}

// TestExecuteWorkflowRunSettlesStorageFault: the CLI run path must settle a
// run whose controller stopped with a genuine (non-deadline) storage fault.
// Controller.Run self-settles deadline errors and cancel owns cancelled runs,
// but a raw ledger fault must reach the settle so the run does not stay
// running with no cause (DC-9, the 6c5419d class on the CLI run entry point).
func TestExecuteWorkflowRunSettlesStorageFault(t *testing.T) {
	root, _ := newForcedResumeFixture(t)
	ctx := context.Background()
	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("ledger read: database is locked")

	var runID string
	originalBuild := workflowRunBuild
	t.Cleanup(func() { workflowRunBuild = originalBuild })
	workflowRunBuild = func(_ string, _ *config.Resolved, _ *storage.SQLite, repo workflowledger.Repository, _ *compiler.CompiledWorkflow, _ string, _ map[string]any, _ map[string]string, _ []byte, id string, _ *workflowledger.Snapshot, _ *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
		runID = id
		fault := &failWhenRunningRepository{Repository: repo, err: sentinel}
		ctrl, err := controller.NewLinearController(fault, &workflowResumeJoinRunner{}, compiled, nil, map[string]any{"task": "test"}, id, rawSnapshot)
		if err != nil {
			return workflowControllerBuild{}, err
		}
		return workflowControllerBuild{
			Controller: ctrl,
			Dispatcher: workflowTestDispatcher{},
			Admission:  controller.Admission{InputDigest: workflowledger.InputDigest(snapshot.Inputs)},
		}, nil
	}

	err = executeWorkflowRun("two-step", root, filepath.Join(root, "config.toml"), []string{"task=test"}, false, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("executeWorkflowRun() error = %v, want the injected storage fault", err)
	}
	// executeWorkflowRun closes the store it opened before returning, so read
	// the settled run through a fresh repository over the same SQLite file.
	store, err := openContextStorePath(filepath.Join(root, "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	after, err := workflowledger.NewStorageRepository(store).GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status after storage fault = %q, want failed: a run whose controller stopped must not stay running", after.Status)
	}
}
