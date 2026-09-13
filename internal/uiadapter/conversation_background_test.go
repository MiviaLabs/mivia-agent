// conversation_background_test.go pins Send's background gate: a
// background-marked Conversation must not touch the process-wide
// SubagentProgressRegistrar, so a background automation run cannot
// hijack the foreground session's subagent-dispatch display. It also
// covers the registrar's ownership-MOVES behavior (adopt/release across
// SetForeground/SetBackground).
package uiadapter_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
)

// registerCountingSubagentProgress installs a SubagentProgressRegistrar
// that counts invocations, and restores the prior registrar on
// t.Cleanup. Mirrors registerCapturingSubagentProgress's swap/restore
// pattern above, but only needs a call count here.
//
// A plain int is safe here (unlike registerTrackingSubagentProgress's
// counters below): every call this file makes into the counting
// registrar happens synchronously on the test's own goroutine, inside
// Conversation.Send or SetForeground/SetBackground, never from the
// per-turn background goroutine's deferred release.
func registerCountingSubagentProgress(t *testing.T) *int {
	t.Helper()
	calls := 0
	prev := uiadapter.SubagentProgressRegistrar
	uiadapter.SubagentProgressRegistrar = func(fn func(agent.Event)) func() {
		calls++
		return func() {}
	}
	t.Cleanup(func() { uiadapter.SubagentProgressRegistrar = prev })
	return &calls
}

// TestSend_BackgroundConversationSkipsSubagentRegistrar covers T6.1: a
// Conversation marked background via SetBackground must not invoke the
// package-wide SubagentProgressRegistrar, so its subagent-progress
// events cannot overwrite whatever the foreground session installed.
func TestSend_BackgroundConversationSkipsSubagentRegistrar(t *testing.T) {
	calls := registerCountingSubagentProgress(t)

	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	conv := newTestConversation(t, completer)
	conv.SetBackground(true)
	if !conv.IsBackground() {
		t.Fatal("IsBackground() = false after SetBackground(true)")
	}

	handle, err := conv.Send(context.Background(), intent.Send{Text: "run automation"})
	if err != nil {
		t.Fatalf("conv.Send: %v", err)
	}
	drainUntilClose(t, handle.Events(), 5*time.Second)

	if *calls != 0 {
		t.Fatalf("SubagentProgressRegistrar called %d times for a background conversation, want 0", *calls)
	}
}

// TestSend_ForegroundConversationInvokesSubagentRegistrar covers the
// control case: an ordinary (non-background) Conversation must still
// invoke the registrar, so T6.1's skip is scoped to background
// conversations only.
func TestSend_ForegroundConversationInvokesSubagentRegistrar(t *testing.T) {
	calls := registerCountingSubagentProgress(t)

	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	conv := newTestConversation(t, completer)
	if conv.IsBackground() {
		t.Fatal("IsBackground() = true for a fresh conversation, want false")
	}

	handle, err := conv.Send(context.Background(), intent.Send{Text: "hello"})
	if err != nil {
		t.Fatalf("conv.Send: %v", err)
	}
	drainUntilClose(t, handle.Events(), 5*time.Second)

	if *calls != 1 {
		t.Fatalf("SubagentProgressRegistrar called %d times for a foreground conversation, want 1", *calls)
	}
}

// registerTrackingSubagentProgress installs a registrar that records
// acquisitions and releases, so a test can observe ownership MOVING rather
// than only counting installs.
//
// Both counters are atomic.Int32, not plain int: acquired is only ever
// bumped synchronously on the test's own goroutine (Send's
// beginTurnProgress call, or SetForeground/SetBackground's
// syncProgressRegistration call), but released can also fire from the
// per-turn BACKGROUND goroutine's own deferred release
// (runTurnGoroutine's `defer h.restore()`, which calls endTurnProgress)
// when a turn completes on its own rather than via an explicit
// SetForeground the test calls itself. That defer runs strictly AFTER
// the turn's events channel closes - emitTurnEndIfWinner's
// stream.Close() happens synchronously in runTurnGoroutine's function
// body, before ANY of its defers run, and h.restore() is the LAST of
// those defers to run (registered first among the four, and defers run
// LIFO: waiter.Done(), then active.Store(false), then turnMu.Unlock(),
// then h.restore()) - so a test that reads a plain int right after
// drainUntilClose observes the channel close with no happens-before
// edge to that later write. That was the test-only data race in this
// file's registrar tests (reading a plain int written from another
// goroutine with no synchronization); waitGroupDone below closes the
// gap deterministically instead of just switching to an atomic - an
// atomic alone fixes the -race report but not the flakiness, since the
// VALUE can still be stale immediately after drainUntilClose returns.
func registerTrackingSubagentProgress(t *testing.T) (acquired, released *atomic.Int32) {
	t.Helper()
	a := &atomic.Int32{}
	r := &atomic.Int32{}
	prev := uiadapter.SubagentProgressRegistrar
	uiadapter.SubagentProgressRegistrar = func(fn func(agent.Event)) func() {
		a.Add(1)
		return func() { r.Add(1) }
	}
	t.Cleanup(func() { uiadapter.SubagentProgressRegistrar = prev })
	return a, r
}

