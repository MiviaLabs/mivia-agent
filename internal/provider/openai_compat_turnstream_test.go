package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// streamTransportServer routes on the wire stream flag, counting every
// request. A stream:true body runs streamFn; anything else runs jsonFn (the
// terminal non-stream attempt shape). It also records the last request body
// so tests can assert which shape the provider actually saw.
func streamTransportServer(t *testing.T, streamFn, jsonFn func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *int32, *[]byte) {
	t.Helper()
	var calls int32
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		lastBody = body
		if bytes.Contains(body, []byte(`"stream":true`)) {
			streamFn(w, r)
			return
		}
		jsonFn(w, r)
	}))
	return srv, &calls, &lastBody
}

// sseChunks writes each chunk as one data event, then [DONE], and flushes.
func sseChunks(w http.ResponseWriter, chunks []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		_, _ = w.Write([]byte("data: " + c + "\n\n"))
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// trickleLine writes line every tick until the request context ends. It is
// the keepalive dribble: bytes keep flowing, the model answer never advances.
func trickleLine(w http.ResponseWriter, r *http.Request, line string, tick time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(tick):
			if _, err := io.WriteString(w, line); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// trickleChunksThenDone writes line every tick for n rounds - every round a
// progress line, so the content deadline keeps moving - then closes the
// stream with a finish chunk and [DONE].
func trickleChunksThenDone(w http.ResponseWriter, r *http.Request, line string, tick time.Duration, n int) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	f.Flush()
	for i := 0; i < n; i++ {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(tick):
		}
		if _, err := io.WriteString(w, line); err != nil {
			return
		}
		f.Flush()
	}
	sseChunks(w, []string{`{"choices":[{"delta":{},"finish_reason":"stop"}]}`})
}

// keepaliveLine is a pure keepalive: an SSE comment, never a data line.
const keepaliveLine = ": keepalive\n\n"

// roleOnlyLine is a data line that decodes to no accumulator contribution:
// enough to prove the stream speaks SSE, not enough to count as progress.
const roleOnlyLine = "data: " + `{"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"

func jsonAnswer(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fallback"},"finish_reason":"stop"}]}`))
}

func streamTransportClient(t *testing.T, srv *httptest.Server, cacheUsage bool) *OpenAICompat {
	t.Helper()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	return NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: cacheUsage})
}

func turnStreamReq() Request {
	return Request{
		Model:           "m",
		StreamTransport: true,
		Messages:        []Message{{Role: RoleUser, Content: "hi"}},
	}
}

// TestChatTurnStreamTransportWireBodyStreamsTrue locks the wire contract: a
// StreamTransport request reaches the provider with "stream":true even though
// the caller asked for the non-stream shape.
func TestChatTurnStreamTransportWireBodyStreamsTrue(t *testing.T) {
	srv, calls, lastBody := streamTransportServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			sseChunks(w, []string{`{"choices":[{"delta":{"content":"streamed"}}]}`, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`})
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	resp, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "streamed" {
		t.Fatalf("content = %q, want %q", resp.Content, "streamed")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("requests = %d, want 1 (no fallback on a clean stream)", got)
	}
	if !bytes.Contains(*lastBody, []byte(`"stream":true`)) {
		t.Fatalf("wire body missing \"stream\":true: %s", *lastBody)
	}
}

// TestChatTurnStreamTransportResponseParityThroughSSE proves the assembled
// *Response matches the non-stream contract field for field: content, every
// reasoning shape, tool-call fragment merge, delta-level web_search, finish
// reason, token usage, and cache usage.
func TestChatTurnStreamTransportResponseParityThroughSSE(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"content":"Hello "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"think-a"}}]}`,
		`{"choices":[{"delta":{"reasoning":"think-b"}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"thinking","text":"think-c"}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"delta":{"web_search":[{"title":"ws"}]}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}`,
	}
	srv, calls, _ := streamTransportServer(t,
		func(w http.ResponseWriter, _ *http.Request) { sseChunks(w, chunks) },
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, true)
	resp, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if resp.Content != "Hello " {
		t.Fatalf("content = %q", resp.Content)
	}
	if resp.ReasoningContent != "think-athink-bthink-c" {
		t.Fatalf("reasoning = %q, want the three shapes folded in order", resp.ReasoningContent)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Function.Name != "f" || resp.ToolCalls[0].Function.Arguments != `{"a":1}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q", resp.FinishReason)
	}
	if len(resp.WebSearch) != 1 || resp.WebSearch[0].Title != "ws" {
		t.Fatalf("web_search = %+v", resp.WebSearch)
	}
	if !resp.TokenUsage.Reported || resp.TokenUsage.InputTokens != 100 {
		t.Fatalf("token usage = %+v", resp.TokenUsage)
	}
	wantCache := CacheUsage{Reported: true, Style: CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80}
	if resp.CacheUsage != wantCache {
		t.Fatalf("cache usage = %+v, want %+v", resp.CacheUsage, wantCache)
	}
}

// TestChatTurnStreamTransportContentTrickleCompletes proves the content
// watchdog does not punish a merely slow stream: content chunks arriving
// inside the bound complete untouched.
func TestChatTurnStreamTransportContentTrickleCompletes(t *testing.T) {
	withStreamContentIdleTimeout(t, 400*time.Millisecond)
	srv, calls, _ := streamTransportServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			trickleChunksThenDone(w, r, "data: "+`{"choices":[{"delta":{"content":"x"}}]}`+"\n\n", 40*time.Millisecond, 6)
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	resp, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err != nil {
		t.Fatalf("content trickle must complete, got: %v", err)
	}
	if resp.Content != "xxxxxx" {
		t.Fatalf("content = %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q", resp.FinishReason)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("requests = %d, want 1 (a slow-but-progressing stream never retries)", got)
	}
}

// TestChatTurnStreamTransportKeepaliveTrickleAborts proves the incident fix:
// a connection whose keepalive dribble feeds every byte-level watchdog still
// aborts inside the content-idle bound, and the surfaced error is the
// transient stall class.
func TestChatTurnStreamTransportKeepaliveTrickleAborts(t *testing.T) {
	withStreamContentIdleTimeout(t, 300*time.Millisecond)
	srv, calls, _ := streamTransportServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			trickleLine(w, r, keepaliveLine, 25*time.Millisecond)
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	_, err := c.ChatTurn(context.Background(), Request{
		Model: "m", StreamTransport: true, DisableProviderReplay: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("keepalive trickle must abort")
	}
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("abort must wrap ErrStreamIdle, got: %v", err)
	}
	if !IsTransient(err) {
		t.Fatalf("abort must classify as transient, got: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("requests = %d, want 1 (replay disabled: single attempt)", got)
	}
}

// TestChatTurnStreamTransportStallRetryFreshConnection proves the bounded
// stall retry: attempt 1 trickles data lines that never contribute content,
// the watchdog aborts, and attempt 2 on a fresh connection completes. Two
// connections, one logical turn.
func TestChatTurnStreamTransportStallRetryFreshConnection(t *testing.T) {
	withStreamContentIdleTimeout(t, 300*time.Millisecond)
	var attempt int32
	srv, calls, _ := streamTransportServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&attempt, 1) == 1 {
				trickleLine(w, r, roleOnlyLine, 25*time.Millisecond)
				return
			}
			sseChunks(w, []string{`{"choices":[{"delta":{"content":"second try"}}]}`, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`})
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	resp, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "second try" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("requests = %d, want 2 (stall + fresh-connection retry)", got)
	}
}

