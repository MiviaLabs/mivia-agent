package cliworkflow

// workflow_tool_engine_coverage_pass2_test.go covers the remaining uncovered
// statement lines in workflow_tool_engine.go and workflow_tool_engine_ops.go:
// the Start/Cancel/Delete/Deliver early refusal branches, the keyedRunID
// resume and terminal-result branches, and the buildAndStart failure paths
// reached through direct in-package calls and the WorkflowRunBuild /
// WorkflowRunSetAdmission seams.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// newEngineCoveragePrepared builds a two-step prepared run for direct
// buildAndStart calls. The prepared run owns an open store, so each test gets
// a fresh one: buildAndStart consumes prepared ownership on every path.
func newEngineCoveragePrepared(t *testing.T) (*sessionWorkflowEngine, *PreparedWorkflowRun) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	configPath := filepath.Join(root, "config.toml")
	prepared, err := PrepareWorkflowRun("two-step", root, configPath, []string{"task=x"})
	if err != nil {
		t.Fatalf("PrepareWorkflowRun() error = %v", err)
	}
	return NewSessionWorkflowEngine(root, configPath), prepared
}

// TestSessionEngineStartNilReceiverRefused covers the nil-engine guard in
// Start: a nil engine must fail closed with an explicit error.
func TestSessionEngineStartNilReceiverRefused(t *testing.T) {
	var e *sessionWorkflowEngine
	_, err := e.Start(context.Background(), workflowledger.StartRequest{Workflow: "two-step"})
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("Start() on nil engine error = %v, want a nil-engine error", err)
	}
}

// TestSessionEngineStartUnencodableInputRefused covers the inputsToRawFlags
// failure branch in startCLI: an input value JSON cannot encode must surface
// the encode error before any admission work starts.
func TestSessionEngineStartUnencodableInputRefused(t *testing.T) {
	e := NewSessionWorkflowEngine(t.TempDir(), "")
	_, err := e.Start(context.Background(), workflowledger.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": make(chan int)},
	})
	if err == nil || !strings.Contains(err.Error(), "encode JSON") {
		t.Fatalf("Start() error = %v, want a JSON encode error", err)
	}
}

// TestSessionEngineStartMissingWorkspaceRefused covers the PrepareWorkflowRun
// failure branch in startCLI: an unopenable workspace root must fail the
// start with the workspace error.
func TestSessionEngineStartMissingWorkspaceRefused(t *testing.T) {
	e := NewSessionWorkflowEngine(filepath.Join(t.TempDir(), "absent"), "")
	_, err := e.Start(context.Background(), workflowledger.StartRequest{
		Workflow: "two-step",
		Inputs:   map[string]any{"task": "x"},
	})
	if err == nil {
		t.Fatal("Start() on a missing workspace succeeded; want the prepare error")
	}
}

// TestSessionEngineStartKeyOnTerminalRunReturnsStored covers the
// keyedRunID terminal branch: a retry under a key bound to an already
// terminal run must return the stored terminal result without launching a
// second run.
func TestSessionEngineStartKeyOnTerminalRunReturnsStored(t *testing.T) {
	root, configPath := newTwoWorkflowFixture(t)
	e := NewSessionWorkflowEngine(root, configPath)
	key := "cov-terminal-key-1"
	ctx := context.Background()

	first, err := e.Start(ctx, workflowledger.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "compile"}, InvocationKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionEngineIdle(t, e, first.RunID)

	second, err := e.Start(ctx, workflowledger.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "compile"}, InvocationKey: key,
	})
	if err != nil {
		t.Fatalf("retry under a terminal key error = %v, want the stored result", err)
	}
	if second.RunID != string(workflowledger.InvocationRunID(key)) {
		t.Fatalf("retry RunID = %q, want the keyed run id", second.RunID)
	}
	if second.Status != string(workflowledger.RunStatusSucceeded) {
		t.Fatalf("retry status = %q, want succeeded from the stored run", second.Status)
	}
}

// TestSessionEngineStartKeyMatchingInputsResumes covers the keyedRunID
// silent-resume branch: a retry under a key bound to a non-terminal run with
// the SAME inputs must resume that run, not be refused as an input mismatch
// and not start a second run.
func TestSessionEngineStartKeyMatchingInputsResumes(t *testing.T) {
	key := "cov-matching-inputs-key-1"
	root, configPath := newGatedKeyFixture(t, key)
	e := NewSessionWorkflowEngine(root, configPath)

	result, err := e.Start(context.Background(), workflowledger.StartRequest{
		Workflow: "gated", Inputs: map[string]any{"task": "test"}, InvocationKey: key,
	})
	if err != nil && strings.Contains(err.Error(), "different inputs") {
		t.Fatalf("matching inputs refused as different: %v", err)
	}
	if err == nil {
		if result.RunID != string(workflowledger.InvocationRunID(key)) {
			t.Fatalf("resume RunID = %q, want the keyed run id", result.RunID)
		}
		waitForSessionEngineIdle(t, e, result.RunID)
	}
}

