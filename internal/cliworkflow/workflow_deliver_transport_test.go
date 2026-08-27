package cliworkflow

// Transport-fault classification at the CLI settle point: a git network
// death must not dispatch a repair agent or write a failure record.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// transportFaultGitRunner fails every git command with a DNS-resolution
// transport fault, the text a `git fetch` against an unreachable network
// produces.
type transportFaultGitRunner struct{ delivery.GitRunner }

func (transportFaultGitRunner) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	return "", errors.New("fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com")
}

// TestWorkflowDeliverTransportFaultStaysUnrecordedAndUnrouted pins the
// transport-fault classification at the settle point: provider.IsTransient
// knows only provider/HTTP phrases, so a git DNS failure on a workflow WITH
// an on_failure route was misread as a repairable rejection - a repair agent
// was dispatched (and, without a route, a failed record written) for a fault
// no agent can fix. A transport fault must leave the run delivery_pending
// with no repair attempt and no failure record.
func TestWorkflowDeliverTransportFaultStaysUnrecordedAndUnrouted(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	appendWorkflowDeliveryOnFailure(t, root, "one")
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	originalGit := WorkflowDeliverGit
	WorkflowDeliverGit = transportFaultGitRunner{}
	t.Cleanup(func() { WorkflowDeliverGit = originalGit })

	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, io.Discard, io.Discard)
	if err != nil && !strings.Contains(err.Error(), "Could not resolve host") {
		t.Fatalf("deliver error = %v, want the transport fault to surface", err)
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending: a transport fault is retryable, not a condition in the change", run.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.StepID == delivery.DeliveryRepairStepID {
			t.Fatalf("a wf-delivery repair attempt was recorded for a transport fault: %+v; no agent can repair a network death", a)
		}
	}
	if _, gerr := repo.GetDeliveryByIdempotencyKey(context.Background(), delivery.DeliveryKey(runID, run.WorkflowDigest)); gerr == nil {
		t.Fatal("a failed delivery record was written for a transport fault; the noise would surface in workflow status until a later success overwrote it")
	}
}
