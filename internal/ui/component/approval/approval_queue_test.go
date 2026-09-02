package approval

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// The agent runs tool calls in parallel, and each gated call blocks its own
// goroutine until the operator answers it. This model held ONE request and
// replaced it on arrival, so a second prompt discarded the first - and the
// gate behind the discarded one waited for a decision that could no longer be
// made. It also cleared on ANY tool start or end, so an unrelated call
// finishing dismissed a prompt the operator was still reading.
//
// Both shapes end the same way: a session that looks idle and is actually
// blocked forever.

func pending(id, name string) uievent.ToolPendingBody {
	return uievent.ToolPendingBody{ToolCallID: id, Name: name}
}

func newModel() Model { return New(theme.Theme{}, theme.TierASCII) }

// press sends one key and returns the decision it emitted, if any.
func press(t *testing.T, m Model, key string) (Model, *DecisionMsg) {
	t.Helper()
	m, cmd := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	if cmd == nil {
		return m, nil
	}
	msg, ok := cmd().(DecisionMsg)
	if !ok {
		return m, nil
	}
	return m, &msg
}

// TestASecondRequestDoesNotDiscardTheFirst is the parallel-call defect.
func TestASecondRequestDoesNotDiscardTheFirst(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("call-1", "edit_file"))
	m.SetRequest(pending("call-2", "run_command"))

	m, first := press(t, m, "o")
	if first == nil {
		t.Fatal("no decision was emitted at all")
	}
	if first.ToolCallID != "call-1" {
		t.Errorf("answered %q first, want call-1: the operator answers what they "+
			"were shown, and the first prompt was on screen", first.ToolCallID)
	}
	if !m.Active() {
		t.Fatal("the second request vanished with the first; its gate waits for a " +
			"decision that can no longer be made")
	}

	_, second := press(t, m, "o")
	if second == nil || second.ToolCallID != "call-2" {
		t.Errorf("second decision = %v, want call-2", second)
	}
}

// TestAnotherCallStartingDoesNotDismissThePrompt is the blanket-clear defect.
// A parallel call reaching tool.start must not dismiss a prompt for a
// different call that is still waiting.
func TestAnotherCallStartingDoesNotDismissThePrompt(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("call-1", "edit_file"))

	m.Resolve("call-2")

	if !m.Active() {
		t.Fatal("an unrelated call dismissed the prompt; the operator is left with " +
			"no way to answer, and the gate blocks for ever")
	}
}

// TestTheAnsweredCallsOwnStartClearsIt: the call the operator approved does
// reach tool.start, and its prompt must go.
func TestTheAnsweredCallsOwnStartClearsIt(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("call-1", "edit_file"))
	m.SetRequest(pending("call-2", "run_command"))

	m.Resolve("call-1")

	if !m.Active() {
		t.Fatal("resolving the head left nothing pending, but call-2 is still waiting")
	}
	_, got := press(t, m, "o")
	if got == nil || got.ToolCallID != "call-2" {
		t.Errorf("after the head resolved, the prompt shows %v, want call-2", got)
	}
}

// TestResolvingAQueuedCallLeavesTheHeadAlone: a call answered elsewhere (a
// standing decision, a timeout) must be removed from the queue without
// disturbing what the operator is currently reading.
func TestResolvingAQueuedCallLeavesTheHeadAlone(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("call-1", "edit_file"))
	m.SetRequest(pending("call-2", "run_command"))
	m.SetRequest(pending("call-3", "delete_file"))

	m.Resolve("call-2")

	m, first := press(t, m, "o")
	if first == nil || first.ToolCallID != "call-1" {
		t.Fatalf("the head changed under the operator: %v", first)
	}
	_, next := press(t, m, "o")
	if next == nil || next.ToolCallID != "call-3" {
		t.Errorf("next = %v, want call-3 (call-2 was resolved elsewhere)", next)
	}
}

// TestClearAllDropsEveryPendingRequest is the turn-ended case: nothing can be
// answered any more, so nothing should be shown.
func TestClearAllDropsEveryPendingRequest(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("call-1", "edit_file"))
	m.SetRequest(pending("call-2", "run_command"))

	m.ClearAll()

	if m.Active() {
		t.Error("a prompt survived the end of the turn that produced it")
	}
}

