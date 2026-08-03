package runtime

import (
	"crypto/sha256"
	"encoding/hex"
)

// Per-turn dedup for Tool invocations.
//
// A duplicate delivery of the same logical tool call (same tool, same input,
// fresh call ID) must not execute its side effects a second time. The
// ID-keyed completed map cannot catch it - the duplicate carries a new ID - so
// completed Tool results are additionally recorded per
// (TurnID, ParentID, name+input hash) and a re-issued identical call within
// the same turn returns the recorded result instead of re-executing. Scoped
// per turn so an identical command in a LATER turn, or in a different subagent
// task context, always executes fresh.

// maxTurnBuckets bounds the per-turn dedup cache. TurnIDs advance
// monotonically per session; keeping only the most recent few turns bounds
// memory while covering the duplicate-delivery window (the re-issue arrives
// while the same turn is still in flight or just completed).
const maxTurnBuckets = 8

// turnDedupKey derives the per-turn dedup bucket key and content hash for a
// Tool invocation. ParentID separates the root loop from each subagent task so
// identical calls in different task contexts never collide. An empty TurnID
// disables the dedup entirely (no bucket to collide in).
func turnDedupKey(req Request) (key, contentHash string) {
	if req.Kind != Tool || req.TurnID == "" {
		return "", ""
	}
	sum := sha256.Sum256(append(append([]byte(req.Name), 0), req.Input...))
	return req.TurnID + "\x00" + req.ParentID, hex.EncodeToString(sum[:8])
}

// turnDedupReservation returns a duplicate reservation when an identical Tool
// call (same name+input) was already completed within the same turn. The
// recorded result is delivered through the closed waiter, so the re-issued
// call never reaches its handler.
func (d *Dispatcher) turnDedupReservationLocked(req Request) (reservation, bool) {
	key, contentHash := turnDedupKey(req)
	if key == "" {
		return reservation{}, false
	}
	bucket, ok := d.turnResults[key]
	if !ok {
		return reservation{}, false
	}
	previous, ok := bucket[contentHash]
	if !ok {
		return reservation{}, false
	}
	return reservation{dup: true, waiter: closedResult(previous)}, true
}

// recordTurnResult remembers a completed Tool invocation under its content key
// so a duplicate delivery of the same logical call returns the recorded result
// instead of executing twice. Blocked (hook-denied) calls are deliberately NOT
// recorded: an admission verdict can legitimately change mid-turn and the
// re-issued call must be re-evaluated, not answered from a stale block.
func (d *Dispatcher) recordTurnResult(req Request, result Result) {
	key, contentHash := turnDedupKey(req)
	if key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	bucket := d.turnResults[key]
	if bucket == nil {
		bucket = make(map[string]Result)
		d.turnResults[key] = bucket
		d.turnOrder = append(d.turnOrder, key)
		if len(d.turnOrder) > maxTurnBuckets {
			oldest := d.turnOrder[0]
			d.turnOrder = d.turnOrder[1:]
			delete(d.turnResults, oldest)
		}
	}
	bucket[contentHash] = result
}
