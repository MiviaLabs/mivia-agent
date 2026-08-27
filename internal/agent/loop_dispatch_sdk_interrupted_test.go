package agent

// Regression tests for interrupted-turn persistence parity on the SDK
// backend (session flip gap 4): a canceled first-step turn must keep
// the user message in loop.Messages, a canceled streaming turn must
// keep the streamed partial, and the success path must not duplicate
// the user message after the pre-append change.

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// cancelFirstStepCompleter fails the first ChatTurn with
// context.Canceled, modeling a turn canceled before any iteration
// completes.
type cancelFirstStepCompleter struct{}

func (c *cancelFirstStepCompleter) Name() string { return "canceler" }
func (c *cancelFirstStepCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (c *cancelFirstStepCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (c *cancelFirstStepCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, context.Canceled
}

// streamingCancelCompleter streams a partial answer into the request's
// StreamWriter, then fails the ChatTurn with context.Canceled.
type streamingCancelCompleter struct{}

func (c *streamingCancelCompleter) Name() string { return "stream-canceler" }
func (c *streamingCancelCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (c *streamingCancelCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (c *streamingCancelCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, "partial answer so far")
	}
	return nil, context.Canceled
}

// TestSDKRunOnceKeepsUserMessageOnFirstStepCancel pins the parity rule
// against runOnceLegacy: a turn canceled before any SDK iteration
// completes (the SDK's hard-fail Result is empty) must still leave the
// user message in loop.Messages so the session persists on disk.
func TestSDKRunOnceKeepsUserMessageOnFirstStepCancel(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: &cancelFirstStepCompleter{}, Tools: reg}
	_, err := loop.Run(context.Background(), "do work", Options{Model: "m"})
	if err == nil {
		t.Fatal("want cancel error")
	}
	found := false
	for _, m := range loop.Messages {
		if m.Role == provider.RoleUser && m.Content == "do work" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user message lost on first-step cancel: %+v", loop.Messages)
	}
}

// TestSDKRunOnceRecordsStreamedPartialOnCancel pins the legacy
// recordInterruptedPartial contract on the SDK path: the partial text a
// canceled streaming turn already emitted through the StreamingWriter
// tee must land in loop.Messages as an assistant message.
func TestSDKRunOnceRecordsStreamedPartialOnCancel(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: &streamingCancelCompleter{}, Tools: reg}
	_, err := loop.Run(context.Background(), "do work", Options{Model: "m", FinalWriter: io.Discard})
	if err == nil {
		t.Fatal("want cancel error")
	}
	found := false
	for _, m := range loop.Messages {
		if m.Role == provider.RoleAssistant && m.Content == "partial answer so far" {
			found = true
		}
	}
	if !found {
		t.Fatalf("streamed partial lost on cancel: %+v", loop.Messages)
	}
}

// TestSDKRunOnceSuccessDoesNotDuplicateUserMessage pins the no-double-
// append rule: after the pre-append of the user message into
// loop.Messages, the history write-back on success must leave exactly
// one copy of the turn's user message.
func TestSDKRunOnceSuccessDoesNotDuplicateUserMessage(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	comp := &scriptedTurnCompleter{steps: []provider.Response{{Content: "ok", FinishReason: "stop"}}}
	loop := &Loop{Completer: comp, Tools: reg, Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "earlier", CreatedAt: time.Now()},
	}}
	if _, err := loop.Run(context.Background(), "next", Options{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range loop.Messages {
		if m.Role == provider.RoleUser && m.Content == "next" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("user message appears %d times, want 1: %+v", count, loop.Messages)
	}
}