// TestChatTurnStreamTransportExhaustionSurfacesTransient proves the spend
// order: three stalled SSE attempts, then one failed doJSON attempt, and the
// surfaced cause is the transient stall - retryable at the step level.
func TestChatTurnStreamTransportExhaustionSurfacesTransient(t *testing.T) {
	withStreamContentIdleTimeout(t, 200*time.Millisecond)
	srv, calls, lastBody := streamTransportServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			trickleLine(w, r, roleOnlyLine, 25*time.Millisecond)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			// A non-retryable, non-transient failure class: the transport
			// must not expand the attempt count beyond one fallback.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"fallback refused"}}`))
		})
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	_, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err == nil {
		t.Fatal("exhaustion must surface an error")
	}
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("exhaustion must keep the stall as the surfaced cause, got: %v", err)
	}
	if !IsTransient(err) {
		t.Fatalf("exhaustion must surface a transient error, got: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 4 {
		t.Fatalf("requests = %d, want 4 (3 stalled SSE attempts + 1 fallback)", got)
	}
	if !bytes.Contains(*lastBody, []byte(`"stream":false`)) {
		t.Fatalf("last request must be the non-stream fallback, got: %s", *lastBody)
	}
}

// TestChatTurnStreamTransportRejectMarksHostile proves the JSON-rejection
// memory: the first turn falls back and completes, and every later turn goes
// straight to the non-stream endpoint - the server never sees a second
// stream:true.
func TestChatTurnStreamTransportRejectMarksHostile(t *testing.T) {
	srv, calls, lastBody := streamTransportServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnsupportedMediaType)
			_, _ = w.Write([]byte(`{"error":{"message":"streaming is not supported"}}`))
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	resp, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err != nil {
		t.Fatalf("first turn must fall back and complete, got: %v", err)
	}
	if resp.Content != "fallback" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("requests = %d, want 2 (rejected stream + fallback)", got)
	}
	if _, err := c.ChatTurn(context.Background(), turnStreamReq()); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Fatalf("requests = %d, want 3 (second turn went straight to doJSON)", got)
	}
	if bytes.Contains(*lastBody, []byte(`"stream":true`)) {
		t.Fatalf("hostile provider must never see a second stream request, got: %s", *lastBody)
	}
}

