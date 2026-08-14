package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func toolCall(id, name, args string) ToolCall {
	var c ToolCall
	c.ID = id
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = args
	return c
}

func TestMessageReasoningContentRoundTrips(t *testing.T) {
	m := Message{
		Role:             RoleAssistant,
		Content:          "answer",
		ReasoningContent: "chain of thought here",
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reasoning_content"`) {
		t.Fatalf("expected reasoning_content key in JSON, got %s", raw)
	}
	var decoded Message
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ReasoningContent != m.ReasoningContent {
		t.Fatalf("round-trip lost ReasoningContent: got %q", decoded.ReasoningContent)
	}

	// Empty must be omitted (omitempty).
	empty := Message{Role: RoleAssistant, Content: "ok"}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "reasoning_content") {
		t.Fatalf("empty ReasoningContent must be omitted, got %s", raw)
	}
	// Legacy JSON without the field decodes to empty.
	var legacy Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"hi"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ReasoningContent != "" {
		t.Fatalf("legacy decode should leave ReasoningContent empty, got %q", legacy.ReasoningContent)
	}
}

func TestToAPIMessagesReplayGatedByCapability(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello", ReasoningContent: "thinking about hi"},
	}
	off := toAPIMessages(msgs, false, false)
	if len(off) != 2 {
		t.Fatalf("len=%d", len(off))
	}
	if off[1].ReasoningContent != "" {
		t.Fatalf("capability off must not emit reasoning, got %q", off[1].ReasoningContent)
	}
	on := toAPIMessages(msgs, true, false)
	if on[1].ReasoningContent != "thinking about hi" {
		t.Fatalf("capability on must emit assistant reasoning, got %q", on[1].ReasoningContent)
	}
}

func TestToAPIMessagesEmitsReasoningContentOnlyForAssistant(t *testing.T) {
	// Host history may carry stray reasoning on non-assistant roles (hand-edited
	// JSONL); the wire must never emit them even when replay is on.
	call := toolCall("c1", "f", `{}`)
	msgs := []Message{
		{Role: RoleSystem, Content: "sys", ReasoningContent: "sys-think"},
		{Role: RoleUser, Content: "hi", ReasoningContent: "user-think"},
		{Role: RoleAssistant, Content: "ok", ReasoningContent: "asst-think"},
		{Role: RoleAssistant, Content: "empty-reason", ReasoningContent: ""},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}, ReasoningContent: "tool-turn"},
		{Role: RoleTool, ToolCallID: "c1", Name: "f", Content: "r", ReasoningContent: "tool-think"},
	}
	out := toAPIMessages(msgs, true, false)
	for _, am := range out {
		switch am.Role {
		case RoleAssistant:
			if am.Content != nil && *am.Content == "ok" && am.ReasoningContent != "asst-think" {
				t.Fatalf("assistant with reasoning lost it: %+v", am)
			}
			if am.Content != nil && *am.Content == "empty-reason" && am.ReasoningContent != "" {
				t.Fatalf("empty assistant reasoning must be absent: %+v", am)
			}
			if len(am.ToolCalls) > 0 && am.ReasoningContent != "tool-turn" {
				t.Fatalf("tool-call assistant must keep reasoning: %+v", am)
			}
		default:
			if am.ReasoningContent != "" {
				t.Fatalf("non-assistant role %q must not emit reasoning: %+v", am.Role, am)
			}
		}
	}
}

func TestToAPIMessagesReasoningContentPreservedThroughToolTurn(t *testing.T) {
	call := toolCall("c1", "read_file", `{"path":"a"}`)
	msgs := []Message{
		{Role: RoleUser, Content: "read a"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}, ReasoningContent: "I should read the file"},
		{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data"},
	}
	out := toAPIMessages(msgs, true, false)
	if len(out) != 3 {
		t.Fatalf("len=%d want 3", len(out))
	}
	if out[1].ReasoningContent != "I should read the file" {
		t.Fatalf("tool-call turn reasoning missing: %+v", out[1])
	}
	if len(out[1].ToolCalls) != 1 {
		t.Fatalf("tool_calls lost: %+v", out[1])
	}
}

