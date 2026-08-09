package cli

import (
	"errors"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestExecuteWorkflowApproveRunLookupFailure drives executeWorkflowApprove's
// before-snapshot read error path: the lock and stale-claim clear succeed for
// a run the ledger does not know, so the first GetRun returns ErrNotFound and
// the command aborts before any controller is built. No stdout/stderr output
// is produced.
func TestExecuteWorkflowApproveRunLookupFailure(t *testing.T) {
	root, configPath, _, _ := newGatedApprovalFixture(t)
	var stdout, stderr strings.Builder
	err := executeWorkflowApprove("wfr-approve-missing", "wfa-approval-review-1", root, configPath, "", &stdout, &stderr)
	if !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("executeWorkflowApprove() error = %v, want ErrNotFound from the before-snapshot read", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("approve on a missing run wrote output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestExecuteWorkflowRejectMissingRunFailsAtBeforeRead drives
// executeWorkflowReject on a run the ledger does not know. Mirroring
// executeWorkflowApprove, reject reads the before-snapshot BEFORE building the
// resolution controller, so the unknown run fails at the before-read with the
// plain not-found error, before any controller mutation or output happens.
func TestExecuteWorkflowRejectMissingRunFailsAtBeforeRead(t *testing.T) {
	root, configPath, _, _ := newGatedApprovalFixture(t)
	var stdout, stderr strings.Builder
	err := executeWorkflowReject("wfr-reject-missing", "wfa-approval-review-1", root, configPath, "", "not now", &stdout, &stderr)
	if !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("executeWorkflowReject() error = %v, want ErrNotFound from the before-snapshot read", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("reject on a missing run wrote output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestPublishCanceledAttemptsCLINilStderr covers publishCanceledAttemptsCLI's
// nil-writer guard: with stderr nil the helper must return before creating the
// progress writer, no matter how many attempts a cancel settled.
func TestPublishCanceledAttemptsCLINilStderr(t *testing.T) {
	publishCanceledAttemptsCLI("wfr-nil-stderr", []workflowledger.StepAttempt{
		{AttemptID: "att-1", RunID: "wfr-nil-stderr", StepID: "one", AttemptNo: 1, Status: workflowledger.AttemptStatusPending},
		{AttemptID: "att-2", RunID: "wfr-nil-stderr", StepID: "two", AttemptNo: 1, Status: workflowledger.AttemptStatusPending},
	}, nil)
}