// TestBuildAndStartExecutionLockHeldFails covers the BeginWorkflowExecution
// failure path in buildAndStart: a second admission for a run whose execution
// flock is already held must settle its resources and return the lock error.
func TestBuildAndStartExecutionLockHeldFails(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	runID := "wfr-cov-lock-held"
	release, err := BeginWorkflowExecution(prepared.Root, ContextStorePath(prepared.Root, prepared.Res.Subagents), runID)
	if err != nil {
		t.Fatalf("BeginWorkflowExecution() error = %v", err)
	}
	defer release()

	_, err = e.buildAndStart(context.Background(), prepared, workflowledger.StartRequest{Workflow: "two-step"}, runID, "", nil)
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("buildAndStart() under a held lock error = %v, want a lock error", err)
	}
}

// TestBuildAndStartBuildFailureSettles covers the WorkflowRunBuild failure
// path in buildAndStart through the seam: a build error must release the
// execution lock and the prepared store before returning.
func TestBuildAndStartBuildFailureSettles(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	prev := WorkflowRunBuild
	WorkflowRunBuild = func(_ string, _ *config.Resolved, _ *storage.SQLite, _ workflowledger.Repository, _ *definition.CompiledWorkflow, _ string, _ map[string]any, _ map[string]string, _ []byte, _ string, _ *workflowledger.Snapshot, _ []byte, _ *workflowledger.RunSnapshot, _ map[string]bool, _ *skills.Registry, _ string, _ ledger.LedgerRepository) (WorkflowControllerBuild, error) {
		return WorkflowControllerBuild{}, errors.New("coverage build failure")
	}
	defer func() { WorkflowRunBuild = prev }()

	_, err := e.buildAndStart(context.Background(), prepared, workflowledger.StartRequest{Workflow: "two-step"}, "wfr-cov-build-fail", "", nil)
	if err == nil || !strings.Contains(err.Error(), "coverage build failure") {
		t.Fatalf("buildAndStart() error = %v, want the seam error", err)
	}
}

// TestBuildAndStartAdmissionFailureSettles covers the WorkflowRunSetAdmission
// failure path in buildAndStart through the seam: an admission error must
// clean up the built controller and dispatcher before returning.
func TestBuildAndStartAdmissionFailureSettles(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	prev := WorkflowRunSetAdmission
	WorkflowRunSetAdmission = func(_ WorkflowControllerBuild) error {
		return errors.New("coverage admission failure")
	}
	defer func() { WorkflowRunSetAdmission = prev }()

	_, err := e.buildAndStart(context.Background(), prepared, workflowledger.StartRequest{Workflow: "two-step"}, "wfr-cov-admission-fail", "", nil)
	if err == nil || !strings.Contains(err.Error(), "coverage admission failure") {
		t.Fatalf("buildAndStart() error = %v, want the seam error", err)
	}
}

// TestBuildAndStartUnadmittableRunIDFails covers the StartNew error path in
// buildAndStart: a run ID the ledger refuses to admit (CreateRun rejects IDs
// without the wfr- prefix) must fail after full resource cleanup.
func TestBuildAndStartUnadmittableRunIDFails(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	_, err := e.buildAndStart(context.Background(), prepared, workflowledger.StartRequest{Workflow: "two-step"}, "cov-not-a-run-id", "", nil)
	if err == nil {
		t.Fatal("buildAndStart() with an unadmittable run ID succeeded; want the admission error")
	}
}

// TestBuildAndStartExistingRunReturnsStoredStatus covers the created=false
// path in buildAndStart: when the run was already admitted with the same
// snapshot, StartNew reports created=false and the engine must return the
// stored status without launching a second controller.
func TestBuildAndStartExistingRunReturnsStoredStatus(t *testing.T) {
	e, prepared := newEngineCoveragePrepared(t)
	runID := "wfr-cov-existing"
	ctx := context.Background()

	// Admit the run once through the same build the engine uses, then drop
	// that controller without running it.
	built, err := WorkflowRunBuild(prepared.Root, prepared.Res, prepared.Store, prepared.Repo, prepared.Compiled, prepared.RefBase, prepared.Inputs, prepared.InputSnapshot, prepared.Raw, runID, nil, nil, nil, nil, nil, "", nil)
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

	result, err := e.buildAndStart(ctx, prepared, workflowledger.StartRequest{Workflow: "two-step"}, runID, "", nil)
	if err != nil {
		t.Fatalf("buildAndStart() on an existing run error = %v, want the stored result", err)
	}
	if result.RunID != runID {
		t.Fatalf("result RunID = %q, want %q", result.RunID, runID)
	}
	if result.Status != string(workflowledger.RunStatusPending) {
		t.Fatalf("result status = %q, want pending from the stored run", result.Status)
	}
}