func TestReasoningContentByteStabilityWhenReplayDisabled(t *testing.T) {
	// Capability off → marshalled request byte-identical whether or not history
	// carries ReasoningContent.
	base := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi there"},
	}
	withReasoning := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi there", ReasoningContent: "secret thoughts that must not leak"},
	}
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "https://example.invalid/v1", APIKey: "k"})
	if c.replayReasoning {
		t.Fatal("default client must not require reasoning replay")
	}
	body := func(msgs []Message) []byte {
		t.Helper()
		raw, err := c.marshalBody(Request{Model: "m", Messages: msgs})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	a, b := body(base), body(withReasoning)
	if string(a) != string(b) {
		t.Fatalf("capability-off bodies must be byte-identical:\n%s\n---\n%s", a, b)
	}
	if strings.Contains(string(a), "reasoning_content") {
		t.Fatalf("capability-off body must not contain reasoning_content: %s", a)
	}
}

func TestAdoptingProviderDropsReasoningLessToolCallExchange(t *testing.T) {
	// D2: RejectReasoningLessToolTurns on (DeepSeek) drops empty-reasoning tool exchanges.
	call := toolCall("c1", "read_file", `{"path":"a"}`)
	goodCall := toolCall("c2", "read_file", `{"path":"b"}`)
	msgs := []Message{
		{Role: RoleUser, Content: "read a then b"},
		// Legacy /effort-off: tool turn without reasoning → drop with results.
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data-a"},
		// Healthy turn with reasoning → keep.
		{Role: RoleAssistant, ToolCalls: []ToolCall{goodCall}, ReasoningContent: "now read b"},
		{Role: RoleTool, ToolCallID: "c2", Name: "read_file", Content: "data-b"},
	}
	out := toAPIMessages(msgs, true, true)
	if len(out) != 3 {
		t.Fatalf("expected user + healthy exchange only, got %d: %+v", len(out), out)
	}
	if out[0].Role != RoleUser {
		t.Fatalf("first=%+v", out[0])
	}
	if out[1].ReasoningContent != "now read b" || len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != "c2" {
		t.Fatalf("healthy turn lost: %+v", out[1])
	}
	if out[2].ToolCallID != "c2" {
		t.Fatalf("healthy tool result lost: %+v", out[2])
	}
	for _, am := range out {
		if am.ReasoningContent == "" && len(am.ToolCalls) > 0 {
			t.Fatalf("reject-on must not emit reasoning-less tool-call turn: %+v", am)
		}
	}

	// Reject off: legacy exchange stays even with replay on.
	off := toAPIMessages(msgs, true, false)
	if len(off) != 5 {
		t.Fatalf("reject-off path must keep full history, got %d", len(off))
	}
}

func TestAdoptingProviderKeepsCurrentReasoningLessToolExchange(t *testing.T) {
	// The current loop's tool result must remain visible. Dropping this
	// terminal exchange makes the model receive no result and repeat the call.
	call := toolCall("current", "lookup", `{}`)
	msgs := []Message{
		{Role: RoleUser, Content: "lookup"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		{Role: RoleTool, ToolCallID: "current", Name: "lookup", Content: "found"},
	}
	out := toAPIMessages(msgs, true, true)
	if len(out) != 3 || len(out[1].ToolCalls) != 1 || out[2].ToolCallID != "current" {
		t.Fatalf("current tool exchange was dropped: %+v", out)
	}
}

func TestTerminalExchangeSurvivesTrailingInjectedMessages(t *testing.T) {
	// The host appends messages after the current exchange's tool results
	// before the request ships: the user-role context summary and the
	// conclude nudge. The terminal-exchange guard must ignore those trailing
	// messages, or the reject gate drops the CURRENT exchange with its tool
	// result and the model never sees the result it just produced.
	call := toolCall("current", "lookup", `{}`)
	summary := Message{Role: RoleUser, Content: "[host-injected context summary]", Name: "context-summary"}
	nudge := Message{Role: RoleUser, Content: "conclude with what you have", Name: "conclude-nudge"}
	msgs := []Message{
		{Role: RoleUser, Content: "lookup"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}}, // no reasoning
		{Role: RoleTool, ToolCallID: "current", Name: "lookup", Content: "found"},
		summary,
	}
	out := toAPIMessages(msgs, true, true)
	if len(out) != 4 || len(out[1].ToolCalls) != 1 || out[2].ToolCallID != "current" {
		t.Fatalf("summary tail: current tool exchange was dropped: %+v", out)
	}
	// Both trailing injections at once (summary then nudge) must also keep it.
	msgs = append(msgs, nudge)
	out = toAPIMessages(msgs, true, true)
	if len(out) != 5 || len(out[1].ToolCalls) != 1 || out[2].ToolCallID != "current" {
		t.Fatalf("both tails: current tool exchange was dropped: %+v", out)
	}
}

