package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// strictAPI models what an OpenAI-compatible endpoint actually enforces, rather
// than accepting whatever we send. Anything it rejects here is a request a real
// provider would reject with HTTP 400, so these tests fail loudly instead of
// only surfacing as a runtime outage.
//
// Enforced:
//   - an assistant message must carry content or tool_calls
//   - every tool message must reference a tool_call_id announced by a preceding
//     assistant message
//   - no host-only bookkeeping fields may appear on any message
func strictAPI(t *testing.T, reply func(turn int) map[string]any) *httptest.Server {
	t.Helper()
	turn := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "decode", http.StatusBadRequest)
			return
		}
		announced := map[string]bool{}
		for i, m := range body.Messages {
			for _, host := range []string{"created_at"} {
				if _, bad := m[host]; bad {
					t.Errorf("message %d leaked host-only field %q: %v", i, host, m)
					http.Error(w, "host field", http.StatusBadRequest)
					return
				}
			}
			role, _ := m["role"].(string)
			if role == provider.RoleAssistant {
				content, _ := m["content"].(string)
				calls, hasCalls := m["tool_calls"].([]any)
				if strings.TrimSpace(content) == "" && !hasCalls {
					t.Errorf("message %d: invalid assistant message: content or tool calls must be provided: %v", i, m)
					http.Error(w, "invalid assistant message", http.StatusBadRequest)
					return
				}
				for _, c := range calls {
					if cm, ok := c.(map[string]any); ok {
						if id, ok := cm["id"].(string); ok {
							announced[id] = true
						}
					}
				}
			}
			if role == provider.RoleTool {
				id, _ := m["tool_call_id"].(string)
				if !announced[id] {
					t.Errorf("message %d: orphaned tool result for %q: %v", i, id, m)
					http.Error(w, "orphaned tool result", http.StatusBadRequest)
					return
				}
			}
		}
		turn++
		_ = json.NewEncoder(w).Encode(reply(turn))
	}))
}

func textReply(content string) map[string]any {
	return map[string]any{"choices": []map[string]any{
		{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"},
	}}
}

func strictSession(t *testing.T, srv *httptest.Server) *Session {
	t.Helper()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	comp := provider.NewOpenAICompat("strict", srv.URL, "k", "", "")
	res := &config.Resolved{Model: "m"}
	sess := NewSession(res, comp)
	sess.SessionDir = t.TempDir()
	return sess
}

// A model that answers with empty content must not make every later turn fail.
// Before the fix the empty reply was recorded as a contentless assistant
// message, replayed on every subsequent request, and the API rejected the whole
// request with HTTP 400 — permanently, because history is also persisted.
func TestStrictAPI_EmptyReplyDoesNotPoisonSession(t *testing.T) {
	srv := strictAPI(t, func(turn int) map[string]any {
		if turn == 1 {
			return textReply("") // model returns nothing
		}
		return textReply("recovered")
	})
	defer srv.Close()

	sess := strictSession(t, srv)
	ctx := context.Background()

	if _, err := sess.SendUser(ctx, "first", io.Discard); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	got, err := sess.SendUser(ctx, "second", io.Discard)
	if err != nil {
		t.Fatalf("second turn must succeed after an empty reply: %v", err)
	}
	if got != "recovered" {
		t.Fatalf("reply=%q", got)
	}
}

// The same history reaches the API after a save/load round trip, so a session
// file written by an older build (or any other producer) must not be able to
// poison a reloaded session either.
func TestStrictAPI_PoisonedSessionFileStillUsable(t *testing.T) {
	srv := strictAPI(t, func(int) map[string]any { return textReply("ok") })
	defer srv.Close()

	sess := strictSession(t, srv)
	sess.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant}, // poisoned turn from an older build
	}
	if err := sess.Save("poisoned"); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded := strictSession(t, srv)
	reloaded.SessionDir = sess.SessionDir
	if err := reloaded.Load("poisoned"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := reloaded.SendUser(context.Background(), "continue", io.Discard); err != nil {
		t.Fatalf("reloaded poisoned session must still be usable: %v", err)
	}
}
