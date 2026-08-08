package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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

// TestReadStreamFallbackPropagatesTimeout verifies that when the simple-path
// readStream fallback triggers (empty stream with no content), the Request's
// Timeout is propagated to the fallback Chat call. The test uses a short
// timeout with a slow fallback server: if Timeout is honoured the call fails
// with context.DeadlineExceeded; without the fix it would succeed.
func TestReadStreamFallbackPropagatesTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		// Fallback: hold the response until the test releases it. The only way
		// the client returns before release is the caller's Timeout deadline
		// firing - no sleep, the deadline is the deterministic signal.
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "fallback"}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	var buf strings.Builder
	errCh := make(chan error, 1)
	go func() {
		_, err := c.ChatStream(context.Background(), Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Timeout:  50 * time.Millisecond,
		}, &buf)
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
			t.Fatalf("expected deadline exceeded, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not respect the 50ms Timeout on the fallback request")
	}
	close(release)
}

func TestReadStreamFallbackPreservesToolChoice(t *testing.T) {
	var choices []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		choices = append(choices, body["tool_choice"])
		if len(choices) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "fallback"}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	text, err := c.ChatStream(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, ToolChoice: "none"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if text != "fallback" || len(choices) != 2 || choices[0] != "none" || choices[1] != "none" {
		t.Fatalf("fallback tool choices = %#v, text=%q", choices, text)
	}
}

// partialReadServer returns an httptest server that writes a partial body (no
// Content-Length, no line/JSON terminator), flushes, then blocks until release
// is closed. The client's body read therefore blocks until its deadline fires.
func partialReadServer(release chan struct{}, contentType, partial string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, partial)
		w.(http.Flusher).Flush()
		<-release
	}))
}

// closeOnce closes ch exactly once. It exists so a deferred close can never
// panic on the explicit path, and so a blocked handler is always released
// before srv.Close() waits on outstanding requests.
func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// The deadline-identity tests below cover finding F2: when a provider read
// times out, the surfaced error must say which deadline fired (the armed
// request Timeout, or the transport backstop) while still matching
// errors.Is(err, context.DeadlineExceeded) for the downstream sites in
// internal/cli, internal/agent and internal/coordinator.

func TestChatTurnReadDeadlineIdentifiesArmedRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := partialReadServer(release, "application/json", `{"choices":[{"message":{"content":"par`)
	defer srv.Close()
	defer closeOnce(release)

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "deepseek", BaseURL: srv.URL, APIKey: "k"})
	errCh := make(chan error, 1)
	go func() {
		_, err := c.Chat(context.Background(), Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Timeout:  50 * time.Millisecond,
		})
		errCh <- err
	}()
	var err error
	select {
	case err = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not respect the 50ms Timeout on the body read")
	}
	if err == nil {
		t.Fatal("expected a deadline error from the blocked body read")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), "request deadline") || !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("err=%q should name the armed 50ms request deadline", err)
	}
}

func TestChatTurnReadDeadlineNamesTransportWhenNoRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := partialReadServer(release, "application/json", `{"choices":[{"message":{"content":"par`)
	defer srv.Close()
	defer closeOnce(release)

	// No per-request Timeout armed: only the client's transport backstop can
	// fire. Use a short client Timeout so the test does not wait
	// DefaultHTTPTimeout.
	c := &OpenAICompat{
		name:        "deepseek",
		baseURL:     srv.URL,
		apiKey:      "k",
		errorParser: openaiErrorParser,
		client:      &http.Client{Timeout: 50 * time.Millisecond},
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
		errCh <- err
	}()
	var err error
	select {
	case err = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not respect the transport backstop on the body read")
	}
	if err == nil {
		t.Fatal("expected a deadline error from the blocked body read")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), "request deadline") || !strings.Contains(err.Error(), "transport") {
		t.Fatalf("err=%q should name the transport backstop as the deadline", err)
	}
}

func TestChatStreamReadDeadlineIdentifiesArmedRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := partialReadServer(release, "text/event-stream", `data: {"choices":[{"delta":{"content":"hel`)
	defer srv.Close()
	defer closeOnce(release)

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "deepseek", BaseURL: srv.URL, APIKey: "k"})
	var buf strings.Builder
	errCh := make(chan error, 1)
	go func() {
		_, err := c.ChatStream(context.Background(), Request{
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Timeout:  50 * time.Millisecond,
		}, &buf)
		errCh <- err
	}()
	var err error
	select {
	case err = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not respect the 50ms Timeout on the stream body read")
	}
	if err == nil {
		t.Fatal("expected a deadline error from the blocked stream read")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), "request deadline") || !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("err=%q should name the armed 50ms request deadline", err)
	}
}

func TestChatTurnStreamReadDeadlineIdentifiesArmedRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := partialReadServer(release, "text/event-stream", `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":"{\"p`)
	defer srv.Close()
	defer closeOnce(release)

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "deepseek", BaseURL: srv.URL, APIKey: "k"})
	errCh := make(chan error, 1)
	go func() {
		_, err := c.ChatTurn(context.Background(), Request{
			Model:    "m",
			Stream:   true,
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Tools:    []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file"}}},
			Timeout:  50 * time.Millisecond,
		})
		errCh <- err
	}()
	var err error
	select {
	case err = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not respect the 50ms Timeout on the tool stream read")
	}
	if err == nil {
		t.Fatal("expected a deadline error from the blocked tool stream read")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), "request deadline") || !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("err=%q should name the armed 50ms request deadline", err)
	}
}

// TestChatStreamEmptyStreamFallback is an integration test verifying that when
// a stream produces only [DONE] (no content deltas), ChatStream falls back to
// a non-streaming Chat call and returns the correct content.
func TestChatStreamEmptyStreamFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "fallback response"}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	var buf strings.Builder
	out, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if out != "fallback response" {
		t.Fatalf("got %q, want 'fallback response'", out)
	}
	// Regression: the fallback answer must also reach the stream writer w, or a
	// plain (no-tools) chat turn completes with no visible answer.
	if buf.String() != "fallback response" {
		t.Fatalf("stream writer got %q, want 'fallback response'", buf.String())
	}
}

// TestChatStreamEmptyStreamFallbackNilWriter locks the nil-writer variant of
// the empty-stream fallback: ChatStream must return the fallback content
// without panicking when no writer is supplied.
func TestChatStreamEmptyStreamFallbackNilWriter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "fallback response"}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	out, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "fallback response" {
		t.Fatalf("got %q, want 'fallback response'", out)
	}
}
