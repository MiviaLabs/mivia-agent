package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// fakeCompleter satisfies provider.Completer with stub return values.
// Each field can be set per-test; the test overrides only what it needs.
type fakeCompleter struct {
	name        string
	chatOut     string
	chatErr     error
	chatTurnOut *provider.Response
	chatTurnErr error
	// blocksChat is set to a non-nil channel to make Chat block until
	// the channel is closed (for cancellation tests).
	blocksChat chan struct{}
	// onChatTurn, when non-nil, runs inside ChatTurn before the
	// configured return value, letting tests observe that a turn has
	// actually started (used to flip signal-branch predicates from
	// the test goroutine).
	onChatTurn func()
}

func (f *fakeCompleter) Name() string { return f.name }
func (f *fakeCompleter) Chat(ctx context.Context, _ provider.Request) (string, error) {
	if f.blocksChat != nil {
		// A real completer honors ctx cancellation while blocked; the
		// fake must too, or the wrapper's goroutine deadlocks when the
		// test cancels the context mid-call.
		select {
		case <-f.blocksChat:
			return "", context.Canceled
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.chatOut, f.chatErr
}
func (f *fakeCompleter) ChatStream(_ context.Context, _ provider.Request, _ io.Writer) (string, error) {
	return f.chatOut, f.chatErr
}
func (f *fakeCompleter) ChatTurn(_ context.Context, _ provider.Request) (*provider.Response, error) {
	if f.onChatTurn != nil {
		f.onChatTurn()
	}
	return f.chatTurnOut, f.chatTurnErr
}

// TestAgentLoopCompleterName asserts Name forwards.
func TestAgentLoopCompleterName(t *testing.T) {
	f := &fakeCompleter{name: "test-model"}
	w, err := newAgentLoopCompleter(f)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	if got := w.Name(); got != "test-model" {
		t.Fatalf("Name() = %q, want %q", got, "test-model")
	}
}

// TestAgentLoopCompleterChatReturnsContent asserts Chat uses ChatTurn
// when ChatTurn returns a non-nil response.
func TestAgentLoopCompleterChatReturnsContent(t *testing.T) {
	f := &fakeCompleter{
		chatTurnOut: &provider.Response{Content: "hello", FinishReason: "stop"},
	}
	w, err := newAgentLoopCompleter(f)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	got, err := w.Chat(context.Background(), sdkshape.Request{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got.Message.Content != "hello" {
		t.Fatalf("Content = %q, want %q", got.Message.Content, "hello")
	}
	if got.Message.Role != sdkshape.RoleAssistant {
		t.Fatalf("Role = %q, want %q", got.Message.Role, sdkshape.RoleAssistant)
	}
	if got.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want %q", got.FinishReason, "stop")
	}
}

// TestAgentLoopCompleterChatSurfacesToolCalls asserts the wrapper
// preserves tool calls from ChatTurn through the SDK Response.
func TestAgentLoopCompleterChatSurfacesToolCalls(t *testing.T) {
	f := &fakeCompleter{
		chatTurnOut: &provider.Response{
			ToolCalls: []provider.ToolCall{
				{ID: "call-1", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"/x"}`}},
			},
			FinishReason: "tool_calls",
		},
	}
	w, err := newAgentLoopCompleter(f)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	got, err := w.Chat(context.Background(), sdkshape.Request{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(got.ToolCalls))
	}
	if got.ToolCalls[0].ID != "call-1" {
		t.Fatalf("ToolCall.ID = %q, want %q", got.ToolCalls[0].ID, "call-1")
	}
	if got.ToolCalls[0].Name != "read_file" {
		t.Fatalf("ToolCall.Name = %q, want %q", got.ToolCalls[0].Name, "read_file")
	}
	if string(got.ToolCalls[0].Arguments) != `{"path":"/x"}` {
		t.Fatalf("ToolCall.Arguments = %q", got.ToolCalls[0].Arguments)
	}
	if got.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want %q", got.FinishReason, "tool_calls")
	}
}

// TestAgentLoopCompleterChatAssignsIdentifiedIDsWhenEmpty asserts that tool calls
// missing IDs are given deterministic identifiers so that tool lifecycle events
// and UI transcript blocks pair correctly.
func TestAgentLoopCompleterChatAssignsIdentifiedIDsWhenEmpty(t *testing.T) {
	f := &fakeCompleter{
		chatTurnOut: &provider.Response{
			ToolCalls: []provider.ToolCall{
				{ID: "", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "memory_search", Arguments: `{"query":"test"}`}},
			},
			FinishReason: "tool_calls",
		},
	}
	w, err := newAgentLoopCompleter(f)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	got, err := w.Chat(context.Background(), sdkshape.Request{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(got.ToolCalls))
	}
	if got.ToolCalls[0].ID == "" {
		t.Fatal("expected non-empty ID assigned to tool call")
	}
	if !strings.HasPrefix(got.ToolCalls[0].ID, "call_") {
		t.Fatalf("ToolCall.ID = %q, want prefix call_", got.ToolCalls[0].ID)
	}
}

// TestAgentLoopCompleterChatReturnsError asserts Chat returns the
// wrapped error from ChatTurn.
func TestAgentLoopCompleterChatReturnsError(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeCompleter{chatTurnErr: boom}
	w, err := newAgentLoopCompleter(f)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	_, err = w.Chat(context.Background(), sdkshape.Request{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wraps %v", err, boom)
	}
}

// TestAgentLoopCompleterChatStreamEmitsContentAndDone asserts
// ChatStream emits one content chunk then one done chunk then closes.
func TestAgentLoopCompleterChatStreamEmitsContentAndDone(t *testing.T) {
	f := &fakeCompleter{chatOut: "streamed"}
	w, err := newAgentLoopCompleter(f)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	ch, err := w.ChatStream(context.Background(), sdkshape.Request{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var got []sdkshape.Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 2 {
		t.Fatalf("chunks = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Delta != "streamed" {
		t.Fatalf("chunk[0].Delta = %q, want %q", got[0].Delta, "streamed")
	}
	if !got[1].Done {
		t.Fatalf("chunk[1].Done = false, want true")
	}
	if got[1].FinishReason != "stop" {
		t.Fatalf("chunk[1].FinishReason = %q, want %q", got[1].FinishReason, "stop")
	}
}

// TestAgentLoopCompleterChatStreamContextCancel asserts the channel
// closes promptly when the context is cancelled.
func TestAgentLoopCompleterChatStreamContextCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := &fakeCompleter{blocksChat: release}
	w, err := newAgentLoopCompleter(f)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := w.ChatStream(ctx, sdkshape.Request{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	// Cancel after a short delay.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	// Drain the channel; it should close without emitting terminal chunk.
	var got []sdkshape.Chunk
	for c := range ch {
		got = append(got, c)
	}
	// We expect at most one chunk (the cancel may have raced the
	// first emit). The key assertion is the channel closed.
	_ = got
}

// TestAgentLoopCompleterChatStreamEmitsError asserts an error from
// Chat produces a Chunk with Err set, then closes.
func TestAgentLoopCompleterChatStreamEmitsError(t *testing.T) {
	boom := errors.New("stream-boom")
	f := &fakeCompleter{chatErr: boom}
	w, err := newAgentLoopCompleter(f)
	if err != nil {
		t.Fatalf("newAgentLoopCompleter: %v", err)
	}
	ch, err := w.ChatStream(context.Background(), sdkshape.Request{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var got []sdkshape.Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 1 {
		t.Fatalf("chunks = %d, want 1", len(got))
	}
	if !errors.Is(got[0].Err, boom) {
		t.Fatalf("chunk[0].Err = %v, want wraps %v", got[0].Err, boom)
	}
}