// waitGroupDone blocks until wg reaches zero or timeout elapses, failing
// the test on timeout. This is the deterministic alternative to reading
// a release counter right after drainUntilClose: drainUntilClose only
// proves the EARLIER emitTurnEndIfWinner call closed the channel, not
// that the per-turn goroutine's defer chain (which runs the release)
// has finished - see registerTrackingSubagentProgress's doc comment.
// Every test below that needs to observe a release the turn's OWN
// goroutine performs (as opposed to one driven synchronously by an
// explicit SetForeground/SetBackground call on the test's own
// goroutine) installs turnWaiter and waits with this helper first.
func waitGroupDone(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("per-turn goroutine did not return within %s", timeout)
	}
}

// TestAdoptingARunningBackgroundTurnAcquiresTheSubagentRegistrar is the
// defect. A turn's registrar ownership used to be decided ONCE, at Send. An
// automation run starts its turn while background - correctly installing
// nothing - and the operator then opens it. SetForeground flipped a flag
// and nothing re-registered, so for the rest of that turn the subagent
// panel received no progress at all: no step count, no tool count, a status
// that never moved, and an empty agent dialog.
func TestAdoptingARunningBackgroundTurnAcquiresTheSubagentRegistrar(t *testing.T) {
	acquired, released := registerTrackingSubagentProgress(t)
	var wg sync.WaitGroup
	uiadapter.SetTurnWaiterForTest(&wg)
	defer uiadapter.SetTurnWaiterForTest(nil)

	block := make(chan struct{})
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}, block: block}
	conv := newTestConversation(t, completer)
	conv.SetBackground(true)

	wg.Add(1)
	handle, err := conv.Send(context.Background(), intent.Send{Text: "run automation"})
	if err != nil {
		t.Fatalf("conv.Send: %v", err)
	}
	if got := acquired.Load(); got != 0 {
		t.Fatalf("an unwatched background turn took the registrar %d time(s); it must not hijack the watched session", got)
	}

	// The operator opens the run while its turn is still in flight.
	conv.SetForeground(true)
	if got := acquired.Load(); got != 1 {
		t.Fatalf("adopting a running background turn acquired the registrar %d time(s), want 1; its subagent progress would never reach the panel", got)
	}

	close(block)
	drainUntilClose(t, handle.Events(), 5*time.Second)
	waitGroupDone(t, &wg, 5*time.Second)
	if got := released.Load(); got != 1 {
		t.Fatalf("the finished turn released the registrar %d time(s), want 1", got)
	}
}

// TestReleasingAWatchedTurnGivesTheSubagentRegistrarBack pins the other
// direction: switching away mid-turn hands the single process-wide slot
// back, so the session the operator moves TO can take it.
//
// The release this test checks (after SetForeground(false)) runs
// SYNCHRONOUSLY on the test's own goroutine, inside
// syncProgressRegistration - not on the per-turn background goroutine -
// so, unlike TestAdoptingARunningBackgroundTurnAcquiresTheSubagentRegistrar
// above, no wait is needed before reading released here.
func TestReleasingAWatchedTurnGivesTheSubagentRegistrarBack(t *testing.T) {
	acquired, released := registerTrackingSubagentProgress(t)

	block := make(chan struct{})
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}, block: block}
	conv := newTestConversation(t, completer)

	handle, err := conv.Send(context.Background(), intent.Send{Text: "hello"})
	if err != nil {
		t.Fatalf("conv.Send: %v", err)
	}
	if got := acquired.Load(); got != 1 {
		t.Fatalf("a foreground turn acquired the registrar %d time(s), want 1", got)
	}

	conv.SetForeground(false)
	if got := released.Load(); got != 1 {
		t.Fatalf("switching away released the registrar %d time(s), want 1", got)
	}
	// Re-adopting the same live turn takes it back.
	conv.SetForeground(true)
	if got := acquired.Load(); got != 2 {
		t.Fatalf("re-adopting acquired the registrar %d time(s), want 2", got)
	}

	close(block)
	drainUntilClose(t, handle.Events(), 5*time.Second)
}

