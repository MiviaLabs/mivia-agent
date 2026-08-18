package cli

// Pins a live e2e finding (dag-v3 chunks c1/c2, stack-v8 chunk c3, all on
// 2026-08-15): deliverRunWithStore returns nil both when a chunk's PR
// actually published (the run settles RunStatusSucceeded) AND when a
// rejected delivery re-entered the repair loop (ReopenForRepair CASes the
// run back to RunStatusRunning and returns nil - see its doc comment:
// "continue with: mivia workflow resume <runID>"). driveChunk treated any
// nil error as a real publish, unconditionally printing "published; merge
// queue will merge" and marking the chunk stackStatusPublished - so the
// stack ledger permanently lied about a chunk that had no PR at all, the
// wait loop polled a merge that could never land, and the chunk was
// silently orphaned (nothing ever re-drives a "running" repair without a
// manual `workflow resume`, and the false "published" status hid that fact
// from `mivia stack status`).

import (
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

func TestChunkPublishedOnlyWhenRunActuallySucceeded(t *testing.T) {
	cases := []struct {
		name        string
		runStatus   string
		wantMessage string
	}{
		{"real publish", "succeeded", "published"},
		{"repair re-entry", "running", "entered repair"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chunkDeliveryOutcomeMessage("c1", "wfr-x", tc.runStatus)
			if got == "" {
				t.Fatal("chunkDeliveryOutcomeMessage returned empty")
			}
			if !containsSubstr(got, tc.wantMessage) {
				t.Fatalf("message = %q, want it to contain %q", got, tc.wantMessage)
			}
		})
	}
}

func TestChunkDeliverySucceededOnlyForSucceededRunStatus(t *testing.T) {
	if !chunkDeliverySucceeded("succeeded") {
		t.Fatal("chunkDeliverySucceeded(succeeded) = false, want true")
	}
	for _, s := range []string{"running", "delivery_pending", "delivery_failed", "pending"} {
		if chunkDeliverySucceeded(s) {
			t.Fatalf("chunkDeliverySucceeded(%s) = true, want false (a repair re-entry, not a real publish)", s)
		}
	}
}

// TestChunkSettleAfterDeliveryOnlyPublishesOnRealSuccess closes the actual
// gap the dag-v3/stack-v8 incident exposed: TestChunkPublishedOnlyWhenRunActuallySucceeded
// and TestChunkDeliverySucceededOnlyForSucceededRunStatus above only pin the
// pure helper functions (the honest MESSAGE was already correct) - neither
// exercises chunkSettleAfterDelivery itself, the function that actually
// calls ledger.TransitionTask. That gap let the exact same defect recur
// live on wfr-MASR36MV6LQRBSYC's chunks c1/c2 (2026-08-18): both chunk
// deliveries were rejected by the commit-msg hook and re-entered repair
// (run status stayed "running"), yet `mivia stack status` reported both
// chunks "published" with an "open PR" - because chunkSettleAfterDelivery
// called TransitionTask(..., stackStatusPublished) unconditionally,
// ignoring the fresh run status it had just used to build the (correct)
// printed message. This pins the ledger transition itself, not just the
// message string.
func TestChunkSettleAfterDeliveryOnlyPublishesOnRealSuccess(t *testing.T) {
	cases := []struct {
		name       string
		runStatus  workflowledger.RunStatus
		wantStatus string
	}{
		{"real publish marks published", workflowledger.RunStatusSucceeded, stackStatusPublished},
		{"repair re-entry stays reviewed", workflowledger.RunStatusRunning, stackStatusReviewed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := workflowledger.NewMemoryRepository()
			t.Cleanup(func() { _ = repo.Close() })
			ledger := tasks.NewMemoryStore()
			stackID := "stack-settle-" + string(tc.runStatus)
			if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
				t.Fatal(err)
			}
			seedStackTaskStatus(t, ledger, stackID, "c1", stackStatusReviewed)

			fresh := workflowledger.RunSnapshot{RunID: "wfr-c1", Status: tc.runStatus}
			chunkSettleAfterDelivery(repo, ledger, stackID, "c1", fresh)

			got, err := ledger.GetTask(stackID, "c1")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("chunk status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
}

func containsSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
