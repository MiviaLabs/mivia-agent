package provider

// When a stream stalls before delivering anything, the turn is re-asked
// non-streamed. That re-ask must be the SAME QUESTION: a turn that offered
// tools has to keep offering them, and a tool call in the answer has to reach
// the caller. Rebuilding the request field by field dropped req.Tools, and
// answering through Chat discarded the tool calls, so a network stall quietly
// converted a tool-calling turn into a prose one - the model stopped acting
// and the loop saw a turn that had simply decided to stop.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// stallThenAnswer streams headers and nothing else on the streaming attempt,
// then answers the non-stream re-ask with a tool call. It records whether that
// re-ask still carried the tools array.
type stallThenAnswer struct {
	srv             *httptest.Server
	retriedWithTool atomic.Bool
	nonStreamCalls  atomic.Int32
}

func newStallThenAnswer(t *testing.T) *stallThenAnswer {
	t.Helper()
	probe := &stallThenAnswer{}
	release := make(chan struct{})
	probe.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if streamed, _ := body["stream"].(bool); streamed {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-release // stall: the idle watchdog ends this attempt
			return
		}
		probe.nonStreamCalls.Add(1)
		if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
			probe.retriedWithTool.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`)
	}))
	t.Cleanup(probe.srv.Close)
	t.Cleanup(func() { close(release) })
	return probe
}

func toolCallingRequest(streamWriter io.Writer) Request {
	return Request{
		Model:    "probe-model",
		Messages: []Message{{Role: RoleUser, Content: "list the directory"}},
		Tools: []ToolSpec{{
			"type": "function",
			"function": map[string]any{
				"name":        "list_dir",
				"description": "list a directory",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}},
		Stream:       true,
		StreamWriter: streamWriter,
	}
}

func TestStreamStallFallbackKeepsToolsAndToolCalls(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	probe := newStallThenAnswer(t)
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "probe", BaseURL: probe.srv.URL, APIKey: "k"})

	var sink stringSink
	resp, err := c.ChatTurn(context.Background(), toolCallingRequest(&sink))
	if err != nil {
		t.Fatalf("the stalled turn should have recovered non-streamed: %v", err)
	}
	if probe.nonStreamCalls.Load() == 0 {
		t.Fatal("the stall never triggered a non-stream re-ask")
	}
	if !probe.retriedWithTool.Load() {
		t.Error("the re-ask dropped the tools array; the model was asked a different question")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1: the recovered turn must still be able to act", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Function.Name != "list_dir" {
		t.Errorf("tool name = %q, want %q", resp.ToolCalls[0].Function.Name, "list_dir")
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "tool_calls")
	}
	// Accounting must survive the recovery too, or the turn's tokens vanish.
	if !resp.TokenUsage.Reported {
		t.Error("recovered turn reported no token usage")
	}
}
