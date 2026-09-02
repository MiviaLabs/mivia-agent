// conversation_cancel_tool_call_test.go exercises turnHandle.CancelToolCall
// end-to-end through the real chat.Session / agent SDK backend, proving the
// ports.TurnHandle contract this package's turnHandle implements: canceling
// one in-flight tool call by ID ends that call without aborting the turn,
// and a miss (unknown ID, nothing in flight) is a harmless no-op.
package uiadapter_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// blockingUITool is a CLI tool whose Execute blocks until ctx is done, used
// to give the test a deterministic window in which a tool call is "running"
// on the wire (KindToolStart already emitted, no KindToolEnd yet) for
// CancelToolCall to act on.
type blockingUITool struct{ entered chan struct{} }

func newBlockingUITool() *blockingUITool { return &blockingUITool{entered: make(chan struct{})} }

func (b *blockingUITool) Name() string               { return "blocking_tool" }
func (b *blockingUITool) Description() string        { return "blocks until canceled" }
func (b *blockingUITool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (b *blockingUITool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (b *blockingUITool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	close(b.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

// TestCancelToolCall_CancelsRunningCallWithoutEndingTheTurn drives a real
// turn through one blocking tool call, cancels it mid-flight by its call ID
// once its KindToolStart has been observed, and asserts: the tool call
// reaches a terminal state (KindToolEnd) instead of hanging forever, the
// turn itself completes normally (KindTurnEnd with reason "completed", not
// "cancelled"), and the whole thing happens well inside the test timeout -
// which it would not if the cancellation had no effect on the blocked call.
func TestCancelToolCall_CancelsRunningCallWithoutEndingTheTurn(t *testing.T) {
	blocking := newBlockingUITool()
	comp := &scriptedCompleter{turns: []provider.Response{
		toolResponse("tc1", "blocking_tool", "{}"),
		assistantResponse("done"),
	}}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(blocking)
	conv := uiadapter.NewConversation(sess)

	handle, err := conv.Send(context.Background(), intent.Send{Text: "run it"})
	if err != nil {
		t.Fatalf("Send err=%v", err)
	}

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocking tool never started executing")
	}

	callID := waitForToolStartCallID(t, handle)

	if !handle.CancelToolCall(callID) {
		t.Fatal("CancelToolCall reported no match for the running call")
	}

	var sawToolEnd, sawTurnEndCompleted bool
	drainDeadline := time.After(5 * time.Second)
drain:
	for {
		select {
		case ev, ok := <-handle.Events():
			if !ok {
				break drain
			}
			switch ev.Kind {
			case uievent.KindToolEnd:
				sawToolEnd = true
			case uievent.KindTurnEnd:
				body, _ := ev.Body.(uievent.TurnEndBody)
				sawTurnEndCompleted = body.Reason == "completed"
			}
		case <-drainDeadline:
			t.Fatal("timed out draining events after CancelToolCall; the " +
				"blocked call was not actually released")
		}
	}
	if !sawToolEnd {
		t.Fatal("no KindToolEnd observed for the canceled call")
	}
	if !sawTurnEndCompleted {
		t.Fatal("the turn did not end with reason \"completed\" - canceling " +
			"one tool call must not turn the whole turn into a cancellation")
	}
}

// waitForToolStartCallID drains handle's events until a KindToolStart
// arrives and returns its tool call ID, failing the test on timeout, a
// closed channel, or a malformed body.
func waitForToolStartCallID(t *testing.T, handle ports.TurnHandle) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-handle.Events():
			if !ok {
				t.Fatal("channel closed before a KindToolStart event arrived")
			}
			if ev.Kind != uievent.KindToolStart {
				continue
			}
			body, ok := ev.Body.(uievent.ToolStartBody)
			if !ok {
				t.Fatalf("KindToolStart body has the wrong type: %T", ev.Body)
			}
			if body.ToolCallID == "" {
				t.Fatal("KindToolStart carried no tool call ID")
			}
			return body.ToolCallID
		case <-deadline:
			t.Fatal("timed out waiting for KindToolStart")
			return ""
		}
	}
}

// TestCancelToolCall_UnknownIDIsANoOp proves CancelToolCall never panics
// and simply reports false for an ID that matches nothing, both before any
// turn has run (no registry exists yet) and once one has completed (the
// registry exists but is empty of that ID).
func TestCancelToolCall_UnknownIDIsANoOp(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("done")}}
	conv := newTestConversation(t, comp)

	handle, err := conv.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatalf("Send err=%v", err)
	}
	drainUntilClose(t, handle.Events(), 5*time.Second)

	if handle.CancelToolCall("does-not-exist") {
		t.Fatal("CancelToolCall reported a match for an unknown call ID")
	}
	if handle.CancelToolCall("") {
		t.Fatal("CancelToolCall reported a match for a blank call ID")
	}
}
