package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// fuzzHookContext is the fixed PostInvokeHook text every fuzz iteration's hook
// returns, so a same-step duplicate carrying the owner's record is provably
// carrying the POST-hook result (D2).
const fuzzHookContext = "post-hook-fixed"

// Fuzz iteration uses fixed, per-dispatcher-fresh IDs: each iteration builds a
// brand-new Dispatcher, so the IDs never collide across iterations.
const (
	fuzzOwnerID = "owner"
	fuzzDupID   = "owner-dup"
)

type fuzzDedupParams struct {
	mode      int
	turn      bool
	parentID  string
	step      int
	skipDedup bool
	postHook  bool
	maxOut    int
	budget    int
	maxBudget int
	input     json.RawMessage
}

// deriveFuzzDedupParams maps fuzz bytes to request shapes. Budget and MaxBudget
// are varied in ranges where reservation ALWAYS succeeds, so the dedup
// assertions are unconditional. The worst case is a turn-scoped SkipDedup pair
// sharing one budget key (the turn): each call charges its own Budget because
// a SkipDedup duplicate never short-circuits on the bucket, so Budget ≤ 2 with
// MaxBudget = 5 keeps 2+2 = 4 under the cap; a non-skip turn-scoped duplicate
// short-circuits on the bucket BEFORE the budget charge, and non-turn calls
// charge under their own IDs. Input is capped at 256 bytes, far under the
// default 64KiB input allowance, so validation can never reject it.
func deriveFuzzDedupParams(data []byte) fuzzDedupParams {
	seed := func(i int) byte {
		if i < len(data) {
			return data[i]
		}
		return 0
	}
	start := 8
	if start > len(data) {
		start = len(data)
	}
	in := data[start:]
	if len(in) > 256 {
		in = in[:256]
	}
	p := fuzzDedupParams{
		mode:      int(seed(0) % 3),
		turn:      seed(1)&1 == 1,
		step:      int(seed(2) % 3),
		skipDedup: seed(3)&1 == 1,
		postHook:  seed(4)&1 == 1,
		budget:    int(seed(7) % 3),
		input:     json.RawMessage(in),
	}
	if seed(5)&1 == 1 {
		p.maxOut = 4096
	}
	if seed(6)%2 == 1 {
		p.maxBudget = 5
	}
	// ParentID is meaningful only for turn-scoped calls: it separates subagent
	// task contexts in the turn-dedup key. For a non-turn call it would become
	// the SHARED budget key for the owner+dup pair, and with MaxBudget=3 two
	// budget-2 charges (2+2 > 3) would fail the dup's reservation - the pair
	// would execute once, not twice, and the unconditional assertions below
	// would be false positives, not defect reports.
	if seed(1)&2 == 2 && p.turn {
		p.parentID = "task"
	}
	return p
}

// fuzzDedupHandler is a synthetic handler whose mode and output size are fixed
// by the derived params: 0 success, 1 fail, 2 runaway (past ceiling×4 when a
// small ceiling is configured).
func fuzzDedupHandler(p fuzzDedupParams, calls *atomic.Int32) Handler {
	return handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		calls.Add(1)
		switch p.mode {
		case 1:
			return nil, errors.New("boom")
		case 2:
			size := 100
			if p.maxOut > 0 {
				size = p.maxOut*outputCeilingRunawayFactor + 1
			}
			return json.RawMessage(strings.Repeat("x", size)), nil
		default:
			return json.RawMessage(`{"ok":true}`), nil
		}
	})
}

