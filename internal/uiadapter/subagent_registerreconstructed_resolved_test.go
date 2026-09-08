package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestHasResolvedMatch pins hasResolvedMatch's gate: BOTH c.resolved and a
// non-empty, EQUAL sourceToolCallsRef are required - matching ref alone, or
// resolved alone, must never report true. See registerReconstructed's
// carry-forward check (subagent.go) which relies on this gate to decide
// whether an incoming reconstruction may be dropped in favor of an
// already-resolved existing one.
func TestHasResolvedMatch(t *testing.T) {
	t.Run("resolved with matching ref reports true", func(t *testing.T) {
		c := newReconstructedConversation("t", ports.ModelInfo{Name: "t"}, nil)
		c.setPendingToolCalls("ref-1", nil)
		c.mu.Lock()
		c.resolved = true
		c.mu.Unlock()

		if !c.hasResolvedMatch("ref-1") {
			t.Error("expected hasResolvedMatch(\"ref-1\") to be true")
		}
	})

	t.Run("resolved with different ref reports false", func(t *testing.T) {
		c := newReconstructedConversation("t", ports.ModelInfo{Name: "t"}, nil)
		c.setPendingToolCalls("ref-1", nil)
		c.mu.Lock()
		c.resolved = true
		c.mu.Unlock()

		if c.hasResolvedMatch("different-ref") {
			t.Error("expected hasResolvedMatch(\"different-ref\") to be false")
		}
	})

	t.Run("unresolved conversation with matching ref reports false", func(t *testing.T) {
		c := newReconstructedConversation("t", ports.ModelInfo{Name: "t"}, nil)
		c.setPendingToolCalls("ref-1", nil)
		// resolved deliberately left false - freshly constructed, no manual
		// mutation.
		if c.hasResolvedMatch("ref-1") {
			t.Error("expected hasResolvedMatch to require resolved==true, not just a matching ref")
		}
	})

	t.Run("empty ref never counts as a match even when resolved", func(t *testing.T) {
		c := newReconstructedConversation("t", ports.ModelInfo{Name: "t"}, nil)
		c.mu.Lock()
		c.resolved = true
		c.mu.Unlock()
		// sourceToolCallsRef left at its zero value, "".

		if c.hasResolvedMatch("") {
			t.Error("expected hasResolvedMatch(\"\") to be false when sourceToolCallsRef is empty")
		}
	})
}

// TestRegisterReconstructed_CarriesForwardResolvedMatch is the carry-forward
// case: an existing, ALREADY-RESOLVED reconstruction under key must survive
// a later registerReconstructed call for an incoming reconstruction sharing
// the same sourceToolCallsRef - the incoming one is dropped, not installed,
// and the resolved content already fetched is not discarded.
func TestRegisterReconstructed_CarriesForwardResolvedMatch(t *testing.T) {
	threads := NewSubagentThreads()

	first := newReconstructedConversation("first", ports.ModelInfo{Name: "first"}, []ports.Message{
		{Role: "assistant", Text: "resolved content"},
	})
	first.setPendingToolCalls("ref-1", nil)
	first.mu.Lock()
	first.resolved = true
	first.mu.Unlock()

	threads.registerReconstructed("k", first)

	got, ok := threads.Thread("k")
	if !ok {
		t.Fatalf("expected a thread under k")
	}
	if got != ports.Conversation(first) {
		t.Fatalf("expected first to be registered under k before the carry-forward attempt")
	}

	second := newReconstructedConversation("second", ports.ModelInfo{Name: "second"}, []ports.Message{
		{Role: "assistant", Text: "fresh unresolved placeholder"},
	})
	second.setPendingToolCalls("ref-1", nil)

	threads.registerReconstructed("k", second)

	got, ok = threads.Thread("k")
	if !ok {
		t.Fatalf("expected a thread under k after carry-forward attempt")
	}
	if got != ports.Conversation(first) {
		t.Errorf("expected the existing resolved conversation to be retained, got a different object installed")
	}
	hist := got.History()
	if len(hist) != 1 || hist[0].Text != "resolved content" {
		t.Errorf("expected resolved content preserved, got %+v", hist)
	}
}

