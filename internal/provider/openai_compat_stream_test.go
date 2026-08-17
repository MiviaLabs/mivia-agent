package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestReadTurnStreamSurfacedDeadlineError covers readTurnStream's
// scanner-error deadline branch: a DeadlineExceeded surfaced by the underlying
// reader (rather than a clean EOF from the transport closing a stalled
// connection) must produce an error naming the armed request deadline while
// staying errors.Is(context.DeadlineExceeded).
func TestReadTurnStreamSurfacedDeadlineError(t *testing.T) {
	c := &OpenAICompat{name: "test", errorParser: openaiErrorParser}
	_, _, _, _, _, _, _, err := c.readTurnStream(
		context.Background(),
		deadlineReadErrorReader{},
		io.Discard,
		50*time.Millisecond,
	)
	if err == nil {
		t.Fatal("expected a deadline error from the failing stream reader")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), "request deadline") || !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("err=%q should name the armed 50ms request deadline", err)
	}
}

// --- RED: transient 200-in-band error replays once non-streamed ---
//
// A provider can answer a streaming chat.completions request with HTTP 200 and
// an in-band error envelope ({"error":{"type":"server_error",...}}) instead of
// an SSE completion. The status line is already committed and no content was
// delivered, so this is a transient provider fault that just failed to produce
// an answer. Both stream paths must re-ask the turn once non-streamed instead
// of surfacing it as a terminal failure.
//
// The error envelope reaches the stream parsers as an SSE chunk so the error
// parser is exercised on the stream request; the non-streamed re-ask is
// answered with a plain JSON completion (NOT an SSE stream). Today both paths
// return the parser's error with no replay, so the "re-ask once" cases below
// are RED.
//
// transient200InBandServer answers the streaming request (stream:true) with the
// supplied SSE chunks and any non-streamed request with a plain-JSON completion.
// It records the total request count and the stream flag of every request.
func transient200InBandServer(t *testing.T, streamChunks []string, retryBody string) (*httptest.Server, *int32, *[]bool) {
	t.Helper()
	var calls int32
	var flags []bool
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		var payload chatRequestBody
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		flags = append(flags, payload.Stream)
		mu.Unlock()
		if !payload.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, retryBody)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range streamChunks {
			_, _ = io.WriteString(w, "data: "+c+"\n\n")
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	return srv, &calls, &flags
}

const transient200InBandErrorChunk = `{"error":{"type":"server_error","message":"boom"}}`

const transient200InBandRetryBody = `{"choices":[{"message":{"role":"assistant","content":"retried"},"finish_reason":"stop"}]}`

// Path A: ChatStream, no tools -> readStream.

// TestChatStreamReplaysTransient200InBandOnce locks that a 200-in-band
// server_error envelope (nothing received) is re-asked exactly once
// non-streamed. RED today: readStream returns the parser error with no replay.
func TestChatStreamReplaysTransient200InBandOnce(t *testing.T) {
	srv, calls, flags := transient200InBandServer(t,
		[]string{transient200InBandErrorChunk},
		transient200InBandRetryBody)
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	var sink strings.Builder
	out, err := c.ChatStream(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, &sink)
	if err != nil {
		t.Fatalf("a transient 200-in-band stream error must be re-asked once non-streamed, got: %v", err)
	}
	if out != "retried" || sink.String() != "retried" {
		t.Fatalf("out=%q writer=%q, want %q", out, sink.String(), "retried")
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("made %d requests, want 2 (failed stream + one non-streamed re-ask)", got)
	}
	if len(*flags) != 2 || (*flags)[1] {
		t.Fatalf("retry stream flags = %v, want [true false] (a non-streamed re-ask)", *flags)
	}
}

// TestChatStreamReplaysTransient200InBandDisabled locks the replay-off variant:
// the transient 200-in-band error must propagate and the provider must be asked
// only once. RED today: the surfaced error is not transient.
func TestChatStreamReplaysTransient200InBandDisabled(t *testing.T) {
	srv, calls, _ := transient200InBandServer(t,
		[]string{transient200InBandErrorChunk},
		transient200InBandRetryBody)
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.ChatStream(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, DisableProviderReplay: true,
	}, io.Discard)
	if err == nil {
		t.Fatal("a 200-in-band stream error must propagate when replay is disabled")
	}
	if !IsTransient(err) {
		t.Fatalf("server_error in-band must be transient, got IsTransient=false: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("replay-disabled in-band error made %d requests, want 1", got)
	}
}

