package cliworkflow

// Pins F11: `mivia stack drive` must settle the plan run's own
// delivery_pending status once the stack it drives finishes - `workflow
// deliver`, `resume`, and `cancel` all refuse a delivery_pending run (see
// workflow_deliver_stack_refuse_test.go and ClassifyStackPlanRunDeliveryFunc),
// and before this fix `runStackDrive` never called any settle path of its
// own, so a plain operator `mivia stack drive` invocation left the plan run
// parked forever even after driving every chunk and the integration run to
// completion.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestSettleStackPlanRunIfCompleteSettlesSkippedPublication proves that once
// a stack has driven to completion (every chunk merged, integration run
// settled) under a non-auto merge policy, SettleStackPlanRunIfCompleteFn
// settles the plan run succeeded WITHOUT publishing when the workflow keeps
// delivery.deliver_plan_run=false (the default) - the same skip-shape the
// session recovery sweep already handles, now reachable from a bare
// `mivia stack drive` invocation.
func TestSettleStackPlanRunIfCompleteSettlesSkippedPublication(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedGrantPolicyParkedStackingPlanRun(t, root, storePath, repo)
	completeParkedStackDrive(t, storePath, repo, planRunID)

	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_ = configPath

	prepared := &PreparedWorkflowRun{Root: root, Store: store, Repo: repo}
	var stdout bytes.Buffer
	if err := SettleStackPlanRunIfCompleteFn(context.Background(), prepared, planRunID, &stdout); err != nil {
		t.Fatalf("SettleStackPlanRunIfCompleteFn() error = %v", err)
	}

	run, err := repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("plan run status = %q, want succeeded (settled without publication)", run.Status)
	}
	if !strings.Contains(stdout.String(), "plan run settled") {
		t.Fatalf("stdout = %q, want the settle notice", stdout.String())
	}
}

// TestSettleStackPlanRunIfCompleteLeavesIncompleteStackParked proves the
// settle path does nothing (no error, no output, run stays delivery_pending)
// when the stack is seeded but has not driven to completion - the routine,
// non-error `stack drive` outcome under merge_policy=approve while chunks
// still await their publish grant.
func TestSettleStackPlanRunIfCompleteLeavesIncompleteStackParked(t *testing.T) {
	root, storePath, _, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedGrantPolicyParkedStackingPlanRun(t, root, storePath, repo)

	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	prepared := &PreparedWorkflowRun{Root: root, Store: store, Repo: repo}
	var stdout bytes.Buffer
	if err := SettleStackPlanRunIfCompleteFn(context.Background(), prepared, planRunID, &stdout); err != nil {
		t.Fatalf("SettleStackPlanRunIfCompleteFn() error = %v", err)
	}

	run, err := repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (incomplete stack must stay parked)", run.Status)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty (no settle action for an incomplete stack)", stdout.String())
	}
}