func TestTrailingToolCallTurnStillMarksOlderExchangeNonTerminal(t *testing.T) {
	// The trailing-message trim must never trim exchange-shaped messages: an
	// older completed exchange followed by a NEW assistant tool-call turn is
	// still non-terminal and is dropped by the reject gate.
	c1 := toolCall("c1", "lookup", `{}`)
	c2 := toolCall("c2", "lookup", `{}`)
	msgs := []Message{
		{Role: RoleUser, Content: "lookup twice"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{c1}},
		{Role: RoleTool, ToolCallID: "c1", Name: "lookup", Content: "a"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{c2}},
		{Role: RoleTool, ToolCallID: "c2", Name: "lookup", Content: "b"},
		{Role: RoleUser, Content: "[host-injected context summary]", Name: "context-summary"},
	}
	out := toAPIMessages(msgs, false, true)
	for _, am := range out {
		if am.ToolCallID == "c1" {
			t.Fatalf("older tool result survived: %+v", am)
		}
		for _, tc := range am.ToolCalls {
			if tc.ID == "c1" {
				t.Fatalf("older tool call survived: %+v", am)
			}
		}
	}
	if len(out) != 4 || out[1].ToolCalls[0].ID != "c2" || out[2].ToolCallID != "c2" {
		t.Fatalf("terminal exchange or trailing summary lost: %+v", out)
	}
}

