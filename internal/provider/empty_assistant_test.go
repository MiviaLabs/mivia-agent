package provider

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// decodeRequestMessages runs a Request through the real wire-encoding path and
// returns the messages exactly as the API would receive them.
func decodeRequestMessages(t *testing.T, req Request) []map[string]any {
	t.Helper()
	c := NewOpenAICompat("test", "https://example.invalid/v1", "k", "", "")
	httpReq, err := c.newRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	raw, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body.Messages
}

// An assistant message with neither content nor tool_calls encodes to a bare
// {"role":"assistant"}, which OpenAI-compatible APIs reject with HTTP 400
// ("content or tool calls must be provided"). One such message poisons a
// session permanently: it is replayed on every later turn, so every subsequent
// request fails until the history is repaired.
func TestNewRequestDropsContentlessAssistantMessage(t *testing.T) {
	req := Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant}, // poisoned turn
			{Role: RoleUser, Content: "still there?"},
		},
	}
	msgs := decodeRequestMessages(t, req)

	for _, m := range msgs {
		if m["role"] != RoleAssistant {
			continue
		}
		_, hasContent := m["content"]
		_, hasCalls := m["tool_calls"]
		if !hasContent && !hasCalls {
			t.Fatalf("assistant message with neither content nor tool_calls reached the wire: %v", msgs)
		}
	}
	if len(msgs) != 3 {
		t.Fatalf("expected the poisoned message dropped, got %d: %v", len(msgs), msgs)
	}
}

// A tool-calling assistant turn legitimately carries no content. Dropping it
// would orphan the tool result that references its tool_call_id.
func TestNewRequestKeepsToolCallAssistantWithoutContent(t *testing.T) {
	var call ToolCall
	call.ID = "c1"
	call.Type = "function"
	call.Function.Name = "read_file"
	call.Function.Arguments = "{}"

	req := Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleUser, Content: "read it"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
			{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data"},
		},
	}
	msgs := decodeRequestMessages(t, req)
	if len(msgs) != 3 {
		t.Fatalf("tool-calling turn must survive, got %d: %v", len(msgs), msgs)
	}
	if _, ok := msgs[1]["tool_calls"]; !ok {
		t.Fatalf("assistant tool_calls lost: %v", msgs[1])
	}
}

// Whitespace-only content is equivalent to empty for this purpose: it encodes
// as a present-but-blank content field some APIs also reject.
func TestNewRequestDropsWhitespaceOnlyAssistantMessage(t *testing.T) {
	req := Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, Content: "   \n"},
			{Role: RoleUser, Content: "again"},
		},
	}
	msgs := decodeRequestMessages(t, req)
	if len(msgs) != 2 {
		t.Fatalf("expected whitespace-only assistant dropped, got %d: %v", len(msgs), msgs)
	}
	_ = strings.TrimSpace("")
}

// CreatedAt is host-only session bookkeeping. It must never reach the API:
// `omitempty` does not suppress a zero time.Time, so the previous "strip
// host-only fields" pass still encoded created_at:"0001-01-01T00:00:00Z" on
// every message of every request.
func TestNewRequestNeverSendsCreatedAt(t *testing.T) {
	req := Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "hi", CreatedAt: time.Now()},
			{Role: RoleAssistant, Content: "hello"},
		},
	}
	for _, m := range decodeRequestMessages(t, req) {
		if _, ok := m["created_at"]; ok {
			t.Fatalf("created_at leaked to the API: %v", m)
		}
	}
}

// A tool result may legitimately be empty (read_file on a zero-byte file).
// Content is omitempty, so that encodes to a tool message with no content field
// at all - which OpenAI-compatible APIs reject the same way they reject a
// contentless assistant message.
func TestNewRequestKeepsEmptyToolResultContent(t *testing.T) {
	var call ToolCall
	call.ID = "c1"
	call.Type = "function"
	call.Function.Name = "read_file"

	req := Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleUser, Content: "read the empty file"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
			{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: ""},
		},
	}
	msgs := decodeRequestMessages(t, req)
	last := msgs[len(msgs)-1]
	if last["role"] != RoleTool {
		t.Fatalf("expected tool message last: %v", msgs)
	}
	if _, ok := last["content"]; !ok {
		t.Fatalf("tool message must always carry a content field: %v", last)
	}
}

// DropEmptyAssistantTurns is the same repair as the wire-layer drop above
// (toAPIMessages), applied to the message list itself so the shape never
// reaches persisted history or provider.ValidateToolPairing in the first
// place - not just the outgoing request.
func TestDropEmptyAssistantTurns_DropsContentlessNoToolCallMessage(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant}, // poisoned turn
		{Role: RoleUser, Content: "still there?"},
	}
	out := DropEmptyAssistantTurns(msgs)
	if len(out) != 3 {
		t.Fatalf("expected the poisoned message dropped, got %d: %v", len(out), out)
	}
	for _, m := range out {
		if m.Role == RoleAssistant {
			t.Fatalf("empty assistant message survived: %v", out)
		}
	}
}

// Whitespace-only content is equivalent to empty here too.
func TestDropEmptyAssistantTurns_DropsWhitespaceOnlyMessage(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "   \n"},
		{Role: RoleUser, Content: "again"},
	}
	out := DropEmptyAssistantTurns(msgs)
	if len(out) != 2 {
		t.Fatalf("expected whitespace-only assistant dropped, got %d: %v", len(out), out)
	}
}

// A tool-calling assistant turn with no content is legitimate: dropping it
// would orphan the tool result that references its tool_call_id.
func TestDropEmptyAssistantTurns_KeepsToolCallAssistantWithoutContent(t *testing.T) {
	var call ToolCall
	call.ID = "c1"
	call.Type = "function"
	call.Function.Name = "read_file"
	call.Function.Arguments = "{}"

	msgs := []Message{
		{Role: RoleUser, Content: "read it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data"},
	}
	out := DropEmptyAssistantTurns(msgs)
	if len(out) != 3 {
		t.Fatalf("tool-calling turn must survive, got %d: %v", len(out), out)
	}
}

// A message list with nothing to drop must be returned unchanged (same
// slice, not a needless copy) - callers rely on this to avoid mutating
// history on every turn adoption when nothing is actually wrong.
func TestDropEmptyAssistantTurns_NoOpReturnsSameSlice(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
	}
	out := DropEmptyAssistantTurns(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("expected no messages dropped, got %d want %d", len(out), len(msgs))
	}
	if &out[0] != &msgs[0] {
		t.Fatal("expected the identical backing slice on a no-op")
	}
}
