package ledger

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestRebuildProjectionTombstoneResetsReplayState proves that a tombstone
// starts a new run incarnation with no merge or step state from its predecessor.
func TestRebuildProjectionTombstoneResetsReplayState(t *testing.T) {
	events := []storage.Event{
		wfEvent("e-old-created", 1, 1, eventKindRunCreated, projRunCreatedPayload(t, "old-initial")),
		wfEvent("e-old-started", 2, 2, eventKindAttemptStarted, projAttemptStartedPayload(t, "reused-attempt", "old-step")),
		wfEvent("e-deleted", 3, 3, eventKindRunDeleted, mustMarshal(t, runDeletedPayload{})),
		wfEvent("e-new-created", 4, 4, eventKindRunCreated, projRunCreatedPayload(t, "new-initial")),
		wfEvent("e-new-started", 5, 5, eventKindAttemptStarted, projAttemptStartedPayload(t, "reused-attempt", "new-step")),
	}

	var (
		proj     Projection
		err      error
		panicVal any
	)
	func() {
		defer func() { panicVal = recover() }()
		proj, err = RebuildProjection(events)
	}()
	if panicVal != nil {
		t.Fatalf("RebuildProjection panicked after tombstone: %v", panicVal)
	}
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if len(proj.Attempts) != 1 {
		t.Fatalf("len(Attempts) = %d, want 1: %+v", len(proj.Attempts), proj.Attempts)
	}
	if got := proj.Attempts[0].StepID; got != "new-step" {
		t.Errorf("Attempts[0].StepID = %q, want new-step", got)
	}
	if got := proj.ActiveStepID; got != "new-step" {
		t.Errorf("ActiveStepID = %q, want new-step", got)
	}
}
