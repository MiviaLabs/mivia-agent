package clichat

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// drainKinds subscribes to every kind in kinds and returns a function that
// reports the total number of events observed across all of them after a
// short settle window - used to prove a NEGATIVE ("zero bus events"),
// which collectOne (single-event, blocking) cannot express.
func drainKinds(t *testing.T, bus *events.Bus, kinds []events.Kind) func() int {
	t.Helper()
	var mu sync.Mutex
	count := 0
	h := events.HandlerFunc(func(_ context.Context, _ events.Event) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	for _, k := range kinds {
		bus.Subscribe(k, h)
	}
	return func() int {
		t.Helper()
		bus.Flush()
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// TestSessionBusRegistry_RoutingIsPerSession is the routing table test: two
// registered session buses, an event stamped with Origin.SessionID=B must
// land ONLY on bus B, never on bus A.
func TestSessionBusRegistry_RoutingIsPerSession(t *testing.T) {
	busA := events.New()
	t.Cleanup(busA.Close)
	busB := events.New()
	t.Cleanup(busB.Close)

	releaseA := RegisterSessionBus("session-A", busA)
	t.Cleanup(releaseA)
	releaseB := RegisterSessionBus("session-B", busB)
	t.Cleanup(releaseB)

	waitB := collectOne(t, busB, events.KindSubagentStart)
	countA := drainKinds(t, busA, []events.Kind{events.KindSubagentStart})

	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "worker", ToolCallID: "tc-b",
		Origin: agent.EventOrigin{SessionID: "session-B", TurnID: "turn:1"},
	})

	ev := waitB()
	if ev.SessionID != "session-B" {
		t.Fatalf("SessionID = %q, want session-B", ev.SessionID)
	}
	if n := countA(); n != 0 {
		t.Fatalf("session-A bus received %d events, want 0 (routing crosstalk)", n)
	}
}

// allSubagentEventKinds mirrors agent.Event's four lifecycle kinds and the
// two content kinds this table needs to distinguish "publishes" from
// "stays local".
var allSubagentEventKindsForTest = []struct {
	name        events.Kind
	agentKind   agent.EventKind
	wireKind    events.Kind
	publishesOK bool
}{
	{"subagent_start", agent.EventSubagentStart, events.KindSubagentStart, true},
	{"subagent_end", agent.EventSubagentEnd, events.KindSubagentEnd, true},
	{"subagent_heartbeat", agent.EventSubagentHeartbeat, events.KindSubagentHeartbeat, true},
	{"subagent_begin", agent.EventSubagentBegin, events.KindSubagentBegin, true},
	{"subagent_done", agent.EventSubagentDone, events.KindSubagentDone, true},
	{"thinking", agent.EventThinking, events.KindThinking, true},
	{"assistant", agent.EventAssistant, events.KindAssistant, true},
	// A kind deliberately NOT on the allowlist. Without at least one such
	// row the table would be all-true, and flipping busPublishableKind's
	// default to `return true` would pass unnoticed - the allowlist would
	// stop being an allowlist and nothing would say so. A subagent's raw
	// tool events must reach the bus through the SubagentStart/End mapping
	// in OnEventForMultiStep, never directly.
	{"tool_start", agent.EventToolStart, events.KindToolStart, false},
	{"tool_end", agent.EventToolEnd, events.KindToolEnd, false},
}

// TestSessionBusRegistry_Allowlist is the mutation-proof allowlist table:
// the four lifecycle kinds and the two prose kinds each reach their session
// bus exactly once, and a kind that is not on the allowlist produces ZERO bus
// events while still reaching the UI sink.
//
// Prose used to be on the zero-bus side of this table. It is published now,
// under the same redaction, truncation and StreamAssistant/IncludeThinking
// controls as the root loop's text, so that a remote viewer can open a
// subagent's thread the way the TUI can. See busPublishableKind.
//
// Mutation proof: inverting busPublishableKind's switch (== becomes !=, or
// the default flips to `return true`) makes this test fail on one half of
// the table or the other - the allowed rows stop publishing, or the raw tool
// rows start leaking onto the bus. TWO negative rows, not one: a later change
// that decides raw tool events should publish would flip a single negative row
// and silently end the proof.
func TestSessionBusRegistry_Allowlist(t *testing.T) {
	for _, tc := range allSubagentEventKindsForTest {
		t.Run(string(tc.name), func(t *testing.T) {
			bus := events.New()
			t.Cleanup(bus.Close)
			release := RegisterSessionBus("sess-allowlist", bus)
			t.Cleanup(release)

			var uiCalls int
			var uiMu sync.Mutex
			prevGen := SetSubagentProgress(func(agent.Event) {
				uiMu.Lock()
				uiCalls++
				uiMu.Unlock()
			})
			t.Cleanup(func() { ClearSubagentProgress(prevGen) })

			count := drainKinds(t, bus, []events.Kind{tc.wireKind})

			emitSubagentProgress(agent.Event{
				Kind: tc.agentKind, Name: "x", ToolCallID: "tc",
				Origin:  agent.EventOrigin{SessionID: "sess-allowlist", TurnID: "t1"},
				Content: "some model content",
			})

			n := count()
			if tc.publishesOK && n != 1 {
				t.Fatalf("kind %s: bus received %d events, want exactly 1", tc.agentKind, n)
			}
			if !tc.publishesOK && n != 0 {
				t.Fatalf("kind %s: bus received %d events, want 0 (content must stay local)", tc.agentKind, n)
			}
			uiMu.Lock()
			calls := uiCalls
			uiMu.Unlock()
			if calls != 1 {
				t.Fatalf("kind %s: UI sink called %d times, want exactly 1 (UI sink is unconditional for every kind)", tc.agentKind, calls)
			}
		})
	}
}

// TestSessionBusRegistry_EmptyOriginSessionIDNeverPublishes covers the
// fail-closed guard directly: an unknown/empty Origin.SessionID must never
// panic and must never publish, even for a bus registered for something
// else in the process.
func TestSessionBusRegistry_EmptyOriginSessionIDNeverPublishes(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	release := RegisterSessionBus("some-other-session", bus)
	t.Cleanup(release)

	count := drainKinds(t, bus, []events.Kind{events.KindSubagentStart})

	// Must not panic.
	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentStart, Name: "x"})

	if n := count(); n != 0 {
		t.Fatalf("empty Origin.SessionID published %d events, want 0", n)
	}
}