// TestSessionEngineCancelGuardBranches covers the nil-engine and empty run_id
// guards in Cancel: both must fail closed without touching any store.
func TestSessionEngineCancelGuardBranches(t *testing.T) {
	var nilEngine *sessionWorkflowEngine
	if _, err := nilEngine.Cancel(context.Background(), "wfr-x"); err == nil {
		t.Fatal("Cancel() on nil engine succeeded; want an error")
	}
	e := NewSessionWorkflowEngine(t.TempDir(), "")
	if _, err := e.Cancel(context.Background(), "   "); err == nil {
		t.Fatal("Cancel() with a blank run_id succeeded; want an error")
	}
}

// TestSessionEngineCancelMissingWorkspaceRefused covers the
// openWorkflowResolutionContextBounded failure branch in Cancel: an
// unopenable workspace root must surface the open error.
func TestSessionEngineCancelMissingWorkspaceRefused(t *testing.T) {
	e := NewSessionWorkflowEngine(filepath.Join(t.TempDir(), "absent"), "")
	_, err := e.Cancel(context.Background(), "wfr-cov-missing-workspace")
	if err == nil {
		t.Fatal("Cancel() on a missing workspace succeeded; want the open error")
	}
}

// TestSessionEngineCancelUnknownRunReportsLedgerError covers the
// lock-protected GetRun failure in Cancel: a run ID the ledger does not know
// must surface the ledger error, not a silent success.
func TestSessionEngineCancelUnknownRunReportsLedgerError(t *testing.T) {
	root, configPath, _, _ := newGatedApprovalFixture(t)
	e := NewSessionWorkflowEngine(root, configPath)
	_, err := e.Cancel(context.Background(), "wfr-cov-unknown-run")
	if err == nil {
		t.Fatal("Cancel() of an unknown run succeeded; want the ledger error")
	}
}

// TestSessionEngineDeleteGuardAndFailureBranches covers the nil-engine and
// blank run_id guards, the resolution-context open failure, and the unknown
// run read failure in Delete.
func TestSessionEngineDeleteGuardAndFailureBranches(t *testing.T) {
	var nilEngine *sessionWorkflowEngine
	if _, err := nilEngine.Delete(context.Background(), "wfr-x", false); err == nil {
		t.Fatal("Delete() on nil engine succeeded; want an error")
	}
	e := NewSessionWorkflowEngine(t.TempDir(), "")
	if _, err := e.Delete(context.Background(), "", false); err == nil {
		t.Fatal("Delete() with a blank run_id succeeded; want an error")
	}
	missing := NewSessionWorkflowEngine(filepath.Join(t.TempDir(), "absent"), "")
	if _, err := missing.Delete(context.Background(), "wfr-cov-missing-workspace", false); err == nil {
		t.Fatal("Delete() on a missing workspace succeeded; want the open error")
	}
	root, configPath, _, _ := newGatedApprovalFixture(t)
	fixture := NewSessionWorkflowEngine(root, configPath)
	if _, err := fixture.Delete(context.Background(), "wfr-cov-unknown-run", false); err == nil {
		t.Fatal("Delete() of an unknown run succeeded; want the ledger error")
	}
}

// TestSessionEngineDeliverGuardBranches covers the nil-engine and blank
// run_id guards in Deliver.
func TestSessionEngineDeliverGuardBranches(t *testing.T) {
	var nilEngine *sessionWorkflowEngine
	if _, err := nilEngine.Deliver(context.Background(), "wfr-x", true); err == nil {
		t.Fatal("Deliver() on nil engine succeeded; want an error")
	}
	e := NewSessionWorkflowEngine(t.TempDir(), "")
	if _, err := e.Deliver(context.Background(), "  ", true); err == nil {
		t.Fatal("Deliver() with a blank run_id succeeded; want an error")
	}
}

// TestSessionDeliverResultFromLedgerBranches covers both false branches of
// sessionDeliverResultFromLedger: an unopenable ledger reports no structured
// result, and a run not settled delivery_failed likewise maps to no
// structured result.
func TestSessionDeliverResultFromLedgerBranches(t *testing.T) {
	ctx := context.Background()
	deliverErr := errors.New("host refused delivery")

	if _, ok := sessionDeliverResultFromLedger(ctx, filepath.Join(t.TempDir(), "absent"), "", "wfr-cov-ledger-missing", deliverErr); ok {
		t.Fatal("sessionDeliverResultFromLedger() on a missing workspace reported a structured result")
	}

	root, configPath, _, runID := newGatedApprovalFixture(t)
	if _, ok := sessionDeliverResultFromLedger(ctx, root, configPath, runID, deliverErr); ok {
		t.Fatalf("sessionDeliverResultFromLedger() on run %s (not delivery_failed) reported a structured result", runID)
	}
}
