package ledger

import (
	"strings"
	"testing"
)

// TestStorageEventIDUniqueAcrossSimulatedProcesses pins the cross-process
// uniqueness contract of newStorageEventID. Two processes sharing one SQLite
// store (the supported shared-workspace deployment) each start their
// process-local counter at 0, so at the same counter position both mint
// "se-<n>" and collide on the events table's id TEXT PRIMARY KEY, surfacing as
// ErrDuplicate. The per-mint random suffix must make every ID distinct even at
// an equal counter position.
//
// Each round resets the package counter to 0 to simulate a fresh process
// minting at the position another process already used, then asserts the ID
// carries the random suffix. This fails before the fix with certainty (every
// round mints "se-1"): a per-process nonce would also fail here, because the
// counter is pinned at the same position each round so a fixed nonce repeats
// the identical ID - exactly the shared-store race.
func TestStorageEventIDUniqueAcrossSimulatedProcesses(t *testing.T) {
	preTestCounter := storageEventIDCounter.Load()
	defer storageEventIDCounter.Store(preTestCounter)

	const rounds = 16
	seen := make(map[string]struct{}, rounds)
	for i := 0; i < rounds; i++ {
		// A second process starts fresh: reset the counter to 0 and mint at
		// the same position the first process already used.
		storageEventIDCounter.Store(0)
		id := newStorageEventID()

		if id == "se-1" {
			t.Fatalf("newStorageEventID() = %q: no random suffix, two processes minting at the same counter position collide", id)
		}
		if !strings.HasPrefix(id, "se-") {
			t.Fatalf("newStorageEventID() = %q: want prefix %q", id, "se-")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("newStorageEventID() minted duplicate id %q at the same counter position", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != rounds {
		t.Fatalf("expected %d distinct ids, got %d", rounds, len(seen))
	}
}
