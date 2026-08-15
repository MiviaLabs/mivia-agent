package chat

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// rejectReasoningLessCompleter is a scripted completer that also declares
// DeepSeek's RejectReasoningLessToolTurns policy, so finishAgentTurn's
// persist-time repair (provider.ReasoningPolicyFor) fires for it. It answers
// with a plain final reply - no tool calls of its own - so the turn's
// interesting content is entirely the pre-seeded legacy history.
type rejectReasoningLessCompleter struct {
	requests []provider.Request
	reply    string
}

func (c *rejectReasoningLessCompleter) Name() string { return "reject-reasoning-less" }
func (c *rejectReasoningLessCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *rejectReasoningLessCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *rejectReasoningLessCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.requests = append(c.requests, req)
	return &provider.Response{Content: c.reply, FinishReason: "stop"}, nil
}
func (c *rejectReasoningLessCompleter) ReasoningPolicy() provider.ReasoningPolicy {
	return provider.ReasoningPolicy{RequiresReplay: true, RejectReasoningLess: true}
}

// TestFinishAgentTurnRepairsLegacyReasoningLessExchangeOnce pins the fix: a
// reasoning-less tool-call exchange left over in history (e.g. from before
// this repair existed, or from a run with reasoning off) is dropped from
// PERSISTED session history exactly once, at turn-adoption time - not
// silently rewritten on every later request serialization. Before the fix,
// only toAPIMessages dropped it, per HTTP request, and the persisted
// s.Messages still carried the poisoned exchange forever.
func TestFinishAgentTurnRepairsLegacyReasoningLessExchangeOnce(t *testing.T) {
	comp := &rejectReasoningLessCompleter{reply: "done"}
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, comp)
	s.UseTools = true
	s.Tools = tools.NewRegistry()
	s.MaxSteps = 3

	legacy := legacyReasoningLessHistory()
	s.mu.Lock()
	s.Messages = legacy
	s.mu.Unlock()

	if _, err := s.SendUser(context.Background(), "next", io.Discard); err != nil {
		t.Fatal(err)
	}

	persisted := s.MessagesCopy()
	for _, m := range persisted {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 && m.ReasoningContent == "" {
			t.Fatalf("persisted history still carries the legacy reasoning-less tool-call turn: %+v", persisted)
		}
		if m.ToolCallID == "old" {
			t.Fatalf("persisted history still carries the orphaned tool result: %+v", persisted)
		}
	}

	if len(comp.requests) != 1 {
		t.Fatalf("expected exactly one provider request, got %d", len(comp.requests))
	}
	// Request 1 legitimately still carries "old": the loop's own request
	// serialization is untouched by this fix (that per-request drop lives in
	// the real client's toAPIMessages, which this fake bypasses by design).
	// What must change is what gets PERSISTED after the turn - asserted above
	// - and that the very next request built from that persisted history no
	// longer carries it, asserted below.

	// A second turn must not find anything left to repair: the persisted
	// history is already clean, so nothing further gets dropped and the
	// request body stays stable turn over turn.
	if _, err := s.SendUser(context.Background(), "again", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(comp.requests) != 2 {
		t.Fatalf("expected exactly two provider requests, got %d", len(comp.requests))
	}
	for _, m := range comp.requests[1].Messages {
		if m.ToolCallID == "old" {
			t.Fatalf("second request resurrected the dropped exchange: %+v", comp.requests[1].Messages)
		}
	}
}

// TestFinishAgentTurnKeepsHistoryForNonRejectingCompleter pins the negative
// case: a completer that does not implement provider.ReasoningPolicyAware (or
// declares RejectReasoningLess=false, e.g. z.ai-shaped) must never have its
// persisted history rewritten by this repair - the same reasoning-less
// tool-call turn survives untouched.
func TestFinishAgentTurnKeepsHistoryForNonRejectingCompleter(t *testing.T) {
	comp := &sessionToolCompleter{}
	// Force the scripted completer straight to its final-answer branch by
	// pre-incrementing calls, so this turn produces no tool calls of its own.
	comp.calls = 1
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, comp)
	s.UseTools = true
	s.Tools = tools.NewRegistry()
	s.MaxSteps = 3

	s.mu.Lock()
	s.Messages = legacyReasoningLessHistory()
	s.mu.Unlock()

	if _, err := s.SendUser(context.Background(), "next", io.Discard); err != nil {
		t.Fatal(err)
	}

	var sawExchange bool
	for _, m := range s.MessagesCopy() {
		if m.ToolCallID == "old" {
			sawExchange = true
		}
	}
	if !sawExchange {
		t.Fatal("non-rejecting completer must keep the reasoning-less tool-call exchange in persisted history")
	}
}

// legacyReasoningLessHistory is a fresh copy of a pre-existing, non-terminal
// assistant tool-call turn with no ReasoningContent, paired with its tool
// result - the shape a session accumulates before this repair existed, or
// under a run with reasoning off.
func legacyReasoningLessHistory() []provider.Message {
	call := provider.ToolCall{ID: "old", Type: "function"}
	call.Function.Name = "lookup"
	call.Function.Arguments = "{}"
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "earlier"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: "old", Name: "lookup", Content: "stale-result"},
	}
}
