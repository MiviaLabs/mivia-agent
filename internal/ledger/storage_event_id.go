package ledger

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var storageEventIDCounter atomic.Uint64

// storageEventIDSuffix returns a fresh 64-bit random hex suffix for a storage
// event ID. crypto/rand is the entropy source; if the read fails (an
// unrecoverable condition in practice) it falls back to a time-derived hex
// value so minting is total and an append can never wedge on this helper.
func storageEventIDSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// newStorageEventID mints a storage event ID that is unique across EVERY
// writer of a shared store, not just this process. The monotone process-local
// counter keeps within-process ordering; the fresh per-mint random suffix
// makes IDs from different processes - which each start their counter at 0 -
// disjoint even when two live processes race before either catches up on the
// other's rows, a race the replay-time advance cannot cover. The store's
// events.id PRIMARY KEY remains the fail-closed backstop: a degenerate entropy
// collision surfaces as ErrDuplicate instead of silent corruption.
func newStorageEventID() string {
	n := storageEventIDCounter.Add(1)
	return fmt.Sprintf("se-%d-%s", n, storageEventIDSuffix())
}

// advanceStorageEventIDCounter raises the process-local event ID counter to at
// least n, so IDs minted after a restart cannot collide with replayed LEGACY
// "se-<n>" rows written before the random suffix existed. New-format IDs
// ("se-<n>-<hex>") parse to 0 via parseSuffixNum, making this a harmless
// no-op for them: the per-mint random suffix already guarantees restart
// non-collision.
func advanceStorageEventIDCounter(n uint64) {
	for {
		cur := storageEventIDCounter.Load()
		if n <= cur {
			return
		}
		if storageEventIDCounter.CompareAndSwap(cur, n) {
			return
		}
	}
}