// TestChatTurnStreamTransportZeroDataStallMarksHostile proves the
// never-streamed memory: a stall with zero data lines says the provider
// cannot stream at all, so the first turn fails open to the fallback and the
// memory skips the stream endpoint from then on.
func TestChatTurnStreamTransportZeroDataStallMarksHostile(t *testing.T) {
	withStreamContentIdleTimeout(t, 300*time.Millisecond)
	srv, calls, lastBody := streamTransportServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			trickleLine(w, r, keepaliveLine, 25*time.Millisecond)
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	resp, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err != nil {
		t.Fatalf("first turn must fail open to the fallback, got: %v", err)
	}
	if resp.Content != "fallback" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("requests = %d, want 2 (one aborted stall + fallback, no retries)", got)
	}
	if _, err := c.ChatTurn(context.Background(), turnStreamReq()); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Fatalf("requests = %d, want 3 (second turn went straight to doJSON)", got)
	}
	if bytes.Contains(*lastBody, []byte(`"stream":true`)) {
		t.Fatalf("hostile provider must never see a second stream request, got: %s", *lastBody)
	}
}

// TestChatTurnStreamTransportNoDataBodyFallsBack proves the buffered
// non-streaming provider shape: a 200 whose body never carries a data line
// ends with received=false, falls back once, and does NOT mark the provider
// hostile - the next turn still tries the stream.
func TestChatTurnStreamTransportNoDataBodyFallsBack(t *testing.T) {
	srv, calls, _ := streamTransportServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			// A plain JSON body with no SSE framing at all.
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"buffered"},"finish_reason":"stop"}]}`))
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	resp, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "fallback" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("requests = %d, want 2 (empty stream + fallback)", got)
	}
	if _, err := c.ChatTurn(context.Background(), turnStreamReq()); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 4 {
		t.Fatalf("requests = %d, want 4 (no hostile mark: the second turn streams again)", got)
	}
}

// TestChatTurnStreamTransportDONEOnlyEmptyCompletes proves the
// hostile-negative variant: a stream that delivers finish_reason and usage
// and then [DONE] is a completed turn - no retry, no fallback, no hostile
// mark, and an empty answer returned as the real answer it is.
func TestChatTurnStreamTransportDONEOnlyEmptyCompletes(t *testing.T) {
	srv, calls, _ := streamTransportServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			sseChunks(w, []string{`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5}}`})
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	resp, err := c.ChatTurn(context.Background(), turnStreamReq())
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "" || resp.FinishReason != "stop" {
		t.Fatalf("content = %q finish = %q, want empty content and stop", resp.Content, resp.FinishReason)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("requests = %d, want 1 (a finished turn is never re-asked)", got)
	}
	if _, err := c.ChatTurn(context.Background(), turnStreamReq()); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("requests = %d, want 2 (no hostile mark: the second turn streams again)", got)
	}
}

// TestChatTurnStreamTransportDisableReplaySingleAttempt proves the replay
// gate covers the whole precedence: one SSE attempt, no stall retries, and no
// fallback.
func TestChatTurnStreamTransportDisableReplaySingleAttempt(t *testing.T) {
	withStreamContentIdleTimeout(t, 300*time.Millisecond)
	srv, calls, _ := streamTransportServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			trickleLine(w, r, roleOnlyLine, 25*time.Millisecond)
		},
		jsonAnswer)
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	_, err := c.ChatTurn(context.Background(), Request{
		Model: "m", StreamTransport: true, DisableProviderReplay: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("stalled single attempt must fail")
	}
	if !IsTransient(err) || !strings.Contains(err.Error(), "stream") {
		t.Fatalf("error = %v, want the transient stall class", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// TestChatTurnStreamTransportInBandMaxTokensCapFallsBackAndClamps proves an
// in-band 200 SSE max-tokens-cap rejection does not bypass the existing
// clamp-and-retry recovery (clampMaxTokensForRetry, exercised through
// doChatRequest on the non-stream fallback): the stream-transport path falls
// back on the first sign of ErrMaxTokensExceeded instead of retrying the SSE
// endpoint, and the fallback's own clamp loop still halves the cap and
// retries - exactly like every other request path in this client.
func TestChatTurnStreamTransportInBandMaxTokensCapFallsBackAndClamps(t *testing.T) {
	probe := &maxTokensProbe{}
	var streamCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"stream":true`)) {
			atomic.AddInt32(&streamCalls, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: " + maxTokensIncidentEnvelope + "\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		var wire maxTokensWire
		_ = json.Unmarshal(body, &wire)
		mt := wire.MaxTokens
		probe.record(mt)
		if mt != nil && *mt > 20000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := streamTransportClient(t, srv, false)
	mt := 32768
	req := turnStreamReq()
	req.MaxTokens = &mt
	resp, err := c.ChatTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatTurn err=%v, want nil after fallback+clamp", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp=%+v, want Content %q", resp, "ok")
	}
	if got := atomic.LoadInt32(&streamCalls); got != 1 {
		t.Fatalf("stream requests = %d, want 1 (a cap rejection is not a stall, so it never retries the SSE endpoint)", got)
	}
	count, captured := probe.snapshot()
	if count != 2 {
		t.Fatalf("non-stream requests = %d, want 2 (clamp-and-retry)", count)
	}
	if len(captured) != 2 || captured[0] != 32768 || captured[1] != 16384 {
		t.Fatalf("capturedMaxTokens=%v, want [32768 16384]", captured)
	}
}
