package cliworkflow

// workflow_tool_engine_ops_coverage_test.go drives the small pure
// helpers in workflow_tool_engine_ops.go that the broader engine_*_test.go
// files do not exercise individually.

import (
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestResolveSessionCancelRefusal(t *testing.T) {
	// Terminal statuses short-circuit to a successful CancelResult.
	res, err, ok := resolveSessionCancelRefusal("run-x", workflowledger.RunStatusSucceeded)
	if !ok || err != nil {
		t.Fatalf("resolveSessionCancelRefusal(completed) = (%+v, %v, %v)", res, err, ok)
	}
	if res.Status != string(workflowledger.RunStatusSucceeded) {
		t.Errorf("res.Status = %q, want %q", res.Status, workflowledger.RunStatusSucceeded)
	}
	// Delivery-pending must surface a refusal error.
	_, err, ok = resolveSessionCancelRefusal("run-y", workflowledger.RunStatusDeliveryPending)
	if !ok || err == nil {
		t.Fatalf("delivery-pending must refuse; err=%v ok=%v", err, ok)
	}
	if !strings.Contains(err.Error(), "delivery") {
		t.Errorf("delivery-pending err = %v, want \"delivery\"", err)
	}
	// Non-terminal statuses return (zero, nil, false) so the caller
	// falls through to the real cancel path.
	res, err, ok = resolveSessionCancelRefusal("run-z", workflowledger.RunStatusRunning)
	if ok || err != nil || res.RunID != "" {
		t.Errorf("running = (%+v, %v, %v), want zero + false", res, err, ok)
	}
}

func TestWorkflowHolderMinters(t *testing.T) {
	// Each mint must produce a unique, prefixed string.
	holders := map[string]int{}
	for i := 0; i < 8; i++ {
		c := newWorkflowCancelHolder()
		if !strings.HasPrefix(c, "wfcancel-") {
			t.Errorf("cancel holder missing prefix: %q", c)
		}
		holders[c]++
		d := newWorkflowDeleteHolder()
		if !strings.HasPrefix(d, "wfdelete-") {
			t.Errorf("delete holder missing prefix: %q", d)
		}
		holders[d]++
	}
	for h, n := range holders {
		if n > 1 {
			t.Errorf("holder %q generated %d times; expected unique", h, n)
		}
	}
}