func TestRejectModeDropsOlderReasoningLessExchangeKeepsTerminal(t *testing.T) {
	// D2 pins the documented tradeoff: with RejectReasoningLessToolTurns on, an
	// older reasoning-less exchange is dropped WITH its tool results; only the
	// terminal exchange (the current loop's pending call plus its result)
	// survives so the model still sees the last tool outcome.
	c1 := toolCall("c1", "read_file", `{"path":"a"}`)
	c2 := toolCall("c2", "read_file", `{"path":"b"}`)
	msgs := []Message{
		{Role: RoleUser, Content: "read a then b"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{c1}}, // older, empty reasoning
		{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data-a"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{c2}}, // terminal, empty reasoning
		{Role: RoleTool, ToolCallID: "c2", Name: "read_file", Content: "data-b"},
	}
	out := toAPIMessages(msgs, false, true)
	if len(out) != 3 {
		t.Fatalf("expected user + terminal exchange only, got %d: %+v", len(out), out)
	}
	if out[0].Role != RoleUser {
		t.Fatalf("first message must be user, got %+v", out[0])
	}
	if len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != "c2" {
		t.Fatalf("terminal assistant turn lost: %+v", out[1])
	}
	if out[2].ToolCallID != "c2" {
		t.Fatalf("terminal tool result lost: %+v", out[2])
	}
	// The older exchange must be gone: no c1 anywhere in the wire output.
	for _, am := range out {
		if am.ToolCallID == "c1" {
			t.Fatalf("older tool result survived: %+v", am)
		}
		for _, tc := range am.ToolCalls {
			if tc.ID == "c1" {
				t.Fatalf("older tool call survived: %+v", am)
			}
		}
	}
}

func TestRedactedPlaceholderReasoningSurvivesReplayAndGate(t *testing.T) {
	// Wave-C redaction persists non-empty "[redacted]" reasoning; that
	// placeholder must keep DeepSeek resume working: replayed verbatim on the
	// wire and never treated as reasoning-less by the D2 gate (TrimSpace is
	// non-empty), so the tool exchange is not dropped.
	call := toolCall("c1", "read_file", `{"path":"a"}`)
	msgs := []Message{
		{Role: RoleUser, Content: "read a"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}, ReasoningContent: "[redacted]"},
		{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data"},
	}
	out := toAPIMessages(msgs, true, true)
	if len(out) != 3 {
		t.Fatalf("redacted reasoning must survive the D2 gate, got %d: %+v", len(out), out)
	}
	if out[1].ReasoningContent != "[redacted]" {
		t.Fatalf("wire reasoning_content lost: %+v", out[1])
	}
	if len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != "c1" {
		t.Fatalf("tool-call turn dropped by gate: %+v", out[1])
	}
	if out[2].ToolCallID != "c1" {
		t.Fatalf("tool result lost: %+v", out[2])
	}
}

func TestZaiDoesNotDropReasoningLessToolTurns(t *testing.T) {
	// z.ai: replay on, reject bit off → reasoning-less tool-call turn is SENT.
	// glm-5-turbo ships reasoning=off; multi-step tools must keep those turns.
	call := toolCall("c1", "lookup", `{}`)
	msgs := []Message{
		{Role: RoleUser, Content: "go"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		{Role: RoleTool, ToolCallID: "c1", Name: "lookup", Content: "result"},
	}
	// Direct convert path.
	out := toAPIMessages(msgs, true, false)
	if len(out) != 3 {
		t.Fatalf("z.ai must keep reasoning-less tool exchange, got %d: %+v", len(out), out)
	}
	if len(out[1].ToolCalls) != 1 {
		t.Fatalf("tool-call turn dropped: %+v", out[1])
	}
	// Real factory marshal path.
	c, err := NewZAI(Options{APIKey: "k", BaseURL: "https://example.invalid/v1"})
	if err != nil {
		t.Fatal(err)
	}
	compat := c.(*OpenAICompat)
	if !compat.replayReasoning {
		t.Fatal("NewZAI must set RequiresReasoningReplay")
	}
	if compat.rejectReasoningLessToolTurns {
		t.Fatal("NewZAI must NOT set RejectReasoningLessToolTurns")
	}
	raw, err := compat.marshalBody(Request{
		Model: "glm-5-turbo", Messages: msgs,
		Tools: []ToolSpec{{"type": "function", "function": map[string]any{"name": "lookup"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tool_calls"`) {
		t.Fatalf("z.ai wire body dropped tool_calls: %s", raw)
	}
	if !strings.Contains(string(raw), `"tool_call_id":"c1"`) {
		t.Fatalf("z.ai wire body dropped tool result: %s", raw)
	}
}

func TestValidateToolPairingRejectsNonAssistantReasoning(t *testing.T) {
	healthy := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "ok", ReasoningContent: "fine on assistant"},
	}
	if err := ValidateToolPairing(healthy); err != nil {
		t.Fatalf("healthy assistant reasoning must pass: %v", err)
	}
	cases := []struct {
		name string
		msgs []Message
	}{
		{"user", []Message{{Role: RoleUser, Content: "hi", ReasoningContent: "nope"}}},
		{"system", []Message{
			{Role: RoleSystem, Content: "sys", ReasoningContent: "nope"},
			{Role: RoleUser, Content: "hi"},
		}},
		{"tool", []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{toolCall("c1", "f", `{}`)}},
			{Role: RoleTool, ToolCallID: "c1", Name: "f", Content: "r", ReasoningContent: "nope"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateToolPairing(tc.msgs); err == nil {
				t.Fatal("expected rejection of non-assistant reasoning_content")
			}
		})
	}
}

func TestThinkingObjectPreservedClearThinking(t *testing.T) {
	// thinking_preserved dialect + level != off → clear_thinking:false
	fields := reasoningBodyFields(reasoning.DialectThinkingPreserved, reasoning.High)
	thinking, ok := fields["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" || thinking["clear_thinking"] != false {
		t.Fatalf("thinking_preserved enabled: %#v", fields)
	}
	if fields["reasoning_effort"] != "high" {
		t.Fatalf("thinking_preserved must grade via effort: %#v", fields)
	}
	// off → disabled, no clear_thinking
	off := reasoningBodyFields(reasoning.DialectThinkingPreserved, reasoning.Off)
	thinking, ok = off["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("off: %#v", off)
	}
	if _, present := thinking["clear_thinking"]; present {
		t.Fatalf("disabled must not carry clear_thinking: %#v", thinking)
	}
	// thinking_effort / thinking (deepseek, default zai) → no clear_thinking
	for _, d := range []reasoning.Dialect{reasoning.DialectThinking, reasoning.DialectThinkingEffort} {
		body := reasoningBodyFields(d, reasoning.High)
		th, ok := body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing thinking: %#v", d, body)
		}
		if _, present := th["clear_thinking"]; present {
			t.Fatalf("%s must not carry clear_thinking: %#v", d, th)
		}
	}
}

func TestThinkingPreservedDialectResolvedFromModelEntry(t *testing.T) {
	// A request naming thinking_preserved uses it; default zai dialect unchanged.
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	req.ReasoningDialect = reasoning.DialectThinkingPreserved
	body := captureBody(t, CompatOptions{
		Name: "zai", Reasoning: reasoning.DialectThinking, // factory default
	}, req)
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["clear_thinking"] != false {
		t.Fatalf("model entry dialect must win: %#v", body["thinking"])
	}
	// Without request dialect, factory default (thinking) has no clear_thinking.
	req.ReasoningDialect = ""
	plain := captureBody(t, CompatOptions{
		Name: "zai", Reasoning: reasoning.DialectThinking,
	}, req)
	thinking, ok = plain["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("default thinking: %#v", plain["thinking"])
	}
	if _, present := thinking["clear_thinking"]; present {
		t.Fatalf("default dialect must not force clear_thinking: %#v", thinking)
	}
}

func TestZaiFactoryDeclaresReplayOnly(t *testing.T) {
	comp, err := NewZAI(Options{APIKey: "k", BaseURL: "https://example.invalid/v1"})
	if err != nil {
		t.Fatal(err)
	}
	c := comp.(*OpenAICompat)
	if !c.replayReasoning {
		t.Fatal("NewZAI must set RequiresReasoningReplay")
	}
	if c.rejectReasoningLessToolTurns {
		t.Fatal("NewZAI must NOT set RejectReasoningLessToolTurns")
	}
}

func TestDeepSeekFactoryDeclaresReplayAndReject(t *testing.T) {
	comp, err := NewDeepSeek(Options{APIKey: "k", BaseURL: "https://example.invalid/v1"})
	if err != nil {
		t.Fatal(err)
	}
	c := comp.(*OpenAICompat)
	if !c.replayReasoning {
		t.Fatal("NewDeepSeek must set RequiresReasoningReplay")
	}
	if !c.rejectReasoningLessToolTurns {
		t.Fatal("NewDeepSeek must set RejectReasoningLessToolTurns")
	}
	if c.reasoning != reasoning.DialectThinkingEffort {
		t.Fatalf("NewDeepSeek dialect = %q, want thinking_effort", c.reasoning)
	}
	// DeepSeek thinking object must never receive clear_thinking.
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	body := captureBody(t, CompatOptions{
		Name: "deepseek", Reasoning: reasoning.DialectThinkingEffort, RequiresReasoningReplay: true,
	}, req)
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("deepseek thinking: %#v", body["thinking"])
	}
	if _, present := thinking["clear_thinking"]; present {
		t.Fatalf("deepseek must not receive clear_thinking: %#v", thinking)
	}
}

func TestCompatOptionsWiresReplayAndRejectFlags(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name:                         "t",
		BaseURL:                      "https://example.invalid",
		APIKey:                       "k",
		RequiresReasoningReplay:      true,
		RejectReasoningLessToolTurns: true,
	})
	if !c.replayReasoning || !c.rejectReasoningLessToolTurns {
		t.Fatalf("NewOpenAICompatWithOptions lost flags: replay=%v reject=%v", c.replayReasoning, c.rejectReasoningLessToolTurns)
	}
	c2 := NewOpenAICompatWithOptionsAndRetry(CompatOptions{
		Name:                    "t",
		BaseURL:                 "https://example.invalid",
		APIKey:                  "k",
		RequiresReasoningReplay: true,
	}, nil)
	if !c2.replayReasoning || c2.rejectReasoningLessToolTurns {
		t.Fatalf("AndRetry lost flags: replay=%v reject=%v", c2.replayReasoning, c2.rejectReasoningLessToolTurns)
	}
	c3 := NewOpenAICompatWithOptions(CompatOptions{Name: "t", BaseURL: "https://example.invalid", APIKey: "k"})
	if c3.replayReasoning || c3.rejectReasoningLessToolTurns {
		t.Fatalf("defaults must be off: replay=%v reject=%v", c3.replayReasoning, c3.rejectReasoningLessToolTurns)
	}
}

