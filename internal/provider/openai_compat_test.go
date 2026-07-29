package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestResponseCarriesProviderMetadata(t *testing.T) {
	typ := reflect.TypeOf(Response{})
	reasoning, ok := typ.FieldByName("ReasoningContent")
	if !ok || reasoning.Type.Kind() != reflect.String {
		t.Fatalf("Response.ReasoningContent must be a string field")
	}
	search, ok := typ.FieldByName("WebSearch")
	if !ok || search.Type.Kind() != reflect.Slice || search.Type.Elem().Name() != "WebSearchResult" {
		t.Fatalf("Response.WebSearch must be []WebSearchResult")
	}
}

func TestOpenAICompatCarriesExtensionHooks(t *testing.T) {
	typ := reflect.TypeOf(OpenAICompat{})
	for _, field := range []string{"extraHeaders", "extraBody", "errorParser"} {
		if _, ok := typ.FieldByName(field); !ok {
			t.Fatalf("OpenAICompat must retain %s", field)
		}
	}
}

func TestCompatOptionsAddsClonedRequestHooks(t *testing.T) {
	pointerBody := map[string]any{"value": "original"}
	options := CompatOptions{
		Name:         "test",
		APIKey:       "k",
		ExtraHeaders: map[string]string{"X-Provider-Feature": "original"},
		ExtraBody: map[string]any{
			"provider_feature": "original",
			"provider_nested":  map[string]any{"value": "original"},
			"provider_typed":   map[string]string{"value": "original"},
			"provider_pointer": &pointerBody,
		},
	}
	var gotHeader string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Provider-Feature")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}))
	defer srv.Close()
	options.BaseURL = srv.URL
	c := NewOpenAICompatWithOptions(options)
	options.ExtraHeaders["X-Provider-Feature"] = "mutated"
	options.ExtraBody["provider_feature"] = "mutated"
	options.ExtraBody["provider_nested"].(map[string]any)["value"] = "mutated"
	options.ExtraBody["provider_typed"].(map[string]string)["value"] = "mutated"
	pointerBody["value"] = "mutated"
	if _, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "original" {
		t.Fatalf("header=%q, want cloned original value", gotHeader)
	}
	if gotBody["provider_feature"] != "original" {
		t.Fatalf("body=%v, want cloned original value", gotBody)
	}
	nested, ok := gotBody["provider_nested"].(map[string]any)
	if !ok || nested["value"] != "original" {
		t.Fatalf("nested body=%v, want cloned original value", gotBody)
	}
	typed, ok := gotBody["provider_typed"].(map[string]any)
	if !ok || typed["value"] != "original" {
		t.Fatalf("typed body=%v, want cloned original value", gotBody)
	}
	pointer, ok := gotBody["provider_pointer"].(map[string]any)
	if !ok || pointer["value"] != "original" {
		t.Fatalf("pointer body=%v, want cloned original value", gotBody)
	}
}

func TestCompatOptionsRejectsProtectedRequestOverrides(t *testing.T) {
	for name, options := range map[string]CompatOptions{
		"header case-insensitive": {
			Name: "test", BaseURL: "https://example.com", APIKey: "k",
			ExtraHeaders: map[string]string{"authorization": "Bearer override"},
		},
		"body": {
			Name: "test", BaseURL: "https://example.com", APIKey: "k",
			ExtraBody: map[string]any{"model": "override"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewOpenAICompatWithOptions(options)
			_, err := c.newRequest(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("err=%v, want reserved-field rejection", err)
			}
		})
	}
}

func TestCompatOptionsErrorParserHandlesHTTPError(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":4001,"message":"provider failure"}`))
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: srv.URL, APIKey: "k",
		ErrorParser: func(status int, body []byte) error {
			called = status == http.StatusBadRequest && strings.Contains(string(body), "provider failure")
			return fmt.Errorf("parsed provider error")
		},
	})
	_, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if !called || err == nil || !strings.Contains(err.Error(), "parsed provider error") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

