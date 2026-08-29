package provider

// CONFORMANCE SUITE — every Completer implementation, one set of assertions.
//
// This file exists because of how the 2026-08-29 subagent-stall bugs happened.
// provider.Completer has two real implementations, and seven separate
// contracts were enforced in OpenAICompat and silently absent from
// AnthropicCompleter: req.StreamTransport, req.DisableProviderReplay, the idle
// watchdog on body reads, and torn-tool-call rejection. Every per-client test
// was green the whole time, because each implementation was only ever tested
// against itself.
//
// A per-implementation test answers "does this client work?". It cannot answer
// "do all our clients agree?", and that second question is where this codebase
// keeps losing. The table below is the answer: a new Completer either joins it
// and passes, or its absence is the visible omission.
//
// Two rules for anyone extending this file:
//  1. Assertions here go through the EXPORTED Completer interface against a
//     real httptest server and a real transport. A fake that bypasses the
//     network cannot fail on a timer or a half-sent body, which is precisely
//     how these bugs survived.
//  2. When you fix a contract bug in one client, add the assertion HERE first
//     and watch it fail for the sibling too.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// wireAnswers supplies one implementation's protocol-specific replies. The
// assertions are shared; only the bytes on the wire differ.
type wireAnswers struct {
	// streamed is a COMPLETE streamed answer carrying one list_dir tool call.
	streamed string
	// nonStreamed is the equivalent complete non-streamed answer.
	nonStreamed string
	// failure is a retryable error body for this provider's error shape.
	failure string
}

type completerCase struct {
	name    string
	answers wireAnswers
	build   func(baseURL string) Completer
}

