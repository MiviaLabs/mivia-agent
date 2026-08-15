package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSessionUserKeyGatedOnFlagAndSessionID locks in the wire shape for a
// client with SendSessionUserKey: "user" is emitted only when both the flag
// is set and the request carries a SessionID; either one absent leaves the
// body byte-identical to a request with neither.
func TestSessionUserKeyGatedOnFlagAndSessionID(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	cases := []struct {
		name      string
		enabled   bool
		sessionID string
		wantUser  bool
	}{
		{"flag on, session set", true, "sess-1", true},
		{"flag on, no session", true, "", false},
		{"flag off, session set", false, "sess-1", false},
		{"flag off, no session", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewOpenAICompatWithOptions(CompatOptions{
				Name: "test", BaseURL: "https://example.test", APIKey: "k",
				SendSessionUserKey: tc.enabled,
			})
			raw, err := c.marshalBody(Request{Model: "m", Messages: msgs, SessionID: tc.sessionID})
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decoding marshaled body: %v\nbody: %s", err, raw)
			}
			_, has := body["user"]
			if has != tc.wantUser {
				t.Fatalf("user field present=%v, want %v (body=%s)", has, tc.wantUser, raw)
			}
		})
	}
}

// TestSessionUserKeyStablePerSessionUniqueAcrossSessions pins the hashing
// contract: the same SessionID always derives the same "user" value across
// separate requests, and different SessionIDs derive different values.
func TestSessionUserKeyStablePerSessionUniqueAcrossSessions(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		SendSessionUserKey: true,
	})
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	userFor := func(sessionID string) string {
		raw, err := c.marshalBody(Request{Model: "m", Messages: msgs, SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		user, _ := body["user"].(string)
		if user == "" {
			t.Fatalf("expected non-empty user field, body=%s", raw)
		}
		return user
	}
	sameA := userFor("session-a")
	sameAAgain := userFor("session-a")
	if sameA != sameAAgain {
		t.Fatalf("user key not stable for the same session: %q vs %q", sameA, sameAAgain)
	}
	other := userFor("session-b")
	if sameA == other {
		t.Fatalf("user key must differ across sessions, got %q for both", sameA)
	}
	if sameA == "session-a" || other == "session-b" {
		t.Fatalf("user key must be a hash, not the raw session id: %q, %q", sameA, other)
	}
}

// TestSessionUserKeyOnlyOpenRouterFactory confirms only NewOpenRouter opts
// into sending the field - the other three built-in factories must leave the
// flag off so their request bodies stay unaffected by SessionID.
func TestSessionUserKeyOnlyOpenRouterFactory(t *testing.T) {
	comp, err := NewOpenRouter(Options{APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewOpenRouter must return *OpenAICompat, got %T", comp)
	}
	if !client.sendSessionUserKey {
		t.Fatalf("NewOpenRouter must set sendSessionUserKey, got %v", client.sendSessionUserKey)
	}

	for name, factory := range map[string]func(Options) (Completer, error){
		"deepseek": NewDeepSeek,
		"zai":      NewZAI,
		"ollama":   NewOllama,
	} {
		t.Run(name, func(t *testing.T) {
			comp, err := factory(Options{APIKey: "fake"})
			if err != nil {
				t.Fatal(err)
			}
			client, ok := comp.(*OpenAICompat)
			if !ok {
				t.Fatalf("factory must return *OpenAICompat, got %T", comp)
			}
			if client.sendSessionUserKey {
				t.Fatalf("%s must not set sendSessionUserKey", name)
			}
		})
	}
}

// TestNewRequestRejectsUserExtra pins "user" as reserved unconditionally -
// even for a client that never sets SendSessionUserKey - so an operator
// extra_body can never collide with the session-stickiness key.
func TestNewRequestRejectsUserExtra(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		ExtraBody: map[string]any{"user": "operator-supplied"},
	})
	_, err := c.newRequest(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error = %v, want a reserved-key failure", err)
	}
}

// TestSessionUserKeySurvivesRetryWithoutStreaming pins the SessionID
// propagation through retryWithoutStreaming's hand-built Request literal -
// a prior gap that silently dropped the field on that fallback leg. It calls
// the unexported method directly (in-package test) against a real server so
// the assertion is on the actual wire body, not a hand-copied literal.
func TestSessionUserKeySurvivesRetryWithoutStreaming(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: srv.URL, APIKey: "k",
		SendSessionUserKey: true,
	})
	req := Request{
		Model:     "m",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		Stream:    true,
		SessionID: "sess-retry",
	}
	if _, err := c.retryWithoutStreaming(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if _, has := captured["user"]; !has {
		t.Fatalf("expected user field on the retryWithoutStreaming wire body, got %#v", captured)
	}
}
