package localengine_test

// Delivery-attempt transport faults: the attempt's own bound firing is not a
// condition in the change, so the run must stay retryable.

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// TestEngineDeliverDeadlineStaysDeliveryPending pins the engine-side twin
// of the CLI's settleDeliveryError guard: when the delivery attempt's own
// bound fires (a hung git push or gh call hit DeliveryTimeout), the failure
// is a transport fault, not a condition in the change - no agent can repair
// it - so the run must stay delivery_pending (retryable) and no wf-delivery
// repair attempt may be recorded. The engine routed the deadline error to
// the repair step instead, burning one repair cycle per timeout.
func TestEngineDeliverDeadlineStaysDeliveryPending(t *testing.T) {
	repoRoot, _, run, repo := newSeededDeliveryFixtureTOML(t, deliverMeRepairTOML)
	seedEngineChangeSummary(t, repo, run.RunID, `{"pr_title": "feat(scope): add widget", "pr_summary": "Adds the widget."}`)
	engine := &localengine.Engine{
		WorkspaceRoot:   repoRoot,
		Repo:            repo,
		PR:              &recordingPR{},
		DeliveryTimeout: time.Nanosecond,
	}

	res, err := engine.Deliver(context.Background(), run.RunID, true)
	if err == nil && res.Status != string(workflowledger.RunStatusDeliveryPending) {
		t.Fatalf("Engine.Deliver = (%+v, nil), want either the attempt error or a delivery_pending result", res)
	}
	fresh, err := repo.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending: a timed-out attempt is retryable, not repairable", fresh.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.StepID == delivery.DeliveryRepairStepID {
			t.Fatalf("a wf-delivery repair attempt was recorded for a timed-out delivery: %+v; no agent can repair a transport fault", a)
		}
	}
}
