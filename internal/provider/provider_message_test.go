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
	off := toAPIMessages(msgs, false)
	if len(off) != 2 {
		t.Fatalf("len=%d", len(off))
	}
	if off[1].ReasoningContent != "" {
		t.Fatalf("capability off must not emit reasoning, got %q", off[1].ReasoningContent)
	}
	on := toAPIMessages(msgs, true)
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
	out := toAPIMessages(msgs, true)
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
	out := toAPIMessages(msgs, true)
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
	call := toolCall("c1", "read_file", `{"path":"a"}`)
	goodCall := toolCall("c2", "read_file", `{"path":"b"}`)
	msgs := []Message{
		{Role: RoleUser, Content: "read a then b"},
		// Legacy /effort-off: tool turn without reasoning → drop with results.
		{Role: RoleAssistant, ToolCalls: []ToolCall{call}},
		{Role: RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data-a"},
		// Healthy adopting turn with reasoning → keep.
		{Role: RoleAssistant, ToolCalls: []ToolCall{goodCall}, ReasoningContent: "now read b"},
		{Role: RoleTool, ToolCallID: "c2", Name: "read_file", Content: "data-b"},
	}
	out := toAPIMessages(msgs, true)
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
			t.Fatalf("adopting provider must not emit reasoning-less tool-call turn: %+v", am)
		}
	}

	// Capability off: legacy exchange stays (non-adopters do not 400 on this).
	off := toAPIMessages(msgs, false)
	if len(off) != 5 {
		t.Fatalf("non-adopting path must keep full history, got %d", len(off))
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
	// zai: preserved + level != off → clear_thinking:false
	got := thinkingObject(reasoning.High, true)
	if got["type"] != "enabled" {
		t.Fatalf("enabled type: %#v", got)
	}
	if got["clear_thinking"] != false {
		t.Fatalf("preserved must set clear_thinking false: %#v", got)
	}
	// off → disabled, no clear_thinking
	off := thinkingObject(reasoning.Off, true)
	if off["type"] != "disabled" {
		t.Fatalf("off: %#v", off)
	}
	if _, ok := off["clear_thinking"]; ok {
		t.Fatalf("disabled must not carry clear_thinking: %#v", off)
	}
	// non-preserved (deepseek) → no clear_thinking
	plain := thinkingObject(reasoning.High, false)
	if plain["type"] != "enabled" {
		t.Fatalf("plain: %#v", plain)
	}
	if _, ok := plain["clear_thinking"]; ok {
		t.Fatalf("non-preserved must not carry clear_thinking: %#v", plain)
	}
	// Full body fields path with preserved.
	fields := reasoningBodyFields(reasoning.DialectThinking, reasoning.High, true)
	thinking, ok := fields["thinking"].(map[string]any)
	if !ok || thinking["clear_thinking"] != false {
		t.Fatalf("preserved thinking body: %#v", fields)
	}
}

func TestZaiFactoryDeclaresReplayAndPreserved(t *testing.T) {
	comp, err := NewZAI(Options{APIKey: "k", BaseURL: "https://example.invalid/v1"})
	if err != nil {
		t.Fatal(err)
	}
	c := comp.(*OpenAICompat)
	if !c.replayReasoning {
		t.Fatal("NewZAI must set RequiresReasoningReplay")
	}
	if !c.preservedThinking {
		t.Fatal("NewZAI must set PreservedThinking")
	}
	// Real request path: thinking object carries clear_thinking:false when on.
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	body := captureBody(t, CompatOptions{
		Name: "zai", Reasoning: reasoning.DialectThinking, PreservedThinking: true,
	}, req)
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" || thinking["clear_thinking"] != false {
		t.Fatalf("zai preserved thinking object: %#v", body["thinking"])
	}
	// Off: disabled without clear_thinking.
	req.ReasoningLevel = reasoning.Off
	offBody := captureBody(t, CompatOptions{
		Name: "zai", Reasoning: reasoning.DialectThinking, PreservedThinking: true,
	}, req)
	thinking, ok = offBody["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("off thinking: %#v", offBody["thinking"])
	}
	if _, present := thinking["clear_thinking"]; present {
		t.Fatalf("disabled must not carry clear_thinking: %#v", thinking)
	}
}

func TestDeepSeekFactoryDeclaresReplayAndDialect(t *testing.T) {
	comp, err := NewDeepSeek(Options{APIKey: "k", BaseURL: "https://example.invalid/v1"})
	if err != nil {
		t.Fatal(err)
	}
	c := comp.(*OpenAICompat)
	if !c.replayReasoning {
		t.Fatal("NewDeepSeek must set RequiresReasoningReplay")
	}
	if c.preservedThinking {
		t.Fatal("NewDeepSeek must NOT set PreservedThinking (no clear_thinking)")
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

func TestCompatOptionsWiresReplayAndPreservedFlags(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name:                    "t",
		BaseURL:                 "https://example.invalid",
		APIKey:                  "k",
		RequiresReasoningReplay: true,
		PreservedThinking:       true,
	})
	if !c.replayReasoning || !c.preservedThinking {
		t.Fatalf("NewOpenAICompatWithOptions lost flags: replay=%v preserved=%v", c.replayReasoning, c.preservedThinking)
	}
	c2 := NewOpenAICompatWithOptionsAndRetry(CompatOptions{
		Name:                    "t",
		BaseURL:                 "https://example.invalid",
		APIKey:                  "k",
		RequiresReasoningReplay: true,
		PreservedThinking:       false,
	}, nil)
	if !c2.replayReasoning || c2.preservedThinking {
		t.Fatalf("AndRetry lost flags: replay=%v preserved=%v", c2.replayReasoning, c2.preservedThinking)
	}
	// Default stays off.
	c3 := NewOpenAICompatWithOptions(CompatOptions{Name: "t", BaseURL: "https://example.invalid", APIKey: "k"})
	if c3.replayReasoning || c3.preservedThinking {
		t.Fatalf("defaults must be off: replay=%v preserved=%v", c3.replayReasoning, c3.preservedThinking)
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
