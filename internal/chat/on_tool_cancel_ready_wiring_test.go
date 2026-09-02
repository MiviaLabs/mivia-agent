package chat

import (
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestBuildAgentTurnOptionsForwardsOnToolCancelReady pins the wiring
// TurnOptions.OnToolCancelReady -> agent.Options.OnToolCancelReady inside
// buildAgentTurnOptions: when the caller-supplied *TurnOptions carries a
// non-nil hook, it must be copied onto the built agent.Options rather than
// silently dropped (see session.go's buildAgentTurnOptions, the `if turn !=
// nil && turn.OnToolCancelReady != nil` guard).
func TestBuildAgentTurnOptionsForwardsOnToolCancelReady(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "test-model"}, &fakeCompleter{out: "ok"})
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	called := false
	hook := func(agent.ToolCanceler) { called = true }
	turn := &TurnOptions{OnToolCancelReady: hook}

	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, turn)
	if opts.OnToolCancelReady == nil {
		t.Fatal("agent.Options.OnToolCancelReady is nil, want the turn's hook forwarded")
	}
	opts.OnToolCancelReady(func(string) bool { return true })
	if !called {
		t.Fatal("agent.Options.OnToolCancelReady did not forward to the turn's underlying hook")
	}
}

// TestBuildAgentTurnOptionsNilTurnLeavesOnToolCancelReadyUnset proves the
// nil-turn case (the plain path, or an agent turn started with no
// TurnOptions) leaves agent.Options.OnToolCancelReady nil rather than
// panicking on a nil dereference of turn.
func TestBuildAgentTurnOptionsNilTurnLeavesOnToolCancelReadyUnset(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "test-model"}, &fakeCompleter{out: "ok"})
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, nil)
	if opts.OnToolCancelReady != nil {
		t.Fatal("agent.Options.OnToolCancelReady should be nil when turn is nil")
	}
}

// TestBuildAgentTurnOptionsNilHookLeavesOnToolCancelReadyUnset proves a
// non-nil *TurnOptions whose OnToolCancelReady field is nil also leaves
// agent.Options.OnToolCancelReady nil - the guard checks the field, not
// just the pointer.
func TestBuildAgentTurnOptionsNilHookLeavesOnToolCancelReadyUnset(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "test-model"}, &fakeCompleter{out: "ok"})
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	turn := &TurnOptions{}
	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, turn)
	if opts.OnToolCancelReady != nil {
		t.Fatal("agent.Options.OnToolCancelReady should be nil when turn.OnToolCancelReady is nil")
	}
}