// TestRegisterReconstructed_DifferentRefStillReplaces proves the carry-forward
// check does NOT apply when the incoming reconstruction's ref differs from
// the existing resolved one's ref (or is empty) - the incoming one still
// wins, exactly as registerReconstructed behaved before this change.
func TestRegisterReconstructed_DifferentRefStillReplaces(t *testing.T) {
	t.Run("different non-empty ref", func(t *testing.T) {
		threads := NewSubagentThreads()

		first := newReconstructedConversation("first", ports.ModelInfo{Name: "first"}, nil)
		first.setPendingToolCalls("ref-1", nil)
		first.mu.Lock()
		first.resolved = true
		first.mu.Unlock()
		threads.registerReconstructed("k", first)

		second := newReconstructedConversation("second", ports.ModelInfo{Name: "second"}, nil)
		second.setPendingToolCalls("ref-2", nil)
		threads.registerReconstructed("k", second)

		got, ok := threads.Thread("k")
		if !ok {
			t.Fatalf("expected a thread under k")
		}
		if got != ports.Conversation(second) {
			t.Errorf("expected the second (different ref) conversation to replace the first")
		}
	})

	t.Run("empty incoming ref", func(t *testing.T) {
		threads := NewSubagentThreads()

		first := newReconstructedConversation("first", ports.ModelInfo{Name: "first"}, nil)
		first.setPendingToolCalls("ref-1", nil)
		first.mu.Lock()
		first.resolved = true
		first.mu.Unlock()
		threads.registerReconstructed("k", first)

		second := newReconstructedConversation("second", ports.ModelInfo{Name: "second"}, nil)
		// second carries no ref at all - zero value "".
		threads.registerReconstructed("k", second)

		got, ok := threads.Thread("k")
		if !ok {
			t.Fatalf("expected a thread under k")
		}
		if got != ports.Conversation(second) {
			t.Errorf("expected the second (empty ref) conversation to replace the first")
		}
	})
}

// TestRegisterReconstructed_SameRefButNeverResolvedStillReplaces proves the
// carry-forward gate requires a genuinely RESOLVED existing conversation,
// not merely a matching ref: an existing reconstruction sharing the
// incoming ref but never resolved (still pending, or a failed attempt) must
// still be replaced normally.
func TestRegisterReconstructed_SameRefButNeverResolvedStillReplaces(t *testing.T) {
	t.Run("existing still pending", func(t *testing.T) {
		threads := NewSubagentThreads()

		first := newReconstructedConversation("first", ports.ModelInfo{Name: "first"}, nil)
		first.setPendingToolCalls("ref-1", nil)
		// resolved left false: genuinely pending.
		threads.registerReconstructed("k", first)

		second := newReconstructedConversation("second", ports.ModelInfo{Name: "second"}, nil)
		second.setPendingToolCalls("ref-1", nil)
		threads.registerReconstructed("k", second)

		got, ok := threads.Thread("k")
		if !ok {
			t.Fatalf("expected a thread under k")
		}
		if got != ports.Conversation(second) {
			t.Errorf("expected the incoming conversation to replace a never-resolved existing one")
		}
	})

	t.Run("existing failed a resolve attempt", func(t *testing.T) {
		threads := NewSubagentThreads()

		first := newReconstructedConversation("first", ports.ModelInfo{Name: "first"}, nil)
		first.setPendingToolCalls("ref-1", nil)
		first.mu.Lock()
		first.resolveAttempted = true // failed, resolved stays false
		first.mu.Unlock()
		threads.registerReconstructed("k", first)

		second := newReconstructedConversation("second", ports.ModelInfo{Name: "second"}, nil)
		second.setPendingToolCalls("ref-1", nil)
		threads.registerReconstructed("k", second)

		got, ok := threads.Thread("k")
		if !ok {
			t.Fatalf("expected a thread under k")
		}
		if got != ports.Conversation(second) {
			t.Errorf("expected the incoming conversation to replace an existing conversation whose resolve attempt failed")
		}
	})
}
