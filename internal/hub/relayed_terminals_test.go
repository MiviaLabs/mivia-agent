package hub

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestTurnTerminalsAreNotRelayed pins a deliberate omission, because the list
// reads like an oversight and the fix for it is one line in the wrong
// direction.
//
// chat.Session publishes KindTurnEnd and KindError, so adding them here looks
// obviously right. It is not, yet. renderExternalEvent treats the first event
// carrying a run id as the start of that turn, which needs arrival order, and
// the bus gives one subscription per kind with a queue each - so a terminal
// routinely overtakes the assistant deltas of the turn it closes and the
// receiver mints a turn from it, emits done, drops the run, then mints a
// second turn when the content lands.
//
// Relaying KindError additionally puts raw provider error text on the wire:
// publishTurnEnd sets Err and wire.go serializes err.Error() verbatim, while
// this process's own NDJSON deliberately emits a classified string.
//
// Delete this test when the receiver stops inferring turn start from arrival
// order and the error text is classified at the boundary - not before.
func TestTurnTerminalsAreNotRelayed(t *testing.T) {
	for _, kind := range []events.Kind{events.KindTurnEnd, events.KindError} {
		for _, relayed := range relayedKinds {
			if relayed == kind {
				t.Errorf("%q is relayed, but the receiver cannot order-tolerate it; see this test's comment", kind)
			}
		}
	}
}

// TestTurnStartIsStillRelayed guards the other half: the omission above must
// not be widened into dropping turn starts, which the receiver does handle and
// which carry the user's submitted text.
func TestTurnStartIsStillRelayed(t *testing.T) {
	for _, relayed := range relayedKinds {
		if relayed == events.KindTurnStart {
			return
		}
	}
	t.Error("KindTurnStart is no longer relayed; a second surface loses the user's own text")
}