// checkFuzzDedupInvariants asserts D1 (no ID-keyed completed retention for
// turn-scoped calls, present for non-turn calls), D2 (a same-step duplicate
// carries the owner's post-hook HookContext), and one execution per
// same-step identical pair.
func checkFuzzDedupInvariants(t *testing.T, d *Dispatcher, ownerReq Request, owner, dup Result, p fuzzDedupParams, calls int32) {
	t.Helper()
	key, contentHash := turnDedupKey(ownerReq)
	d.mu.Lock()
	_, completedOwner := d.completed[fuzzOwnerID]
	_, completedDup := d.completed[fuzzDupID]
	_, bucketRecorded := d.turnResults[key][contentHash]
	d.mu.Unlock()

	if key != "" {
		if completedOwner || completedDup {
			t.Errorf("D1: turn-scoped call retained a completed entry (owner=%v dup=%v)", completedOwner, completedDup)
		}
		if !bucketRecorded {
			t.Errorf("turn-scoped call did not record its step-scoped bucket result")
		}
		if dup.Metadata.Status != "duplicate" {
			t.Errorf("same-step identical re-issue status = %q, want duplicate", dup.Metadata.Status)
		}
		if p.postHook {
			if owner.HookContext != fuzzHookContext {
				t.Errorf("owner HookContext = %q, want %q", owner.HookContext, fuzzHookContext)
			}
			if dup.HookContext != fuzzHookContext {
				t.Errorf("D2: duplicate HookContext = %q, want owner's %q", dup.HookContext, fuzzHookContext)
			}
			if string(dup.Output) != string(owner.Output) {
				t.Errorf("duplicate Output differs from the owner's")
			}
		}
		if calls != 1 {
			t.Errorf("handler executed %d times across a same-step identical pair, want 1", calls)
		}
		return
	}
	if p.skipDedup {
		if completedOwner || completedDup || bucketRecorded {
			t.Errorf("SkipDedup call wrote dedup state: owner=%v dup=%v bucket=%v", completedOwner, completedDup, bucketRecorded)
		}
		if calls != 2 {
			t.Errorf("SkipDedup identical pair executed %d times, want 2", calls)
		}
		if dup.Metadata.Status == "duplicate" {
			t.Errorf("SkipDedup re-issue answered as duplicate")
		}
		return
	}
	// Non-turn, non-skip: the ID-keyed completed map is the dedup authority.
	// Invoke's tail writes it only on the COMPLETED path (completeIDKeyed, after
	// the ceiling check): handler failures and over-ceiling destroy return BEFORE
	// that write, and the owner's deferred releaseIDKeyed drains the ID-keyed
	// waiter channel with the final post-hook result, never d.completed. So the
	// map holds exactly the results that completed - presence ⟺ Err == nil -
	// and a same-ID reissue of a FAILED call re-runs today (pre-existing
	// semantics, unchanged by this slice; the step-aware turn bucket is the map
	// that records success AND failure). Both directions are asserted so a
	// future change to record failures for same-ID failure dedup must update
	// this test deliberately.
	if owner.Err == nil && !completedOwner {
		t.Errorf("non-turn success did not record its ID-keyed completed entry")
	}
	if owner.Err != nil && completedOwner {
		t.Errorf("non-turn failure recorded an ID-keyed completed entry (err=%v)", owner.Err)
	}
	if dup.Err == nil && !completedDup {
		t.Errorf("non-turn re-issue success did not record its ID-keyed completed entry")
	}
	if dup.Err != nil && completedDup {
		t.Errorf("non-turn re-issue failure recorded an ID-keyed completed entry (err=%v)", dup.Err)
	}
	if calls != 2 {
		t.Errorf("non-turn identical pair (fresh IDs) executed %d times, want 2", calls)
	}
}

// fuzzDedupSeeds covers the interesting corners so the plain go test run (which
// executes the seed corpus) exercises D1 and D2 without the fuzzer.
func fuzzDedupSeeds() [][]byte {
	return [][]byte{
		{0, 1, 0, 0, 0},          // turn-scoped success: D1 retention probe
		{2, 1, 0, 0, 1, 1},       // turn-scoped runaway + post hook, 4KiB ceiling: D2 probe
		{0, 0, 0, 0, 0},          // non-turn success: completed-map pin
		{1, 1, 0, 0, 1},          // turn-scoped handler failure + post hook
		{0, 1, 1, 1, 0},          // turn-scoped success, SkipDedup pair
		{1, 0, 0, 0, 0},          // non-turn handler failure
		{0, 1, 2, 0, 1},          // turn-scoped success at step 2 + post hook
		{0, 1, 1, 1, 0, 0, 1, 2}, // turn-scoped SkipDedup pair, MaxBudget=5, Budget=2: 2+2=4 stays under the cap
	}
}

// FuzzInvokeDedupState drives the dedup/budget/ceiling state machine with
// varied request shapes over registered synthetic handlers and asserts the D1
// and D2 invariants plus one-execution-per-same-step-identical-pair. It is a
// deterministic in-process target: a fresh dispatcher per iteration, no
// timers, no goroutines.
func FuzzInvokeDedupState(f *testing.F) {
	for _, seed := range fuzzDedupSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p := deriveFuzzDedupParams(data)
		var calls atomic.Int32
		policy := Policy{MaxOutputBytes: p.maxOut, MaxBudget: p.maxBudget}
		if p.postHook {
			policy.PostInvokeHook = func(context.Context, Request, Result) HookResult {
				return HookResult{Context: fuzzHookContext}
			}
		}
		d := New(policy)
		if err := d.Register(Tool, "t", fuzzDedupHandler(p, &calls)); err != nil {
			t.Fatal(err)
		}
		turnID := ""
		if p.turn {
			turnID = "turn:1"
		}
		ownerReq := Request{ID: fuzzOwnerID, Kind: Tool, Name: "t", Input: p.input, TurnID: turnID, ParentID: p.parentID, Step: p.step, SkipDedup: p.skipDedup, Budget: p.budget}
		owner := d.Invoke(context.Background(), ownerReq)
		dupReq := ownerReq
		dupReq.ID = fuzzDupID
		dup := d.Invoke(context.Background(), dupReq)
		checkFuzzDedupInvariants(t, d, ownerReq, owner, dup, p, calls.Load())
	})
}