// TestSessionBusRegistry_UnknownSessionIDNeverPublishes covers a
// SessionID that IS set but was never registered (lookup miss).
func TestSessionBusRegistry_UnknownSessionIDNeverPublishes(t *testing.T) {
	// No RegisterSessionBus call at all for this id.
	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "x",
		Origin: agent.EventOrigin{SessionID: "never-registered", TurnID: "t"},
	})
	// No assertion beyond "did not panic": there is no bus to observe.
}

// TestSessionBusRegistry_ReleaseStopsPublishing proves release() actually
// unbinds: after release, further events for that session id must not
// reach the bus.
func TestSessionBusRegistry_ReleaseStopsPublishing(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	release := RegisterSessionBus("sess-release", bus)

	wait := collectOne(t, bus, events.KindSubagentStart)
	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "x",
		Origin: agent.EventOrigin{SessionID: "sess-release", TurnID: "t"},
	})
	wait()

	release()

	count := drainKinds(t, bus, []events.Kind{events.KindSubagentEnd})
	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentEnd, Name: "x",
		Origin: agent.EventOrigin{SessionID: "sess-release", TurnID: "t"},
	})
	if n := count(); n != 0 {
		t.Fatalf("event published %d times after release, want 0", n)
	}
}

// TestSessionBusRegistry_MatchBeforeDeleteProtectsReplacement is the
// mutation-proof match-before-delete test: a stale release (from an
// earlier registration under the same session id) must NOT unbind a later
// replacement registration for that same id.
//
// Mutation proof: replacing the `if sessionBuses.m[sessionID] == bus`
// guard in RegisterSessionBus's release closure with an unconditional
// delete makes this test fail (busB stops receiving after releaseA fires).
func TestSessionBusRegistry_MatchBeforeDeleteProtectsReplacement(t *testing.T) {
	busA := events.New()
	t.Cleanup(busA.Close)
	busB := events.New()
	t.Cleanup(busB.Close)

	releaseA := RegisterSessionBus("sess-swap", busA)
	releaseB := RegisterSessionBus("sess-swap", busB) // replaces busA's binding
	t.Cleanup(releaseB)

	// The stale release must be a safe no-op: it must NOT unbind busB's
	// live registration.
	releaseA()

	wait := collectOne(t, busB, events.KindSubagentStart)
	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "x",
		Origin: agent.EventOrigin{SessionID: "sess-swap", TurnID: "t"},
	})
	ev := wait()
	if ev.SessionID != "sess-swap" {
		t.Fatalf("busB stopped receiving after the stale releaseA fired: match-before-delete is broken")
	}
}

