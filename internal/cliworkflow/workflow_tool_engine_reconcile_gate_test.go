package cliworkflow

// TestReconcileParkedDeliveryWithholdsApprovePolicyChunkPublish pins the
// recovery-sweep half of the sweep/session publish gate (reachable-bug audit
// finding 1): reconcileParkedDelivery must not auto-publish a stack chunk or
// integration run parked at delivery_pending under merge_policy=approve.
// Before the fix, SkipParkedPlanRunPublication's ReadBackPlan lookup is keyed
// by the PLAN run's id, never a chunk run's, so it always returns false for a
// chunk run and execution fell straight through to
// DeliverRunWithStore(..., allowPublish=true, ...) - publishing the chunk
// regardless of the stack's declared merge policy. Reachable whenever a
// chunk run's admitting process dies mid-flight (or waiting_approval) and a
// later resume settles it at delivery_pending: the next sweep tick picks it
// up here.

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestReconcileParkedDeliveryWithholdsApprovePolicyChunkPublish(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := workflowledger.NewStorageRepository(store)

	toml := strings.Replace(miniStackWorkflowTOML, `merge_policy = "auto"`, `merge_policy = "approve"`, 1)

	planRun, planRaw := stackingResumeSnapshot(t, toml, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-sweep-gate-plan"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	chunkRun, chunkRaw := stackingResumeSnapshot(t, toml, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-sweep-gate-chunk"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}
	settleRunToDeliveryPending(t, repo, chunkRun.RunID)

	res := &config.Resolved{}
	e := NewSessionWorkflowEngine(root, "")

	// The hermetic fixture's DeliverRunWithStore call always fails (no real
	// git remote), so a bare "still delivery_pending, no delivery record"
	// assertion cannot distinguish the gate blocking the attempt from
	// DeliverRunWithStore itself failing early - both produce that exact
	// shape. Capture the log instead: it must show the gate's own withheld
	// message and must NOT show DeliverRunWithStore's failure line, proving
	// the function was never called at all.
	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	e.reconcileParkedDelivery(ctx, root, res, store, repo, storePath, chunkRun.RunID, false)
	log.SetOutput(original)

	if strings.Contains(buf.String(), "deliver "+chunkRun.RunID+" failed") {
		t.Fatalf("sweep log = %q, want no deliver-attempt line (merge_policy=approve must withhold the attempt, not just fail it another way)", buf.String())
	}
	if !strings.Contains(buf.String(), "awaiting a human publish grant") {
		t.Fatalf("sweep log = %q, want the withheld-for-grant message", buf.String())
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
		t.Fatal("chunk run has a delivery record, want none (DeliverRunWithStore must not run)")
	}
}

// TestReconcileParkedDeliveryAllowsAutoPolicyChunkPublish is the positive
// control: under merge_policy=auto the sweep MAY attempt to publish the
// parked chunk run, proving the gate distinguishes policy rather than
// blocking every stack run unconditionally.
func TestReconcileParkedDeliveryAllowsAutoPolicyChunkPublish(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := workflowledger.NewStorageRepository(store)

	planRun, planRaw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{"task": "add feature"})
	planRun.RunID = "wfr-sweep-gate-plan-auto"
	planRun.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(ctx, planRun, planRaw); err != nil {
		t.Fatal(err)
	}

	chunkRun, chunkRaw := stackingResumeSnapshot(t, miniStackWorkflowTOML, map[string]string{
		"task": "add feature", "stack_mode": "chunk", "chunk": "c1", "stack_part": "1/1",
	})
	chunkRun.RunID = "wfr-sweep-gate-chunk-auto"
	chunkRun.Status = workflowledger.RunStatusPending
	chunkRun.InvocationKey = planRun.RunID + ":c1"
	if err := repo.CreateRun(ctx, chunkRun, chunkRaw); err != nil {
		t.Fatal(err)
	}
	settleRunToDeliveryPending(t, repo, chunkRun.RunID)

	res := &config.Resolved{}
	e := NewSessionWorkflowEngine(root, "")

	// The hermetic fixture has no real git remote, so DeliverRunWithStore
	// cannot actually succeed - but under merge_policy=auto the gate must
	// let the ATTEMPT through, unlike the withheld case above. Capture the
	// sweep's log output (quiet=false) to prove DeliverRunWithStore ran,
	// since a workspace-resolution refusal this early never reaches a
	// delivery record.
	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	e.reconcileParkedDelivery(ctx, root, res, store, repo, storePath, chunkRun.RunID, false)
	log.SetOutput(original)

	if !strings.Contains(buf.String(), "deliver "+chunkRun.RunID+" failed") {
		t.Fatalf("sweep log = %q, want a deliver-attempt failure line (the gate must have let DeliverRunWithStore run under merge_policy=auto)", buf.String())
	}
}