// TestOwnershipChangeWithNoLiveTurnTakesNothing pins the guard: the
// registrar is only ever held on behalf of a turn in flight, so switching
// between idle sessions must not install a stale sink.
//
// Waits for the per-turn goroutine's own release to finish (see
// waitGroupDone) BEFORE toggling foreground: acquireProgressLocked's
// no-op guard is `progressHandler == nil`, which the turn's own deferred
// endTurnProgress clears. Toggling foreground before that defer has run
// found progressHandler still set and re-acquired the registrar - a
// test-only race/flake this slice fixes alongside the production
// late-release bug, since both trace back to the same defer-ordering
// window (see progressGen's doc comment on Conversation).
func TestOwnershipChangeWithNoLiveTurnTakesNothing(t *testing.T) {
	acquired, _ := registerTrackingSubagentProgress(t)
	var wg sync.WaitGroup
	uiadapter.SetTurnWaiterForTest(&wg)
	defer uiadapter.SetTurnWaiterForTest(nil)

	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	conv := newTestConversation(t, completer)
	wg.Add(1)
	handle, err := conv.Send(context.Background(), intent.Send{Text: "hello"})
	if err != nil {
		t.Fatalf("conv.Send: %v", err)
	}
	drainUntilClose(t, handle.Events(), 5*time.Second)
	waitGroupDone(t, &wg, 5*time.Second)

	before := acquired.Load()
	conv.SetForeground(false)
	conv.SetForeground(true)
	if got := acquired.Load(); got != before {
		t.Fatalf("ownership changes with no live turn acquired the registrar %d extra time(s)", got-before)
	}
}

// TestProgressRegistrationOnANilConversationIsSafe pins the nil guards.
// Every other method on Conversation tolerates a nil receiver, and these
// run from ownership changes the screen makes during teardown, when the
// conversation it is switching away from may already be gone.
func TestProgressRegistrationOnANilConversationIsSafe(t *testing.T) {
	var conv *uiadapter.Conversation
	// Any of these panicking would take the TUI down on a switch during
	// shutdown, so the property under test is that each returns.
	panicked := func() (p any) {
		defer func() { p = recover() }()
		conv.SetForeground(true)
		conv.SetForeground(false)
		conv.SetBackground(true)
		return nil
	}()

	if panicked != nil {
		t.Fatalf("ownership change on a nil Conversation panicked: %v, want a nil-receiver no-op", panicked)
	}
}

// TestOverlappingTurnsDoNotStealEachOthersRegistrarRelease is the slice
// 2 regression. The bug: runTurnGoroutine's `defer c.turnMu.Unlock()`
// runs BEFORE `defer h.restore()` in program order but defers run LIFO,
// so restore() (and therefore endTurnProgress) actually fires AFTER
// turnMu unlocks. That opens a window where turn N's own defer chain is
// still unwinding while turn N+1 has already acquired turnMu, called
// Send, and registered ITS OWN progressHandler/registrar acquisition.
// Turn N's late endTurnProgress, if it were unconditional, would then
// nil out turn N+1's progressHandler and release turn N+1's registrar
// hold out from under it - the live turn's subagent panel would freeze
// mid-turn with no error and no signal, from a turn that already ended.
//
// This test cannot reliably WIN that race by timing alone (the window
// is a handful of instructions), so it proves the fix structurally
// instead: it drives two SEQUENTIAL turns (turnMu serializes Send, so
// they cannot literally overlap in wall-clock time) and asserts, via
// SetTurnWaiterForTest, that turn 1's goroutine - including its
// deferred endTurnProgress - has FULLY finished before turn 2 starts.
// It then confirms turn 2 gets its own fresh acquisition (acquired==2)
// and that turn 1's completion did not touch a THIRD count once turn 2
// is in flight - i.e. the generation token in beginTurnProgress/
// endTurnProgress is what makes a stale release safe, not accidental
// timing. TestReleasingLateFromAnOlderGenerationIsANoOp below exercises
// the actual token check directly and deterministically.
func TestOverlappingTurnsDoNotStealEachOthersRegistrarRelease(t *testing.T) {
	acquired, released := registerTrackingSubagentProgress(t)
	var wg sync.WaitGroup
	uiadapter.SetTurnWaiterForTest(&wg)
	defer uiadapter.SetTurnWaiterForTest(nil)

	comp := &scriptedCompleter{turns: []provider.Response{{Content: "one"}, {Content: "two"}}}
	conv := newTestConversation(t, comp)

	wg.Add(1)
	h1, err := conv.Send(context.Background(), intent.Send{Text: "first"})
	if err != nil {
		t.Fatalf("send 1: %v", err)
	}
	drainUntilClose(t, h1.Events(), 5*time.Second)
	waitGroupDone(t, &wg, 5*time.Second)
	if got := acquired.Load(); got != 1 {
		t.Fatalf("turn 1 acquired the registrar %d time(s), want 1", got)
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("turn 1's own completion released the registrar %d time(s), want 1", got)
	}

	wg.Add(1)
	h2, err := conv.Send(context.Background(), intent.Send{Text: "second"})
	if err != nil {
		t.Fatalf("send 2: %v", err)
	}
	// Turn 2's beginTurnProgress must have taken its OWN fresh
	// acquisition - not found the registrar already held by a phantom
	// leftover from turn 1's release racing its own start.
	if got := acquired.Load(); got != 2 {
		t.Fatalf("turn 2 acquired the registrar %d time(s) total, want 2 (its own fresh acquisition)", got)
	}
	drainUntilClose(t, h2.Events(), 5*time.Second)
	waitGroupDone(t, &wg, 5*time.Second)
	if got := released.Load(); got != 2 {
		t.Fatalf("turn 2's own completion released the registrar %d time(s) total, want 2", got)
	}
}

