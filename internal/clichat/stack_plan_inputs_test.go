package clichat

// Pins a live e2e finding (2026-08-15): `mivia stack drive` failed on every
// invocation with 'chunk id "" must match ^[A-Za-z0-9._-]+$ for a stable
// admission key'. stackPlanInputs looked up the plan run via stackRunRef(
// repo, stackID, "") - the CHUNK-run admission-key lookup ("<stack>:<chunk>")
// - with an empty chunk id, which stackAdmissionKey correctly rejects: that
// key format is for chunk runs only. The plan run is never admitted with
// such a key; its own RunID IS the stack id (resolveStackID resolves a plan
// run by InvocationKey=="", and every chunk run's stable key embeds the
// plan run's RunID as the stack id - see stackAdmissionKey). stackPlanInputs
// must read the plan run directly by RunID, not through the chunk lookup.

import (
	"context"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestStackPlanInputsReadsThePlanRunDirectly(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	stackID := "wfr-plan-abc123"
	snap := workflowledger.RunSnapshot{
		RunID: stackID, WorkflowName: "feature-delivery", WorkflowDigest: "d1",
		Status: workflowledger.RunStatusPending,
	}
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1,
		Inputs:        map[string]string{"task": "add three packages"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}

	got, err := stackPlanInputs(repo, stackID)
	if err != nil {
		t.Fatalf("stackPlanInputs: %v", err)
	}
	if got["task"] != "add three packages" {
		t.Fatalf("stackPlanInputs = %#v, want the plan run's declared task input", got)
	}
}

func TestStackPlanInputsReportsNotFoundForAnUnknownStack(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	if _, err := stackPlanInputs(repo, "wfr-does-not-exist"); err == nil {
		t.Fatal("stackPlanInputs succeeded for an unknown stack id, want an error")
	}
}
