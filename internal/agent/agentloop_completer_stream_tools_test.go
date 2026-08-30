package agent

import (
	"context"
	"io"
	"testing"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// The wrapper's ChatStream used to call the CLI's plain ChatStream, which
// returns content only: every tool call the model made was dropped and the
// finish reason was hardcoded to "stop", so a turn that acted was
// indistinguishable from a turn that decided to stop. These tests pin the
// tool-call-bearing path.

// streamingToolCompleter answers ChatTurn with a turn that both spoke and
// acted, and records whether the plain ChatStream path was taken instead.
type streamingToolCompleter struct {
	plainStreamCalls int
	streamWriter     io.Writer
}

func (c *streamingToolCompleter) Name() string { return "streaming-tool" }

func (c *streamingToolCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}

func (c *streamingToolCompleter) ChatStream(_ context.Context, _ provider.Request, _ io.Writer) (string, error) {
	c.plainStreamCalls++
	return "fallback", nil
}

func (c *streamingToolCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.streamWriter = req.StreamWriter
	call := provider.ToolCall{ID: "call_1", Type: "function"}
	call.Function.Name = "dispatch_tasks"
	call.Function.Arguments = `{"tasks":[]}`
	return &provider.Response{
		Content:      "spawning agents",
		FinishReason: "tool_calls",
		ToolCalls:    []provider.ToolCall{call},
		TokenUsage:   provider.TokenUsage{Reported: true, InputTokens: 11, OutputTokens: 7},
	}, nil
}

func drainChunks(t *testing.T, ch <-chan sdkshape.Chunk) []sdkshape.Chunk {
	t.Helper()
	var got []sdkshape.Chunk
	for c := range ch {
		got = append(got, c)
	}
	return got
}

// TestChatStreamCarriesToolCalls is the regression: a streamed turn that
// called a tool must surface that call and the real finish reason.
func TestChatStreamCarriesToolCalls(t *testing.T) {
	c := &streamingToolCompleter{}
	w, err := newAgentLoopCompleter(c)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	ch, err := w.ChatStream(context.Background(), sdkshape.Request{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	got := drainChunks(t, ch)
	if c.plainStreamCalls != 0 {
		t.Fatalf("ChatTurn answered, so the plain stream path must not run (%d calls)", c.plainStreamCalls)
	}
	if len(got) != 3 {
		t.Fatalf("want content + tool call + done, got %d chunks: %+v", len(got), got)
	}
	if got[0].Delta != "spawning agents" {
		t.Fatalf("content chunk = %q", got[0].Delta)
	}
	if got[1].ToolCallDelta == nil || got[1].ToolCallDelta.Name != "dispatch_tasks" {
		t.Fatalf("tool call not emitted: %+v", got[1])
	}
	if string(got[1].ToolCallDelta.Arguments) != `{"tasks":[]}` {
		t.Fatalf("tool call arguments lost: %q", got[1].ToolCallDelta.Arguments)
	}
	if !got[2].Done || got[2].FinishReason != "tool_calls" {
		t.Fatalf("terminal chunk = %+v, want done with the real finish reason", got[2])
	}
	if got[2].Usage.TotalTokens != 18 {
		t.Fatalf("usage lost: %+v", got[2].Usage)
	}
}

// TestChatStreamReportsRealFinishReason pins that the recorded finish
// reason is the provider's, not the hardcoded "stop" the old code reported
// for every streamed turn.
func TestChatStreamReportsRealFinishReason(t *testing.T) {
	var recorded string
	w, err := newAgentLoopCompleterWithDefaults(
		&streamingToolCompleter{}, turnRequestDefaults{},
		func(finish string) { recorded = finish }, nil, nil,
	)
	if err != nil {
		t.Fatalf("newAgentLoopCompleterWithDefaults: %v", err)
	}
	ch, err := w.ChatStream(context.Background(), sdkshape.Request{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	drainChunks(t, ch)
	if recorded != "tool_calls" {
		t.Fatalf("recorded finish reason = %q, want %q", recorded, "tool_calls")
	}
}

// TestChatStreamSuppressesDeltaForLiveWriter pins that a turn whose content
// already went to a live writer byte by byte is not also replayed as one
// delta chunk, which would duplicate the answer.
func TestChatStreamSuppressesDeltaForLiveWriter(t *testing.T) {
	c := &streamingToolCompleter{}
	w, err := newAgentLoopCompleter(c)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	ch, err := w.ChatStream(context.Background(), sdkshape.Request{StreamingWriter: io.Discard})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	got := drainChunks(t, ch)
	if c.streamWriter == nil {
		t.Fatal("the live writer must reach the CLI request")
	}
	for _, chunk := range got {
		if chunk.Delta != "" {
			t.Fatalf("content was replayed as a delta chunk: %q", chunk.Delta)
		}
	}
	if len(got) != 2 || !got[1].Done {
		t.Fatalf("want tool call + done, got %+v", got)
	}
}
