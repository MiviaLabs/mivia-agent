// conversation_background_test.go pins Send's background gate: a
// background-marked Conversation must not touch the process-wide
// SubagentProgressRegistrar, so a background automation run cannot
// hijack the foreground session's subagent-dispatch display.
package uiadapter_test

import (
	"context"
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
