package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