// TestSessionBusRegistry_ConcurrentTeardown drives ReleaseLeases-shaped
// concurrent teardown: many goroutines calling the SAME release func (and a
// second independent release for a different session) at once must not
// panic and must not double-delete another session's binding. Run with
// -race.
func TestSessionBusRegistry_ConcurrentTeardown(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)
	other := events.New()
	t.Cleanup(other.Close)

	release := RegisterSessionBus("sess-concurrent", bus)
	releaseOther := RegisterSessionBus("sess-concurrent-other", other)
	t.Cleanup(releaseOther)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release()
		}()
	}
	wg.Wait()

	// The other session's binding must be untouched.
	wait := collectOne(t, other, events.KindSubagentStart)
	emitSubagentProgress(agent.Event{
		Kind: agent.EventSubagentStart, Name: "x",
		Origin: agent.EventOrigin{SessionID: "sess-concurrent-other", TurnID: "t"},
	})
	ev := wait()
	if ev.SessionID != "sess-concurrent-other" {
		t.Fatal("concurrent release of an unrelated session corrupted this session's binding")
	}
}

// TestSessionBusRegistry_ReplaceReleaseThenReplacementRelease exercises the
// same-session replace/release ordering the ordering test at package cli
// mirrors, purely at the registry level: releasing the CURRENT binding
// (not a stale one) always succeeds.
func TestSessionBusRegistry_ReplaceReleaseThenReplacementRelease(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)

	release := RegisterSessionBus("sess-rebind", bus)
	release() // idempotent, first call

	if got := LookupSessionBus("sess-rebind"); got != nil {
		t.Fatalf("LookupSessionBus after release = %v, want nil", got)
	}

	// Calling release again must not panic (idempotent).
	release()

	// Re-registering under the same id after a full release works cleanly.
	bus2 := events.New()
	t.Cleanup(bus2.Close)
	release2 := RegisterSessionBus("sess-rebind", bus2)
	t.Cleanup(release2)
	if got := LookupSessionBus("sess-rebind"); got != bus2 {
		t.Fatalf("LookupSessionBus after rebind = %v, want bus2", got)
	}
}

// TestSessionBusRegistry_LookupMiss just documents LookupSessionBus's
// nil-on-miss contract directly, without going through emitSubagentProgress.
func TestSessionBusRegistry_LookupMiss(t *testing.T) {
	if got := LookupSessionBus("definitely-never-registered-" + time.Now().String()); got != nil {
		t.Fatalf("LookupSessionBus for an unregistered id = %v, want nil", got)
	}
}

// TestSessionBusRegistry_TeardownOrderProtectsALaterRebind verifies defer-unwind
// order during session teardown:
//
// 1. dispatchChatSurface (chat_command.go) registers the bus and pushes
// `defer RegisterSessionBus(...)()` before calling TUILauncherFunc.
// 2. In TUILauncherFunc (RunTUI), buildApp builds SessionPool; attachSyncLocked
// re-registers the same session ID via SessionBusRegistrar.
// 3. Go unwinds defers LIFO. RunTUI's `defer runner.Pool().ReleaseLeases(ctx)`
// runs inside TUILauncherFunc and completes before dispatchChatSurface's outer defer.
// The pool release (step 2) always runs before the dispatchChatSurface release (step 1).
//
// Match-before-delete prevents a stale release (step 1) from destroying a later
// rebind of the same ID (e.g. ReattachSyncAfterLogin racing shutdown). Without this
// check, an unconditional delete destroys the active binding.
//
// Mutation proof: removing the `sessionBuses.m[sessionID] == bus` guard in
// RegisterSessionBus's release closure breaks this test when the replacement binding
// is destroyed by the stale release.
func TestSessionBusRegistry_TeardownOrderProtectsALaterRebind(t *testing.T) {
	id := "sess-teardown-order"

	busSurface := events.New() // dispatchChatSurface's sess.EventBus
	t.Cleanup(busSurface.Close)

	// Step 1: dispatchChatSurface registers first.
	releaseSurface := RegisterSessionBus(id, busSurface)

	// Step 2: the pool's attachSyncLocked re-registers the same id (in
	// production, through the SAME sess.EventBus pointer - re-registering
	// with the identical bus is exactly what today's wiring does).
	releasePool := RegisterSessionBus(id, busSurface)

	// Step 3: RunTUI's defer ReleaseLeases fires first (nested inside
	// TUILauncherFunc, unwinds before dispatchChatSurface's own defer).
	releasePool()

	// Before the stale surface release runs, a later rebind lands for the
	// same session id - exactly the race match-before-delete exists to
	// survive.
	busReplacement := events.New()
	t.Cleanup(busReplacement.Close)
	releaseReplacement := RegisterSessionBus(id, busReplacement)
	t.Cleanup(releaseReplacement)

	// dispatchChatSurface's own defer fires second: the STALE release for
	// busSurface, which is no longer the current binding.
	releaseSurface()

	if got := LookupSessionBus(id); got != busReplacement {
		t.Fatalf("stale release corrupted a later rebind: LookupSessionBus = %v, want the replacement bus", got)
	}
}