func TestCompatOptionsErrorParserHandlesSuccessfulEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":4001,"message":"provider failure"}`))
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: srv.URL, APIKey: "k",
		ErrorParser: func(status int, body []byte) error {
			if status == http.StatusOK && strings.Contains(string(body), "provider failure") {
				return fmt.Errorf("parsed success envelope")
			}
			return nil
		},
	})
	_, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "parsed success envelope") {
		t.Fatalf("err=%v", err)
	}
}

func TestChatTurnRejectsOversizedJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxJSONResponseBytes+1))
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("err=%v", err)
	}
}

func TestChatTurnPropagatesProviderMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer","reasoning_content":"thought"},"finish_reason":"stop"}],"web_search":[{"title":"source","link":"https://example.com"}]}`))
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ReasoningContent != "thought" || len(resp.WebSearch) != 1 || resp.WebSearch[0].Title != "source" {
		t.Fatalf("response=%+v", resp)
	}
}

func TestChatTurnRetainsNestedWebSearchWhenTopLevelIsAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer","web_search":[{"title":"nested"}]},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.WebSearch) != 1 || resp.WebSearch[0].Title != "nested" {
		t.Fatalf("response=%+v", resp)
	}
}

func TestChatTurnStreamAccumulatesProviderMetadataAndParsesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\",\"reasoning_content\":\"think \",\"web_search\":[{\"title\":\"one\"}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"more\",\"web_search\":[{\"title\":\"two\"}]},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Stream: true, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "answer" || resp.ReasoningContent != "think more" || len(resp.WebSearch) != 2 || resp.FinishReason != "stop" {
		t.Fatalf("response=%+v", resp)
	}
}

func TestChatStreamUsesErrorParserForSSEError(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"code\":4001,\"message\":\"provider failure\"}\n\n")
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: srv.URL, APIKey: "k",
		ErrorParser: func(status int, body []byte) error {
			if status == http.StatusOK && strings.Contains(string(body), "provider failure") {
				return fmt.Errorf("parsed stream envelope")
			}
			return nil
		},
	})
	_, err := c.ChatStream(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "parsed stream envelope") || requests != 1 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

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
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
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

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL + "/v1", APIKey: "fake-key"})
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

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
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

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "fake-key"})
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

// TestChatTurnStream_ContentDeltas verifies ChatTurn with Stream=true writes
// content chunks to StreamWriter as they arrive (agent/TUI live path).
func TestChatTurnStream_ContentDeltas(t *testing.T) {
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
		write("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		write("data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "fake-key"})
	var buf strings.Builder
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:        "m",
		Messages:     []Message{{Role: RoleUser, Content: "hi"}},
		Stream:       true,
		StreamWriter: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content=%q", resp.Content)
	}
	if buf.String() != "hello" {
		t.Fatalf("StreamWriter got %q, want hello", buf.String())
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("unexpected tool calls: %+v", resp.ToolCalls)
	}
}

// TestChatTurnStream_ToolCallsAssembled verifies streaming tool_call fragments
// are assembled into a complete ToolCall without writing args to StreamWriter.
func TestChatTurnStream_ToolCallsAssembled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = io.WriteString(w, s)
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"p"}}]}}]}` + "\n\n")
		write(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ath\":\"a\"}"}}]}}]}` + "\n\n")
		write(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n")
		write("data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "fake-key"})
	var buf strings.Builder
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:        "m",
		Messages:     []Message{{Role: RoleUser, Content: "read"}},
		Stream:       true,
		StreamWriter: &buf,
		Tools: []ToolSpec{
			{"type": "function", "function": map[string]any{"name": "read_file"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("tool args must not go to StreamWriter, got %q", buf.String())
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls=%+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("name=%q", resp.ToolCalls[0].Function.Name)
	}
	if resp.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("args=%q", resp.ToolCalls[0].Function.Arguments)
	}
}

func TestAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "deepseek", BaseURL: srv.URL, APIKey: "bad"})
	_, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("err=%v", err)
	}
}
