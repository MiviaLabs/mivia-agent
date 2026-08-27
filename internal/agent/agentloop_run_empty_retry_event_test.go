package agent

// Regression coverage for the silent empty-response retry: retryOnEmptyResponse
// (agentloop_run.go) re-runs the WHOLE SDK completion loop up to
// maxEmptyResponseRetries times with ZERO observable signal. A user watching
// the turn would see nothing at all while a second (or third) potentially
// multi-minute LLM call ran behind the scenes - indistinguishable from a
// stalled turn. Same silent-retry shape as the schema-repair retry fixed for
// internal/subagents/multi_step_schema.go's runValidatedReply
// (agent.EventSchemaRetry).
//
// These tests pin the fix: a visible agent.EventEmptyResponseRetry event must
// fire exactly once per retry attempt, before the retry's completion call,
// and must never fire when no retry happens (first response already usable).

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestEmptyResponseRetryVisibility_FiresBeforeRetryCall pins ordering: the
// event for attempt 1/(maxEmptyResponseRetries+1) must land in the log AFTER
// the first (empty) completion call and BEFORE the second (recovering) one.
func TestEmptyResponseRetryVisibility_FiresBeforeRetryCall(t *testing.T) {
	var log []string
	calls := 0
	f := &fakeCompleter{name: "fake"}
	f.onChatTurn = func() {
		calls++
		log = append(log, fmt.Sprintf("call:%d", calls))
		if calls == 1 {
			f.chatTurnOut = &provider.Response{Content: "", FinishReason: "stop"}
		} else {
			f.chatTurnOut = &provider.Response{Content: "real answer", FinishReason: "stop"}
		}
	}
	loop := &Loop{Completer: f, Tools: tools.NewRegistry()}
	opts := Options{
		Model: "m", MaxSteps: 3, RequireFinalText: true,
		OnEvent: func(e Event) {
			if e.Kind != EventEmptyResponseRetry {
				return
			}
			log = append(log, "retry:"+e.Detail)
		},
	}
	got, err := loop.Run(context.Background(), "hi", opts)
	if err != nil {
		t.Fatalf("Run: %v, want nil (empty response retried and recovered)", err)
	}
	if got != "real answer" {
		t.Fatalf("got %q, want %q", got, "real answer")
	}

	wantSeq := []string{"call:1", "retry:empty response on attempt 1/3, retrying...", "call:2"}
	if len(log) != len(wantSeq) {
		t.Fatalf("log = %#v, want %#v", log, wantSeq)
	}
	for i, w := range wantSeq {
		if log[i] != w {
			t.Fatalf("log[%d] = %q, want %q (full log %#v)", i, log[i], w, log)
		}
	}
}

// TestEmptyResponseRetryVisibility_FiresOncePerAttemptOnExhaustion pins the
// bound: a completer that ALWAYS returns an empty response fires the event
// once per retry attempt (maxEmptyResponseRetries times, not zero, not once
// total), and the existing exhaustion behavior (error, call count) is
// completely unchanged.
func TestEmptyResponseRetryVisibility_FiresOncePerAttemptOnExhaustion(t *testing.T) {
	var events []string
	calls := 0
	f := &fakeCompleter{
		name:        "fake",
		chatTurnOut: &provider.Response{Content: "", FinishReason: "stop"},
	}
	f.onChatTurn = func() { calls++ }
	loop := &Loop{Completer: f, Tools: tools.NewRegistry()}
	opts := Options{
		Model: "m", MaxSteps: 3, RequireFinalText: true,
		OnEvent: func(e Event) {
			if e.Kind == EventEmptyResponseRetry {
				events = append(events, e.Detail)
			}
		},
	}
	_, err := loop.Run(context.Background(), "hi", opts)
	if err == nil || !strings.Contains(err.Error(), "no assistant text") {
		t.Fatalf("Run error = %v, want a 'no assistant text' failure after exhausting retries", err)
	}
	want := 1 + maxEmptyResponseRetries
	if calls != want {
		t.Fatalf("completer called %d times, want exactly %d (1 initial + %d retries) - exhaustion behavior must be unchanged", calls, want, maxEmptyResponseRetries)
	}
	if len(events) != maxEmptyResponseRetries {
		t.Fatalf("retry event count = %d, want exactly %d (one per retry attempt)", len(events), maxEmptyResponseRetries)
	}
	for i, detail := range events {
		want := fmt.Sprintf("empty response on attempt %d/%d, retrying...", i+1, maxEmptyResponseRetries+1)
		if detail != want {
			t.Fatalf("events[%d] = %q, want %q", i, detail, want)
		}
	}
}

// TestEmptyResponseRetryVisibility_NeverFiresWhenFirstResponseUsable proves
// the signal is retry-specific: a first response that already succeeds must
// never emit EventEmptyResponseRetry.
func TestEmptyResponseRetryVisibility_NeverFiresWhenFirstResponseUsable(t *testing.T) {
	fired := false
	f := &fakeCompleter{
		name:        "fake",
		chatTurnOut: &provider.Response{Content: "first try answer", FinishReason: "stop"},
	}
	calls := 0
	f.onChatTurn = func() { calls++ }
	loop := &Loop{Completer: f, Tools: tools.NewRegistry()}
	opts := Options{
		Model: "m", MaxSteps: 3, RequireFinalText: true,
		OnEvent: func(e Event) {
			if e.Kind == EventEmptyResponseRetry {
				fired = true
			}
		},
	}
	got, err := loop.Run(context.Background(), "hi", opts)
	if err != nil {
		t.Fatalf("Run: %v, want nil", err)
	}
	if got != "first try answer" {
		t.Fatalf("got %q, want %q", got, "first try answer")
	}
	if calls != 1 {
		t.Fatalf("completer called %d times, want exactly 1 (no retry needed)", calls)
	}
	if fired {
		t.Fatal("EventEmptyResponseRetry fired but no retry happened")
	}
}