// conformanceCases lists every Completer implementation in this package.
// llmproxycli's dispatcher is deliberately absent: it owns no wire format of
// its own and forwards per model to one of the two below.
func conformanceCases() []completerCase {
	return []completerCase{
		{
			name: "openai_compat",
			answers: wireAnswers{
				streamed: "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]}}]}` + "\n\n" +
					"data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}` + "\n\n" +
					"data: [DONE]\n\n",
				nonStreamed: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`,
				failure:     `{"error":{"message":"overloaded","type":"server_error"}}`,
			},
			build: func(baseURL string) Completer {
				return NewOpenAICompatWithOptions(CompatOptions{Name: "conformance", BaseURL: baseURL, APIKey: "k"})
			},
		},
		{
			name: "anthropic",
			answers: wireAnswers{
				streamed: anthropicSSEAnswer,
				nonStreamed: `{"content":[{"type":"text","text":"listing it"},` +
					`{"type":"tool_use","id":"toolu_1","name":"list_dir","input":{"path":"."}}],` +
					`"stop_reason":"tool_use","usage":{"input_tokens":9,"output_tokens":4}}`,
				failure: `{"type":"error","error":{"type":"overloaded_error"}}`,
			},
			build: func(baseURL string) Completer {
				return newAnthropicCompleter("conformance", baseURL, "k", nil, false)
			},
		},
	}
}

// conformanceRequest is the shape a nested/subagent turn sends: tools offered,
// no live streaming writer.
func conformanceRequest() Request {
	return Request{
		Model:    "conformance-model",
		Messages: []Message{{Role: RoleUser, Content: "list the directory"}},
		Tools: []ToolSpec{{
			"type": "function",
			"function": map[string]any{
				"name":        "list_dir",
				"description": "list a directory",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}},
	}
}

// answeringServer replies with the streamed or non-streamed answer according
// to what the request body asked for, and counts requests.
func answeringServer(t *testing.T, answers wireAnswers) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if requestAskedForStream(r) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, answers.streamed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answers.nonStreamed)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// requestAskedForStream reads the wire body's stream flag. Both protocols
// spell it the same way, which is why one helper serves both.
func requestAskedForStream(r *http.Request) bool {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	streamed, _ := body["stream"].(bool)
	return streamed
}

// CONTRACT: the wire-stream transport. A turn that sets StreamTransport with
// no StreamWriter must go out as a stream and come back as a complete
// non-stream Response. A client that ignores the flag sends a non-stream
// completion, whose wait for response headers is the whole generation - which
// a header bound then caps regardless of the caller's budget.
func TestCompleterConformance_HonorsStreamTransport(t *testing.T) {
	for _, tc := range conformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			var sawStream atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requestAskedForStream(r) {
					sawStream.Store(true)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, tc.answers.streamed)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.answers.nonStreamed)
			}))
			defer srv.Close()

			req := conformanceRequest()
			req.StreamTransport = true

			resp, err := tc.build(srv.URL).ChatTurn(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if !sawStream.Load() {
				t.Fatal("StreamTransport did not reach the wire; this client sends a non-stream completion, which a header bound caps at the generation's length")
			}
			assertListDirCall(t, resp)
		})
	}
}

// CONTRACT: one attempt means one attempt. A client that lets the flag reach
// the wire as a suggestion replays a billable generation on every transient
// failure.
func TestCompleterConformance_HonorsDisableProviderReplay(t *testing.T) {
	for _, tc := range conformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, tc.answers.failure)
			}))
			defer srv.Close()

			req := conformanceRequest()
			req.DisableProviderReplay = true

			if _, err := tc.build(srv.URL).ChatTurn(context.Background(), req); err == nil {
				t.Fatal("expected the 503 to surface")
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("provider saw %d requests, want exactly 1", got)
			}
		})
	}
}

// CONTRACT: the retry budget still applies when replay is allowed, so the
// guard above cannot be satisfied by never retrying at all.
func TestCompleterConformance_RetriesWhenReplayAllowed(t *testing.T) {
	for _, tc := range conformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, tc.answers.failure)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.answers.nonStreamed)
			}))
			defer srv.Close()

			resp, err := tc.build(srv.URL).ChatTurn(context.Background(), conformanceRequest())
			if err != nil {
				t.Fatalf("a retryable 503 must be retried: %v", err)
			}
			if got := calls.Load(); got < 2 {
				t.Fatalf("provider saw %d requests, want a retry", got)
			}
			assertListDirCall(t, resp)
		})
	}
}

// CONTRACT: every body read is bounded. A peer that accepts the request and
// then sends nothing must fail on the watchdog, not hold the call to the
// transport's absolute wall - and the failure must be transient, because a
// call that delivered nothing can be asked again.
func TestCompleterConformance_BoundsStalledBodyRead(t *testing.T) {
	for _, tc := range conformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
			srv := silentAfterHeaders(t, "application/json", "")

			completer := tc.build(srv.URL)
			err := callWithin(t, 30*time.Second, func() error {
				_, callErr := completer.ChatTurn(context.Background(), conformanceRequest())
				return callErr
			})
			if !errors.Is(err, ErrStreamIdle) {
				t.Fatalf("a stalled body read must surface ErrStreamIdle, got %v", err)
			}
			if !IsTransient(err) {
				t.Errorf("a call that delivered nothing must be transient, got %v", err)
			}
		})
	}
}

// CONTRACT: the plain path returns the same answer shape as the streamed one,
// so a caller cannot tell which wire shape served its turn.
func TestCompleterConformance_NonStreamReturnsSameShape(t *testing.T) {
	for _, tc := range conformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := answeringServer(t, tc.answers)
			resp, err := tc.build(srv.URL).ChatTurn(context.Background(), conformanceRequest())
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("provider saw %d requests, want 1", calls.Load())
			}
			assertListDirCall(t, resp)
		})
	}
}

// assertListDirCall pins the answer every case's fixtures encode: one
// list_dir tool call with intact arguments, a tool_calls finish reason, and
// reported usage. Accounting is asserted because a recovery path that drops it
// silently loses the turn's tokens.
func assertListDirCall(t *testing.T, resp *Response) {
	t.Helper()
	if resp == nil {
		t.Fatal("nil response")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(resp.ToolCalls))
	}
	call := resp.ToolCalls[0]
	if call.Function.Name != "list_dir" {
		t.Errorf("tool name = %q, want list_dir", call.Function.Name)
	}
	if call.ID == "" {
		t.Error("tool call has no ID")
	}
	if !json.Valid([]byte(call.Function.Arguments)) {
		t.Errorf("tool arguments are not valid JSON: %q", call.Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	if !resp.TokenUsage.Reported {
		t.Error("no token usage reported; a turn's accounting must survive every path")
	}
}

// CONTRACT: a request that spends its whole budget and answers nothing is NOT
// transient. Re-asking it burns another full req.Timeout for the same result.
//
// This is the generalised form of the Anthropic bug: markTransientReadDeadline
// must be given the ARMED request context, and a client that hands it an
// ancestor instead inverts the verdict. Asserting it here rather than per
// client means a third Completer inherits the check by joining the table.
func TestCompleterConformance_SpentRequestBudgetIsNotTransient(t *testing.T) {
	// Watchdogs far away so the request deadline is what fires.
	withWatchdogTimeouts(t, time.Minute, time.Minute)

	// Both stall shapes, because they land on DIFFERENT sites: with no headers
	// the send fails, and with headers flushed first the body read fails. A
	// client can classify one correctly and the other backwards.
	for _, phase := range []struct {
		name        string
		sendHeaders bool
	}{{"stalls before headers", false}, {"stalls after headers", true}} {
		for _, tc := range conformanceCases() {
			t.Run(tc.name+"/"+phase.name, func(t *testing.T) {
				// A LIVE parent deadline is the condition that exposes the
				// inversion: with no parent deadline nothing is marked at all.
				parent, cancelParent := context.WithTimeout(context.Background(), time.Minute)
				defer cancelParent()

				srv := stalledServer(t, phase.sendHeaders)
				req := conformanceRequest()
				req.Timeout = 250 * time.Millisecond

				completer := tc.build(srv.URL)
				done := make(chan error, 1)
				go func() { _, err := completer.ChatTurn(parent, req); done <- err }()

				select {
				case err := <-done:
					if err == nil {
						t.Fatal("expected the spent request budget to surface")
					}
					if IsTransient(err) {
						t.Fatalf("a spent request budget must not be transient: %v\n"+
							"  this client is judging the deadline against a context other than\n"+
							"  the armed request context, so the step loop will re-ask a call\n"+
							"  that fails identically after another full req.Timeout", err)
					}
				case <-time.After(30 * time.Second):
					t.Fatal("the request timeout never fired")
				}
			})
		}
	}
}
