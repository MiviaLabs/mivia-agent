package cli

// Pins F11's second bullet: `mivia stack plan` misdiagnosed a multi-chunk
// plan's designed delivery_pending pause (merge_policy != auto,
// errStackAwaitsGrant) as a plan failure ("did not succeed... fix the plan
// and re-run"). stackPlanOutcomeLine is the pure decision runStackPlan
// prints from; testing it directly avoids driving a full stacking workflow
// through executeWorkflowRun just to observe the status-line branch.

import (
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestStackPlanOutcomeLineSucceeded(t *testing.T) {
	line, err := stackPlanOutcomeLine("wfr-1", string(workflowledger.RunStatusSucceeded))
	if err != nil {
		t.Fatalf("stackPlanOutcomeLine() error = %v, want nil", err)
	}
	if !strings.Contains(line, "status=succeeded") {
		t.Fatalf("line = %q, want status=succeeded", line)
	}
}

// TestStackPlanOutcomeLineDeliveryPendingIsNotAFailure pins F11: a
// multi-chunk plan pausing at delivery_pending under a non-auto merge policy
// is the designed outcome, not a plan defect - it must not error.
func TestStackPlanOutcomeLineDeliveryPendingIsNotAFailure(t *testing.T) {
	line, err := stackPlanOutcomeLine("wfr-1", string(workflowledger.RunStatusDeliveryPending))
	if err != nil {
		t.Fatalf("stackPlanOutcomeLine() error = %v, want nil (delivery_pending is the designed approve-policy pause)", err)
	}
	if !strings.Contains(line, "status=delivery_pending") || !strings.Contains(line, "awaiting first drive") {
		t.Fatalf("line = %q, want status=delivery_pending and an awaiting-drive note", line)
	}
}

func TestStackPlanOutcomeLineOtherStatusIsAFailure(t *testing.T) {
	_, err := stackPlanOutcomeLine("wfr-1", string(workflowledger.RunStatusFailed))
	if err == nil {
		t.Fatal("stackPlanOutcomeLine() error = nil, want a refusal for a genuinely failed plan run")
	}
	if !strings.Contains(err.Error(), "did not succeed") {
		t.Fatalf("error = %q, want the plan-failure message", err)
	}
}
