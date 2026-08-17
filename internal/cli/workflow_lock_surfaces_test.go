package cli

import (
	"io"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestWorkflowApproveRejectDeleteCleanupResumeUseBoundedLock pins that the
// five mutating operator CLI surfaces (approve, reject, delete, cleanup,
// resume) acquire the per-run execution flock with the bounded primitive:
// while a concurrent holder holds the flock, each surface fails with the
// explained "still held after" error instead of the plain lock's opaque
// "lock is busy" failure; after the holder releases, each surface proceeds to
// its normal behavior (resume is only pinned at the held-lock level: a full
// resume on the parked fixture re-drives the controller, which is heavy and
// would attempt agent execution).
func TestWorkflowApproveRejectDeleteCleanupResumeUseBoundedLock(t *testing.T) {
	shortenWorkflowResolutionLockWait(t)
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	hold, err := acquireWorkflowExecutionLock(storePath, runID)
	if err != nil {
		t.Fatal(err)
	}

	assertBoundedHeldError := func(surface string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s under a held execution lock succeeded; want the bounded 'still held after' refusal", surface)
		}
		if !strings.Contains(err.Error(), "still held after") {
			t.Fatalf("%s under a held execution lock error = %v; want the bounded 'still held after' error (the plain acquire yields the opaque 'lock is busy')", surface, err)
		}
	}

	assertBoundedHeldError("approve", executeWorkflowApprove(runID, "wfa-approval-review-1", root, configPath, "", io.Discard, io.Discard))
	assertBoundedHeldError("reject", executeWorkflowReject(runID, "wfa-approval-review-1", root, configPath, "", "not now", io.Discard, io.Discard))
	assertBoundedHeldError("delete", executeWorkflowDelete(runID, root, configPath, false, io.Discard, io.Discard))
	assertBoundedHeldError("cleanup", executeWorkflowCleanup(runID, root, configPath, io.Discard, io.Discard))
	assertBoundedHeldError("resume", executeWorkflowResume(runID, root, configPath, false, false, false, false, io.Discard, io.Discard))

	// Every refusal happened at the lock: the parked run is untouched.
	repo := openWorkflowTestStore(t, storePath)
	run, getErr := repo.GetRun(t.Context(), runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run status = %q after refused surfaces, want waiting_approval", run.Status)
	}

	// The holder releases; each surface now proceeds to its normal behavior.
	hold()

	// approve: settles the parked run to succeeded.
	var approveOut strings.Builder
	if err := executeWorkflowApprove(runID, "wfa-approval-review-1", root, configPath, workflowApprovalDefaultActor, &approveOut, io.Discard); err != nil {
		t.Fatalf("approve after lock release: %v", err)
	}
	if !strings.Contains(approveOut.String(), "status=succeeded") {
		t.Fatalf("approve output = %q, want status=succeeded", approveOut.String())
	}

	// reject: a fresh parked run settles to failed.
	root2, configPath2, _, runID2 := newGatedApprovalFixture(t)
	var rejectOut strings.Builder
	if err := executeWorkflowReject(runID2, "wfa-approval-review-1", root2, configPath2, "alice", "not now", &rejectOut, io.Discard); err != nil {
		t.Fatalf("reject after lock release: %v", err)
	}
	if !strings.Contains(rejectOut.String(), "status=failed") {
		t.Fatalf("reject output = %q, want status=failed", rejectOut.String())
	}

	// delete: --force purges the non-terminal waiting_approval run.
	root3, configPath3, _, runID3 := newGatedApprovalFixture(t)
	var deleteOut strings.Builder
	if err := executeWorkflowDelete(runID3, root3, configPath3, true, &deleteOut, io.Discard); err != nil {
		t.Fatalf("delete --force after lock release: %v", err)
	}
	if !strings.Contains(deleteOut.String(), "deleted=true") {
		t.Fatalf("delete output = %q, want deleted=true", deleteOut.String())
	}

	// cleanup: the lock is acquired first, so reaching the finished-run gate
	// proves the surface works after release (the parked run is non-terminal).
	root4, configPath4, _, runID4 := newGatedApprovalFixture(t)
	err = executeWorkflowCleanup(runID4, root4, configPath4, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cleanup requires a finished run") {
		t.Fatalf("cleanup after lock release = %v, want the finished-run refusal", err)
	}

	// resume: marked — a full resume on the parked fixture re-drives the
	// controller (agent execution/network), so only the held-lock bounded
	// acquire above is pinned for this surface.
}
