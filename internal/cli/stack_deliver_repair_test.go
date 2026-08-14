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

import "testing"

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

func containsSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
