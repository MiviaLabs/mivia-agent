package cli

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestSettleStackPlanRunFailed pins that a delivery_pending stacking plan run
// can be CAS-settled to failed, that the version is bumped, and that a second
// call is idempotent and does not re-log the cause.
func TestSettleStackPlanRunFailed(t *testing.T) {
	root, storePath, _, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedGrantPolicyParkedStackingPlanRun(t, root, storePath, repo)

	ctx := context.Background()
	before, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("seeded status = %q, want delivery_pending", before.Status)
	}

	var buf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	t.Cleanup(func() { log.SetOutput(prevOutput); log.SetFlags(prevFlags) })
	log.SetOutput(&buf)
	log.SetFlags(0)

	cause := "terminal failure: uncompletable stack"
	if err := settleStackPlanRunFailed(ctx, repo, planRunID, cause); err != nil {
		t.Fatalf("settleStackPlanRunFailed() error = %v", err)
	}

	after, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != workflowledger.RunStatusFailed {
		t.Fatalf("after settle status = %q, want failed", after.Status)
	}
	if after.Version != before.Version+1 {
		t.Fatalf("after settle version = %d, want %d", after.Version, before.Version+1)
	}
	wantLog := "workflow: plan run " + planRunID + " failed: " + cause
	if !strings.Contains(buf.String(), wantLog) {
		t.Fatalf("log = %q, want %q", buf.String(), wantLog)
	}

	if err := settleStackPlanRunFailed(ctx, repo, planRunID, cause); err != nil {
		t.Fatalf("second settleStackPlanRunFailed() error = %v", err)
	}

	repeat, err := repo.GetRun(ctx, planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Status != workflowledger.RunStatusFailed {
		t.Fatalf("repeat status = %q, want failed", repeat.Status)
	}
	if repeat.Version != after.Version {
		t.Fatalf("repeat version = %d, want %d (must not bump)", repeat.Version, after.Version)
	}
	if strings.Count(buf.String(), wantLog) != 1 {
		t.Fatalf("cause logged %d times, want 1; log = %q", strings.Count(buf.String(), wantLog), buf.String())
	}
}