// TestTheSameCallDoesNotQueueTwice guards against a duplicate pending event
// stacking two prompts for one gate, which would leave a ghost the operator
// answers into nothing.
func TestTheSameCallDoesNotQueueTwice(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("call-1", "edit_file"))
	m.SetRequest(pending("call-1", "edit_file"))

	m, _ = press(t, m, "o")
	if m.Active() {
		t.Error("one call armed two prompts; the second has no gate behind it")
	}
}

// TestDecisionsStillCarryTheirKeys keeps the existing key contract intact
// while the queue is added underneath it.
func TestDecisionsStillCarryTheirKeys(t *testing.T) {
	for key, want := range map[string]ports.Decision{
		"o": ports.DecisionOnce,
		"a": ports.DecisionAlways,
		"d": ports.DecisionDeny,
	} {
		m := newModel()
		m.SetRequest(pending("call-1", "edit_file"))
		if _, got := press(t, m, key); got == nil || got.Decision != want {
			t.Errorf("key %q produced %v, want %v", key, got, want)
		}
	}
}

// Model is passed BY VALUE between the foreground screen and its per-session
// states (internal/ui/screen/conversation/session.go copies it in both
// directions). A method that mutates the queue in place therefore writes
// through an array a second live Model still holds, and the two disagree about
// what is pending.
//
// Nothing reads the stale copy today - every router guards on session id, and
// the map entry is rewritten on every switch-away - so this is latent. It is
// also one refactor from resurrecting a resolved prompt or dropping a queued
// one, and a dropped prompt is a tool-call gate that never returns.

func idsOf(m Model) []string {
	out := make([]string, 0, len(m.pending))
	for _, req := range m.pending {
		out = append(out, req.ToolCallID)
	}
	return out
}

// TestResolveDoesNotReachIntoACopiedModel: an in-place removal reslices the
// shared array, so the copy sees a duplicated tail.
func TestResolveDoesNotReachIntoACopiedModel(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("c1", "edit_file"))
	m.SetRequest(pending("c2", "run_command"))
	m.SetRequest(pending("c3", "delete_file"))

	copied := m
	m.Resolve("c2")

	if got := idsOf(copied); len(got) != 3 || got[1] != "c2" {
		t.Errorf("the copy's queue was rewritten by a Resolve on the original: %v", got)
	}
}

// TestSetRequestDoesNotReachIntoACopiedModel: appending into a shared array
// overwrites whatever the other header queued at that index.
func TestSetRequestDoesNotReachIntoACopiedModel(t *testing.T) {
	m := newModel()
	// Three first, deliberately: a plain append only writes through a SHARED
	// array when there is spare capacity, and Go's growth leaves spare
	// capacity at three. With one element the bug hides behind a
	// reallocation, and a test that queues one proves nothing.
	m.SetRequest(pending("c1", "edit_file"))
	m.SetRequest(pending("c2", "run_command"))
	m.SetRequest(pending("c3", "delete_file"))

	copied := m
	copied.SetRequest(pending("c9", "run_command"))
	m.SetRequest(pending("c4", "delete_file"))

	if got := idsOf(copied); len(got) != 4 || got[3] != "c9" {
		t.Errorf("the copy's own queued call was overwritten by the original: %v", got)
	}
	if got := idsOf(m); len(got) != 4 || got[3] != "c4" {
		t.Errorf("the original's queued call was overwritten by the copy: %v", got)
	}
}

// TestHeadDoesNotHandOutAPointerIntoTheQueue closes the fourth route. head()
// returned &m.pending[0] from a VALUE receiver - a pointer into an array every
// copy shares.
func TestHeadDoesNotHandOutAPointerIntoTheQueue(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("c1", "edit_file"))

	copied := m
	if h := m.head(); h != nil {
		h.ToolCallID = "MUTATED"
	}

	if got := idsOf(copied); got[0] != "c1" {
		t.Errorf("a write through head() reached a copy's queue: %v", got)
	}
	if got := idsOf(m); got[0] != "c1" {
		t.Errorf("a write through head() reached this Model's own queue: %v", got)
	}
}

// TestUpdateDoesNotReachIntoACopiedModel guards the one mutator that was
// already safe, so a later simplification cannot quietly undo it.
func TestUpdateDoesNotReachIntoACopiedModel(t *testing.T) {
	m := newModel()
	m.SetRequest(pending("c1", "edit_file"))
	m.SetRequest(pending("c2", "run_command"))

	copied := m
	m, _ = press(t, m, "o")
	_ = m

	if got := idsOf(copied); len(got) != 2 || got[0] != "c1" {
		t.Errorf("answering a prompt rewrote a copy's queue: %v", got)
	}
}
