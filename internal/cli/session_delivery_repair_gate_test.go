package cli

// TestSessionAutoDeliveryRepairLoopWithholdsApprovePolicyChunkPublish pins
// the session-loop half of the sweep/session publish gate (reachable-bug
// audit finding 1): the repair loop must not auto-publish a stack chunk or
// integration run under merge_policy=approve just because it hardcodes
// allowPublish=true for its own (non-stacking) delivery attempts. Before the
// fix, ANY resumed chunk run that settled delivery_pending here (e.g. an
// admitting process crashed mid-flight and the recovery sweep resumed the
// chunk run through this exact loop) was published regardless of the
// stack's declared merge policy.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestSessionAutoDeliveryRepairLoopWithholdsApprovePolicyChunkPublish(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := openContextStorePath(filepath.Join(root, "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := workflowledger.NewStorageRepository(store)

	toml := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "approve"`, 1)

	planRun, planRaw := stackingResumeSnapshot(t, toml, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-repair-gate-plan"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	chunkRun, chunkRaw := stackingResumeSnapshot(t, toml, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-repair-gate-chunk"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}
	settleRunToDeliveryPending(t, repo, chunkRun.RunID)

	res := &config.Resolved{}
	advanceCalls := 0
	advance := func(context.Context) (workflowledger.RunSnapshot, error) {
		advanceCalls++
		return repo.GetRun(ctx, chunkRun.RunID)
	}
	driveStack := func(context.Context) (bool, error) { return false, nil }

	sessionAutoDeliveryRepairLoop(ctx, repo, root, res, store, chunkRun.RunID, advance, driveStack, true)

	if advanceCalls != 1 {
		t.Fatalf("advance called %d times, want 1 (the loop must not re-advance after withholding publish)", advanceCalls)
	}
	final, err := repo.GetRun(ctx, chunkRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("chunk run status = %q, want delivery_pending (merge_policy=approve must withhold auto-publish)", final.Status)
	}
	key := delivery.DeliveryKey(chunkRun.RunID, final.WorkflowDigest)
	if _, err := repo.GetDeliveryByIdempotencyKey(ctx, key); err == nil {
		t.Fatal("chunk run has a delivery record, want none (cliworkflow.DeliverRunWithStore must not run)")
	}
}

// TestSessionAutoDeliveryRepairLoopAllowsAutoPolicyChunkPublish is the
// positive control: under merge_policy=auto the same loop MAY publish the
// chunk run, proving the gate distinguishes policy rather than blocking
// every stack run unconditionally. cliworkflow.DeliverRunWithStore is expected to fail
// in this hermetic fixture (no real git remote/worktree), which is fine -
// what this test pins is that the gate did not short-circuit the attempt.
func TestSessionAutoDeliveryRepairLoopAllowsAutoPolicyChunkPublish(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := openContextStorePath(filepath.Join(root, "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := workflowledger.NewStorageRepository(store)

	toml := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "auto"`, 1)

	planRun, planRaw := stackingResumeSnapshot(t, toml, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-repair-gate-plan-auto"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	chunkRun, chunkRaw := stackingResumeSnapshot(t, toml, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-repair-gate-chunk-auto"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}
	settleRunToDeliveryPending(t, repo, chunkRun.RunID)

	res := &config.Resolved{}
	advance := func(context.Context) (workflowledger.RunSnapshot, error) {
		return repo.GetRun(ctx, chunkRun.RunID)
	}
	driveStack := func(context.Context) (bool, error) { return false, nil }

	sessionAutoDeliveryRepairLoop(ctx, repo, root, res, store, chunkRun.RunID, advance, driveStack, true)

	final, err := repo.GetRun(ctx, chunkRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// The hermetic fixture has no real git remote, so delivery cannot
	// actually succeed - but it must have been ATTEMPTED (and failed) not
	// silently withheld the way the approve-policy case above withholds it.
	// cliworkflow.DeliverRunWithStore's workspace-resolution refusal is recorded as a
	// failed delivery attempt, which is exactly the "the gate let it
	// through" signal this test pins.
	key := delivery.DeliveryKey(chunkRun.RunID, final.WorkflowDigest)
	rec, delErr := repo.GetDeliveryByIdempotencyKey(ctx, key)
	if delErr != nil {
		t.Fatalf("auto policy chunk run has no delivery record; want a recorded attempt (the gate must have let cliworkflow.DeliverRunWithStore run): %v", delErr)
	}
	if rec.Status != "failed" {
		t.Fatalf("delivery record status = %q, want failed (the hermetic fixture has no real git remote)", rec.Status)
	}
}

// settleRunToDeliveryPending advances a freshly-created pending run to
// delivery_pending through the only valid edge (pending -> running ->
// delivery_pending); ValidRunTransition refuses a direct pending ->
// delivery_pending jump.
func settleRunToDeliveryPending(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	ctx := context.Background()
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
}
