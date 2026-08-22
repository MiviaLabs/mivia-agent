package cliworkflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestEmitCLIRunTerminalProgress verifies the operator-command terminal
// progress helper: it writes one run_finished JSON line only when the command
// TRANSITIONS the run from a non-terminal status to a terminal status. An
// idempotent command on an already-terminal run emits nothing (the settlement
// happened elsewhere; a second event would be a duplicate). A nil writer is
// tolerated.
func TestEmitCLIRunTerminalProgress(t *testing.T) {
	var stderr strings.Builder
	emitCLIRunTerminalProgress("wfr-tp", workflowledger.RunStatusDeliveryPending, workflowledger.RunStatusSucceeded, &stderr)
	if !strings.Contains(stderr.String(), "run_finished") || !strings.Contains(stderr.String(), "succeeded") {
		t.Fatalf("stderr = %q, want a run_finished line with status succeeded", stderr.String())
	}
	var event controller.ProgressEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr.String())), &event); err != nil {
		t.Fatalf("stderr line is not valid progress JSON: %v", err)
	}
	if event.Kind != controller.ProgressRunFinished || event.RunID != "wfr-tp" || event.Detail != "succeeded" {
		t.Fatalf("progress event = %+v, want run_finished wfr-tp succeeded", event)
	}

	// Idempotent no-op: before is already terminal, settled is unchanged.
	stderr.Reset()
	emitCLIRunTerminalProgress("wfr-tp", workflowledger.RunStatusSucceeded, workflowledger.RunStatusSucceeded, &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("idempotent command on a terminal run wrote progress: %q", stderr.String())
	}

	// Non-terminal settled status (approve resumed a running run): no output.
	stderr.Reset()
	emitCLIRunTerminalProgress("wfr-tp", workflowledger.RunStatusWaitingApproval, workflowledger.RunStatusRunning, &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("non-terminal settled status wrote progress: %q", stderr.String())
	}

	emitCLIRunTerminalProgress("wfr-tp", workflowledger.RunStatusRunning, workflowledger.RunStatusFailed, nil)
}
