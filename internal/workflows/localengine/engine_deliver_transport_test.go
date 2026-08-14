package localengine_test

// Engine parity with the CLI's transport-fault rule: a git network death is
// not a condition in the change, so the engine must not dispatch a repair
// agent for it either.

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// transportFaultGit fails every command with a git DNS-resolution fault.
type transportFaultGit struct{}

func (transportFaultGit) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	return "", errors.New("fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com")
}

// TestEngineDeliverTransportFaultStaysPending pins the engine twin of
// TestWorkflowDeliverTransportFaultStaysUnrecordedAndUnrouted: with a repair
// route available, a transport fault must leave the run delivery_pending and
// record no wf-delivery repair attempt - provider.IsTransient alone missed
// git's texts, so the fault was dispatched to the repair step.
func TestEngineDeliverTransportFaultStaysPending(t *testing.T) {
	repoRoot, _, run, repo := newSeededDeliveryFixtureTOML(t, deliverMeRepairTOML)
	seedEngineChangeSummary(t, repo, run.RunID, `{"pr_title": "feat(scope): add widget", "pr_summary": "Adds the widget."}`)
	engine := &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		PR:            &recordingPR{},
		Git:           transportFaultGit{},
	}

	res, err := engine.Deliver(context.Background(), run.RunID, true)
	if err == nil && res.Status != string(workflowledger.RunStatusDeliveryPending) {
		t.Fatalf("Engine.Deliver = (%+v, nil), want either the attempt error or a delivery_pending result", res)
	}
	fresh, ferr := repo.GetRun(context.Background(), run.RunID)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending: a transport fault is retryable, not repairable", fresh.Status)
	}
	attempts, aerr := repo.ListStepAttempts(context.Background(), run.RunID)
	if aerr != nil {
		t.Fatal(aerr)
	}
	for _, a := range attempts {
		if a.StepID == delivery.DeliveryRepairStepID {
			t.Fatalf("a wf-delivery repair attempt was recorded for a transport fault: %+v; no agent can repair a network death", a)
		}
	}
}