// TestChatStreamReplaysTransient200InBandContentWinsNoReplay locks the delivery
// boundary: once content has been delivered, a later in-band error must NOT
// trigger a non-streamed re-ask (1 request) and the error propagates, matching
// today's partial-content+error semantics. Preservation test (green today).
func TestChatStreamReplaysTransient200InBandContentWinsNoReplay(t *testing.T) {
	srv, calls, _ := transient200InBandServer(t,
		[]string{
			`{"choices":[{"delta":{"content":"hel"}}]}`,
			transient200InBandErrorChunk,
		},
		transient200InBandRetryBody)
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.ChatStream(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, io.Discard)
	if err == nil {
		t.Fatal("a partial stream followed by an in-band error must still surface the error")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("content-delivered stream with in-band error made %d requests, want 1 (no re-ask)", got)
	}
}

// Path B: chatTurnStream, tools path.

// TestChatTurnStreamReplaysTransient200InBandOnce locks the same re-ask-once
// behaviour for the tool-capable path. RED today: chatTurnStream returns the
// parser error with no replay.
func TestChatTurnStreamReplaysTransient200InBandOnce(t *testing.T) {
	srv, calls, flags := transient200InBandServer(t,
		[]string{transient200InBandErrorChunk},
		transient200InBandRetryBody)
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true, Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools: []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file"}}},
	})
	if err != nil {
		t.Fatalf("a transient 200-in-band stream error must be re-asked once non-streamed on the tools path, got: %v", err)
	}
	if resp == nil || resp.Content != "retried" {
		t.Fatalf("resp=%+v, want content %q", resp, "retried")
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("made %d requests, want 2 (failed stream + one non-streamed re-ask)", got)
	}
	if len(*flags) != 2 || (*flags)[1] {
		t.Fatalf("retry stream flags = %v, want [true false] (a non-streamed re-ask)", *flags)
	}
}

// TestChatTurnStreamReplaysTransient200InBandDisabled locks the replay-off
// variant on the tools path. RED today: the surfaced error is not transient.
func TestChatTurnStreamReplaysTransient200InBandDisabled(t *testing.T) {
	srv, calls, _ := transient200InBandServer(t,
		[]string{transient200InBandErrorChunk},
		transient200InBandRetryBody)
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true, DisableProviderReplay: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file"}}},
	})
	if err == nil {
		t.Fatal("a 200-in-band stream error must propagate when replay is disabled")
	}
	if !IsTransient(err) {
		t.Fatalf("server_error in-band must be transient, got IsTransient=false: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("replay-disabled in-band error made %d requests, want 1", got)
	}
}

// TestChatTurnStreamReplaysTransient200InBandContentWinsNoReplay locks the
// delivery boundary on the tools path: content delivered before the in-band
// error means no re-ask (1 request), error propagates. Preservation (green).
func TestChatTurnStreamReplaysTransient200InBandContentWinsNoReplay(t *testing.T) {
	srv, calls, _ := transient200InBandServer(t,
		[]string{
			`{"choices":[{"delta":{"content":"hel"}}]}`,
			transient200InBandErrorChunk,
		},
		transient200InBandRetryBody)
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true, Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools: []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file"}}},
	})
	if err == nil {
		t.Fatal("a partial stream followed by an in-band error must still surface the error")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("content-delivered stream with in-band error made %d requests, want 1 (no re-ask)", got)
	}
}

// --- RED: top-level web_search on SSE chunks is silently dropped ---
//
// The wire struct chatResponseBody decodes a top-level "web_search" field on
// every SSE chunk (openai_compat.go), and the non-stream ChatTurn path honors
// body.WebSearch first. The stream path's applyStreamChunk only ever appends
// ch.Delta.WebSearch and never reads chunk.WebSearch, so a provider that emits
// search results at the chunk top level (choices may still carry a content
// delta, and the trailing/usage-style chunks commonly carry an empty choices
// array) loses them entirely: Response.WebSearch comes back empty.
//
// These tests are RED on the current code (empty WebSearch and/or a 2-call
// non-stream re-bill) and go GREEN after the minimal fix.

// TestChatTurnStreamSurfacesTopLevelWebSearch (case a): an SSE chunk carrying
// top-level web_search alongside a content delta must surface the entries in
// Response.WebSearch, with no re-ask (calls==1).
func TestChatTurnStreamSurfacesTopLevelWebSearch(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"content":"answer"}}],"web_search":[{"title":"one","link":"https://example.com"}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	srv, calls := countingSSEServer(t, chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("made %d upstream requests, want 1 (no re-ask)", got)
	}
	if resp.Content != "answer" {
		t.Fatalf("content=%q, want %q", resp.Content, "answer")
	}
	if len(resp.WebSearch) != 1 || resp.WebSearch[0].Title != "one" || resp.WebSearch[0].Link != "https://example.com" {
		t.Fatalf("WebSearch=%+v, want the top-level entry {title:one link:https://example.com}", resp.WebSearch)
	}
}