// TestReleasingLateFromAnOlderGenerationIsANoOp verifies cross-conversation
// registrar isolation. An idle conversation must not interfere with a live
// turn that holds the registrar in another conversation.
//
// The test runs turn 1 on conv to completion. Turn 1 acquires and releases
// the shared registrar. Turn 2 then starts on a separate conversation, conv2,
// and holds a live acquisition.
//
// The test toggles foreground status on the idle conv. Because conv has no
// active turn and holds no registration, these ownership changes are no-ops.
// They do not acquire the registrar, and they do not release the registrar
// held by conv2.
//
// Because the registrar is a single process-wide slot, an invalid release
// from conv would tear down conv2's live acquisition. When turn 2 completes,
// conv2 releases the registrar as expected.
func TestReleasingLateFromAnOlderGenerationIsANoOp(t *testing.T) {
	acquired, released := registerTrackingSubagentProgress(t)
	var wg sync.WaitGroup
	uiadapter.SetTurnWaiterForTest(&wg)
	defer uiadapter.SetTurnWaiterForTest(nil)

	comp := &scriptedCompleter{turns: []provider.Response{{Content: "one"}, {Content: "two"}}}
	conv := newTestConversation(t, comp)

	wg.Add(1)
	h1, err := conv.Send(context.Background(), intent.Send{Text: "first"})
	if err != nil {
		t.Fatalf("send 1: %v", err)
	}
	drainUntilClose(t, h1.Events(), 5*time.Second)
	waitGroupDone(t, &wg, 5*time.Second)
	if got := released.Load(); got != 1 {
		t.Fatalf("turn 1 released the registrar %d time(s), want 1", got)
	}

	// Turn 2 starts and holds its own acquisition.
	block := make(chan struct{})
	comp2 := &scriptedCompleter{turns: []provider.Response{{Content: "two"}}, block: block}
	conv2 := newTestConversation(t, comp2)
	wg.Add(1)
	h2, err := conv2.Send(context.Background(), intent.Send{Text: "second"})
	if err != nil {
		t.Fatalf("send 2: %v", err)
	}
	if got := acquired.Load(); got != 2 {
		t.Fatalf("turn 2 acquired the registrar %d time(s) total, want 2", got)
	}

	// A superseded ownership-change call on conv (turn 1's conversation,
	// already idle) must be a no-op: no extra acquire, no extra release,
	// and critically no effect on conv2's still-live acquisition (a
	// process-wide single-slot registrar means an unconditional release
	// anywhere would tear down whichever conversation currently holds
	// it - here conv2's turn 2).
	conv.SetForeground(false)
	conv.SetForeground(true)
	if got := acquired.Load(); got != 2 {
		t.Fatalf("conv's post-completion ownership churn acquired the registrar %d time(s) total, want 2 (no new acquisition from an idle conversation with no live turn)", got)
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("conv's post-completion ownership churn released the registrar %d time(s) total, want 1 (turn 2's acquisition on conv2 must be untouched)", got)
	}

	close(block)
	drainUntilClose(t, h2.Events(), 5*time.Second)
	waitGroupDone(t, &wg, 5*time.Second)
	if got := released.Load(); got != 2 {
		t.Fatalf("turn 2's own completion released the registrar %d time(s) total, want 2", got)
	}
}
