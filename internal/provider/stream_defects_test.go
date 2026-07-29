package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sseServer(t *testing.T, chunks []string, sendDone bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		if sendDone {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
}

func streamingClient(t *testing.T, srv *httptest.Server) *OpenAICompat {
	t.Helper()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	return NewOpenAICompat("test", srv.URL, "k", "", "")
}

// A stream that ends without [DONE] and without a finish_reason is a truncated
// response (proxy cut, HTTP/2 END_STREAM, connection close). bufio.Scanner
// reports nil at EOF, so it is otherwise indistinguishable from a complete one
// — and the caller then executes a tool whose argument JSON is cut in half, or
// presents half an answer as final and persists it.
func TestChatTurnRejectsTruncatedStream(t *testing.T) {
	chunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":"{\"argv\":[\"rm\",\"-rf\",\"/tm"}}]}}]}`
	srv := sseServer(t, []string{chunk}, false) // no [DONE]
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Stream:   true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("truncated stream reported as success: content=%q tools=%+v finish=%q",
			resp.Content, resp.ToolCalls, resp.FinishReason)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "truncat") &&
		!strings.Contains(strings.ToLower(err.Error()), "incomplete") {
		t.Fatalf("error should name the truncation, got: %v", err)
	}
}

// A complete stream that ends with finish_reason but no [DONE] is fine.
func TestChatTurnAcceptsFinishReasonWithoutDone(t *testing.T) {
	chunk := `{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`
	srv := sseServer(t, []string{chunk}, false)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("finish_reason is a valid completion signal: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content=%q", resp.Content)
	}
}

// Some upstreams omit "index" on tool_call fragments. It decodes to 0, so every
// distinct call collapses onto the same map key: earlier calls are erased and
// their arguments concatenated into garbage. The non-streaming path returns
// both calls correctly, so behaviour silently depends on whether streaming is on.
func TestStreamToolCallsWithoutIndexStaySeparate(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_a","function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_b","function":{"name":"g","arguments":"{\"b\":2}"}}]},"finish_reason":"tool_calls"}]}`,
	}
	srv := sseServer(t, chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("expected 2 distinct tool calls, got %d: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	byID := map[string]string{}
	for _, tc := range resp.ToolCalls {
		byID[tc.ID] = tc.Function.Arguments
	}
	if byID["call_a"] != `{"a":1}` || byID["call_b"] != `{"b":2}` {
		t.Fatalf("arguments corrupted by index collision: %+v", byID)
	}
}
