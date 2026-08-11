package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// A provider that caches its prefix automatically (every built-in provider
// today) only pays off if the serialized request prefix is byte-stable
// across turns. Message.CreatedAt is host-only bookkeeping - toAPIMessages
// already strips it before the wire - so two requests built from messages
// that differ only in CreatedAt must serialize to identical bytes. This
// locks that invariant in as a regression test rather than production code.
func TestChatTurnRequestBodyIsByteStableAcrossCreatedAt(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		bodies = append(bodies, append([]byte(nil), buf[:n]...))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	tools := []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file"}}}

	messagesAt := func(when time.Time) []Message {
		return []Message{
			{Role: RoleSystem, Content: "you are a test assistant", CreatedAt: when},
			{Role: RoleUser, Content: "hello", CreatedAt: when},
			{Role: RoleAssistant, Content: "hi there", CreatedAt: when},
		}
	}

	req := func(when time.Time) Request {
		return Request{Model: "m", Messages: messagesAt(when), Tools: tools}
	}

	if _, err := c.ChatTurn(context.Background(), req(time.Unix(0, 0))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChatTurn(context.Background(), req(time.Now())); err != nil {
		t.Fatal(err)
	}

	if len(bodies) != 2 {
		t.Fatalf("captured %d request bodies, want 2", len(bodies))
	}
	if string(bodies[0]) != string(bodies[1]) {
		t.Fatalf("request bodies differ despite identical content and differing CreatedAt only:\n%s\n---\n%s", bodies[0], bodies[1])
	}
}

// TestChatTurnRequestBodyIsByteStableAcrossEqualPrefixIdentity extends B7 from
// CreatedAt-only to the FULL wire-affecting prefix identity (INV-68-1/INV-68-3):
// equal model, tools slice, temperature value, reasoning level/dialect, and
// message set serialize to byte-identical bodies even when non-wire fields
// (CreatedAt, timeout, replay flag) differ.
func TestChatTurnRequestBodyIsByteStableAcrossEqualPrefixIdentity(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "http://example.invalid", APIKey: "k", Reasoning: reasoning.DialectThinkingEffort})
	tools := []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file"}}}
	temp := 0.7

	req := func(when time.Time) Request {
		return Request{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "you are a test assistant", CreatedAt: when}, {Role: RoleUser, Content: "hello", CreatedAt: when}}, Tools: tools, Temperature: &temp, ReasoningLevel: reasoning.High, Timeout: 30 * time.Second}
	}

	body1, err := c.marshalBody(req(time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	body2, err := c.marshalBody(req(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if string(body1) != string(body2) {
		t.Fatalf("request bodies differ despite an equal wire-affecting identity:\n%s\n---\n%s", body1, body2)
	}
}

// TestChatTurnRequestBodyChangesOnlyOnReasoningEffortChange pins that the
// request body changes ONLY when the reasoning dial changes and stays
// byte-identical when only non-wire fields (CreatedAt, timeout, replay flag)
// differ (INV-68-3; gap B13's wire-side proof).
func TestChatTurnRequestBodyChangesOnlyOnReasoningEffortChange(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "http://example.invalid", APIKey: "k", Reasoning: reasoning.DialectThinkingEffort})
	base := Request{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "hi"}}, ReasoningLevel: reasoning.High}

	highBody, err := c.marshalBody(base)
	if err != nil {
		t.Fatal(err)
	}
	lowBase := base
	lowBase.ReasoningLevel = reasoning.Low
	lowBody, err := c.marshalBody(lowBase)
	if err != nil {
		t.Fatal(err)
	}
	if string(highBody) == string(lowBody) {
		t.Fatalf("bodies must differ when the reasoning level changes:\n%s", highBody)
	}

	variant := base
	variant.Timeout = 5 * time.Second
	variant.DisableProviderReplay = true
	variant.Messages[0].CreatedAt = time.Now()
	variantBody, err := c.marshalBody(variant)
	if err != nil {
		t.Fatal(err)
	}
	if string(highBody) != string(variantBody) {
		t.Fatalf("bodies differ when only non-wire fields change:\n%s\n---\n%s", highBody, variantBody)
	}
}

// TestChatTurnRequestBodyChangesOnlyOnToolAdmission pins that the request body
// changes when the Tools slice gains an entry and stays byte-identical when
// Tools is unchanged (INV-68-3; the wire-side proof for W3's tool_admission
// reset event).
func TestChatTurnRequestBodyChangesOnlyOnToolAdmission(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "http://example.invalid", APIKey: "k"})
	base := Request{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "hi"}}, Tools: []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file"}}}}

	baseBody, err := c.marshalBody(base)
	if err != nil {
		t.Fatal(err)
	}
	wider := base
	wider.Tools = append(wider.Tools, ToolSpec{"type": "function", "function": map[string]any{"name": "grep"}})
	widerBody, err := c.marshalBody(wider)
	if err != nil {
		t.Fatal(err)
	}
	if string(baseBody) == string(widerBody) {
		t.Fatalf("bodies must differ when the Tools slice gains an entry:\n%s", baseBody)
	}

	variant := base
	variant.Messages[0].CreatedAt = time.Now()
	variantBody, err := c.marshalBody(variant)
	if err != nil {
		t.Fatal(err)
	}
	if string(baseBody) != string(variantBody) {
		t.Fatalf("bodies differ when Tools is unchanged but CreatedAt differs:\n%s\n---\n%s", baseBody, variantBody)
	}
}