// TestChatTurnStreamCapturesTopLevelWebSearchOnEmptyChoicesChunk (case b): an
// empty-choices chunk carrying top-level web_search must be captured - the
// mirror of the trailing-usage-only chunk case, which already appends before
// the choices-length guard. Two sub-cases: (b1) a trailing empty-choices chunk
// after content preserves its entry (calls==1); (b2) a sole-signal
// web_search-only empty-choices chunk must count as received so the non-stream
// fallback is NOT re-triggered (calls==1 and the entry surfaces).
func TestChatTurnStreamCapturesTopLevelWebSearchOnEmptyChoicesChunk(t *testing.T) {
	tests := []struct {
		name       string
		chunks     []string
		wantCalls  int32
		wantTitles []string
	}{
		{
			name: "trailing empty-choices chunk preserves the entry",
			chunks: []string{
				`{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
				`{"choices":[],"web_search":[{"title":"trailing"}]}`,
			},
			wantCalls:  1,
			wantTitles: []string{"trailing"},
		},
		{
			name: "sole-signal web_search-only chunk counts as received",
			chunks: []string{
				`{"choices":[],"web_search":[{"title":"only"}]}`,
			},
			wantCalls:  1,
			wantTitles: []string{"only"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, calls := countingSSEServer(t, tt.chunks, true)
			defer srv.Close()

			c := streamingClient(t, srv)
			resp, err := c.ChatTurn(context.Background(), Request{
				Model: "m", Stream: true,
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("ChatTurn: %v", err)
			}
			if got := atomic.LoadInt32(calls); got != tt.wantCalls {
				t.Fatalf("made %d upstream requests, want %d (no re-bill)", got, tt.wantCalls)
			}
			if len(resp.WebSearch) != len(tt.wantTitles) {
				t.Fatalf("WebSearch=%+v, want %d entries", resp.WebSearch, len(tt.wantTitles))
			}
			for i, want := range tt.wantTitles {
				if resp.WebSearch[i].Title != want {
					t.Fatalf("WebSearch[%d].Title=%q, want %q (entries=%+v)", i, resp.WebSearch[i].Title, want, resp.WebSearch)
				}
			}
		})
	}
}

// TestChatTurnStreamAccumulatesTopLevelWebSearchInOrder (case c): two chunks
// each carrying top-level web_search accumulate in arrival order with no
// dedupe - including a same-entry-twice sub-case.
func TestChatTurnStreamAccumulatesTopLevelWebSearchInOrder(t *testing.T) {
	tests := []struct {
		name       string
		chunks     []string
		wantTitles []string
	}{
		{
			name: "ordered accumulation across chunks",
			chunks: []string{
				`{"choices":[{"delta":{"content":"a"}}],"web_search":[{"title":"one"}]}`,
				`{"choices":[{"delta":{"content":"b"}}],"web_search":[{"title":"two"}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			},
			wantTitles: []string{"one", "two"},
		},
		{
			name: "same entry twice is not deduped",
			chunks: []string{
				`{"choices":[{"delta":{"content":"a"}}],"web_search":[{"title":"dup"}]}`,
				`{"choices":[{"delta":{"content":"b"}}],"web_search":[{"title":"dup"}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			},
			wantTitles: []string{"dup", "dup"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, calls := countingSSEServer(t, tt.chunks, true)
			defer srv.Close()

			c := streamingClient(t, srv)
			resp, err := c.ChatTurn(context.Background(), Request{
				Model: "m", Stream: true,
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("ChatTurn: %v", err)
			}
			if got := atomic.LoadInt32(calls); got != 1 {
				t.Fatalf("made %d upstream requests, want 1", got)
			}
			if len(resp.WebSearch) != len(tt.wantTitles) {
				t.Fatalf("WebSearch=%+v, want %d entries in order", resp.WebSearch, len(tt.wantTitles))
			}
			for i, want := range tt.wantTitles {
				if resp.WebSearch[i].Title != want {
					t.Fatalf("WebSearch[%d].Title=%q, want %q (entries=%+v)", i, resp.WebSearch[i].Title, want, resp.WebSearch)
				}
			}
		})
	}
}

// TestChatTurnStreamSkipsMalformedChunkThenProcessesLaterChunks (case d): a
// garbage chunk (invalid JSON) and a JSON type-mismatch chunk are skipped
// without a crash, and a following valid content chunk still delivers with
// calls==1. Passes unchanged on the current code (preservation).
func TestChatTurnStreamSkipsMalformedChunkThenProcessesLaterChunks(t *testing.T) {
	chunks := []string{
		`not json at all`,
		`{"choices":[{"delta":{"content":123}}]}`,
		`{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
	}
	srv, calls := countingSSEServer(t, chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("made %d upstream requests, want 1", got)
	}
	if resp.Content != "answer" || resp.FinishReason != "stop" {
		t.Fatalf("content=%q finish=%q, want answer/stop", resp.Content, resp.FinishReason)
	}
}
