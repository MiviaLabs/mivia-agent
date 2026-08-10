package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Per-turn dedup for Tool invocations.
//
// A duplicate delivery of the same logical tool call (same tool, same input,
// fresh call ID) must not execute its side effects a second time. The
// ID-keyed completed map cannot catch it - the duplicate carries a new ID - so
// completed Tool results are additionally recorded per
// (TurnID, ParentID, step, name+input hash) and a re-issued identical call
// within the same turn returns the recorded result instead of re-executing.
// Scoped per turn so an identical command in a LATER turn, or in a different
// subagent task context, always executes fresh; scoped per model step (Step >
// 0) so an identical call re-issued in a LATER step of the same turn re-runs,
// while a same-step re-issue still dedups.
//
// Dedup also covers calls still in flight: the invocation that reserves the
// flight key owns the execution, and every identical call that arrives while
// it runs waits on a per-waiter channel and receives the owner's result. A
// dedup hit skips the budget charge by design - the tool 'did not run', so the
// duplicate cannot consume cumulative budget. Close resolves in-flight waiters
// with a closed result before it releases the table.

// maxTurnBuckets bounds the per-turn dedup cache. TurnIDs advance
// monotonically per session; keeping only the most recent few turns bounds
// memory while covering the duplicate-delivery window (the re-issue arrives
// while the same turn is still in flight or just completed).
const maxTurnBuckets = 8

// turnDedupKey derives the per-turn dedup bucket key and content hash for a
// Tool invocation. The key is TurnID+"\x00"+ParentID, extended with
// "\x00"+itoa(Step) when the call is loop-stamped (Step > 0); the step
// component is what makes a cross-step re-issue re-run while a same-step
// re-issue still dedups. ParentID separates the root loop from each subagent
// task so identical calls in different task contexts never collide. An empty
// TurnID disables the dedup entirely (no bucket to collide in), and so does
// SkipDedup: a read-only call that opted out of dedup gets an empty key, which
// disables flight-entry creation, duplicate reservation, in-flight completion
// and bucket recording for that call.
func turnDedupKey(req Request) (key, contentHash string) {
	if req.Kind != Tool || req.TurnID == "" || req.SkipDedup {
		return "", ""
	}
	sum := sha256.Sum256(append(append([]byte(req.Name), 0), req.Input...))
	key = req.TurnID + "\x00" + req.ParentID
	if req.Step > 0 {
		key += "\x00" + strconv.Itoa(req.Step)
	}
	return key, hex.EncodeToString(sum[:8])
}

// inFlightEntry tracks one in-flight Tool invocation under its flight key
// (the turn-scoped key plus content hash). The owner reserves the entry in
// reserve(); identical calls arriving before it completes are appended to
// waiters and answered from the owner's result when completeTurnInFlight runs.
type inFlightEntry struct {
	// owner is the req.ID of the invocation that reserved the flight key. Only
	// the owner may complete the entry or write its bucket record: a DIFFERENT
	// caller with identical content whose validation/reservation fails must not
	// tear the entry down or poison the bucket with its own error.
	owner   string
	result  Result
	done    bool
	waiters []chan Result
}

// turnDedupReservationLocked returns a duplicate reservation when an identical
// Tool call (same name+input) is already in flight or was already completed
// within the same turn-scoped bucket. The in-flight entry wins over the
// completed bucket: while the owner still runs, a waiter must wait for the
// owner's actual result rather than a stale record. Completed records are
// delivered through a closed waiter, so the re-issued call never reaches its
// handler. d.mu must be held.
func (d *Dispatcher) turnDedupReservationLocked(req Request) (reservation, bool) {
	key, contentHash := turnDedupKey(req)
	if key == "" {
		return reservation{}, false
	}
	flightKey := key + "\x00" + contentHash
	if entry := d.inFlight[flightKey]; entry != nil {
		if entry.done {
			return reservation{dup: true, waiter: closedResult(entry.result)}, true
		}
		waiter := make(chan Result, 1)
		entry.waiters = append(entry.waiters, waiter)
		return reservation{dup: true, waiter: waiter}, true
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

// completeTurnInFlight resolves the in-flight entry for req: it records the
// owner's terminal result, delivers it to every waiter (each waiter channel is
// buffered with capacity 1, so the send never blocks), and removes the entry.
// It runs for every terminal outcome of the OWNER's invocation - success,
// failure, and policy block - so an in-flight duplicate waiter always receives
// a result. A caller that is not the entry owner (it never reserved; its
// validation or reservation failed) is refused: completing the owner's entry
// with a stranger's error would poison the waiters and the bucket. Returns
// true when this call performed the completion. d.mu must be held.
func (d *Dispatcher) completeTurnInFlight(req Request, result Result) bool {
	key, contentHash := turnDedupKey(req)
	if key == "" {
		return false
	}
	flightKey := key + "\x00" + contentHash
	entry := d.inFlight[flightKey]
	if entry == nil || entry.owner != req.ID {
		return false
	}
	entry.result = result
	entry.done = true
	for _, waiter := range entry.waiters {
		select {
		case waiter <- result:
		default:
		}
	}
	delete(d.inFlight, flightKey)
	return true
}

// recordTurnResult remembers a completed Tool invocation under its content key
// so a duplicate delivery of the same logical call returns the recorded result
// instead of executing twice. ALL terminal results are recorded - success AND
// failure, keyed by step: the step component is what makes a cross-step retry
// re-run, while a same-step re-issue of a failure still dedups (dropping
// failure records would re-open same-batch double-execute for failing
// side-effecting tools). Blocked (hook-denied) calls are deliberately NOT
// recorded: an admission verdict can legitimately change mid-turn and the
// re-issued call must be re-evaluated, not answered from a stale block.
func (d *Dispatcher) recordTurnResult(req Request, result Result) {
	key, contentHash := turnDedupKey(req)
	if key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	// Resolve the flight-keyed entry (only the owner may) before writing the
	// bucket: a waiter attached while the call was in flight must receive this
	// result even though the bucket is about to answer the next identical call.
	// A non-owner (validation/reservation failure on identical content) is
	// refused here and must not write the bucket either.
	if !d.completeTurnInFlight(req, result) {
		return
	}
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
