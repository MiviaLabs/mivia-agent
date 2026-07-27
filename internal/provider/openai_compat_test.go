package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestChatTurnIdempotencyKeyIsStablePerRequestAndUniqueAcrossRequests(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	c := NewOpenAICompat("test", srv.URL, "k", "", "")
	req := Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "same"}}}
	if _, err := c.ChatTurn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChatTurn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] == "" || keys[0] == keys[1] {
		t.Fatalf("idempotency keys=%v", keys)
	}
}

func TestChatNonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "hello"}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL+"/v1", "fake-key", "", "")
	out, err := c.Chat(context.Background(), Request{
		Model:    "deepseek-v4-flash",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("got %q", out)
	}
}

func TestChatTurnToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["tools"] == nil {
			t.Fatal("expected tools in request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"finish_reason": "tool_calls",
					"message": map[string]any{
						"role":    "assistant",
						"content": "",
						"tool_calls": []map[string]any{
							{
								"id":   "call_1",
								"type": "function",
								"function": map[string]string{
									"name":      "read_file",
									"arguments": `{"path":"a.txt"}`,
								},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "k", "", "")
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "read a"}},
		Tools: []ToolSpec{
			{"type": "function", "function": map[string]any{"name": "read_file"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("%+v", resp.ToolCalls)
	}
}

func TestChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = io.WriteString(w, s)
			if flusher != nil {
				flusher.Flush()
			}
		}
		write("data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		write("data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "fake-key", "", "")
	var buf strings.Builder
	out, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" || buf.String() != "hello" {
		t.Fatalf("out=%q buf=%q", out, buf.String())
	}
}

func TestAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()
	c := NewOpenAICompat("deepseek", srv.URL, "bad", "", "")
	_, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("err=%v", err)
	}
}