func TestMarshalBodyEmitsReasoningOnlyWhenReplayEnabled(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "yo", ReasoningContent: "thoughts"},
	}
	off := NewOpenAICompatWithOptions(CompatOptions{Name: "t", BaseURL: "https://example.invalid", APIKey: "k"})
	on := NewOpenAICompatWithOptions(CompatOptions{
		Name: "t", BaseURL: "https://example.invalid", APIKey: "k", RequiresReasoningReplay: true,
	})
	offBody, err := off.marshalBody(Request{Model: "m", Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	onBody, err := on.marshalBody(Request{Model: "m", Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(offBody), "reasoning_content") {
		t.Fatalf("off client leaked reasoning_content: %s", offBody)
	}
	if !strings.Contains(string(onBody), `"reasoning_content":"thoughts"`) {
		t.Fatalf("on client missing reasoning_content: %s", onBody)
	}
}

// TestReplayPrefixStableAcrossToolTurns is a light mock of request N → N+1
// prefix stability for an adopting client (full multi-turn integration in
// reasoning_replay_integration_test.go).
func TestReplayPrefixStableAcrossToolTurns(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, append([]byte(nil), raw...))
		// Alternate: first call returns tool_calls+reasoning, second returns final.
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"plan step one","tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done","reasoning_content":"wrap up"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "deepseek-like", BaseURL: srv.URL, APIKey: "k", RequiresReasoningReplay: true,
	})
	// Turn 1: no prior reasoning.
	resp1, err := c.ChatTurn(context.Background(), Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleUser, Content: "do work"},
		},
		Tools: []ToolSpec{{"type": "function", "function": map[string]any{"name": "lookup"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp1.ReasoningContent != "plan step one" {
		t.Fatalf("resp1 reasoning=%q", resp1.ReasoningContent)
	}
	// Turn 2: host history includes the preserved reasoning (as the agent loop will).
	_, err = c.ChatTurn(context.Background(), Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleUser, Content: "do work"},
			{
				Role:             RoleAssistant,
				ToolCalls:        resp1.ToolCalls,
				ReasoningContent: resp1.ReasoningContent,
			},
			{Role: RoleTool, ToolCallID: "c1", Name: "lookup", Content: "result"},
		},
		Tools: []ToolSpec{{"type": "function", "function": map[string]any{"name": "lookup"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies=%d", len(bodies))
	}
	if strings.Contains(string(bodies[0]), "reasoning_content") {
		// First request has no prior assistant reasoning — must not invent any.
		var body0 map[string]any
		_ = json.Unmarshal(bodies[0], &body0)
		msgs, _ := body0["messages"].([]any)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if _, ok := mm["reasoning_content"]; ok {
				t.Fatalf("request 1 invented reasoning_content: %s", bodies[0])
			}
		}
	}
	if !strings.Contains(string(bodies[1]), `"reasoning_content":"plan step one"`) {
		t.Fatalf("request 2 must replay request 1 reasoning verbatim:\n%s", bodies[1])
	}
	// Byte-stable: the same reasoning string appears unchanged.
	if !reflect.DeepEqual(
		extractAssistantReasoning(t, bodies[1]),
		[]string{"plan step one"},
	) {
		t.Fatalf("prefix not stable: %v", extractAssistantReasoning(t, bodies[1]))
	}
}

func extractAssistantReasoning(t *testing.T, raw []byte) []string {
	t.Helper()
	var body struct {
		Messages []struct {
			Role             string `json:"role"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range body.Messages {
		if m.Role == RoleAssistant && m.ReasoningContent != "" {
			out = append(out, m.ReasoningContent)
		}
	}
	return out
}
