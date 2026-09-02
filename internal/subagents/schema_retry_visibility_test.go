package subagents_test

// Regression coverage for the silent schema-repair retry: runValidatedReply
// (internal/subagents/multi_step_schema.go) used to re-enter loop.Run with a
// corrective turn on schema-validation failure with ZERO observable signal.
// A user watching the subagent's dialog would see what read as a complete
// first-attempt report, then nothing, while a second (or third) full LLM
// call ran behind the scenes - indistinguishable from a stalled task.
//
// These tests pin the fix: a visible agent.EventSchemaRetry event must fire
// exactly once per retry attempt, before the retry's loop.Run call, and
// must never fire when no retry happens (first attempt valid, or the retry
// budget is exhausted without a further attempt).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// orderedSchemaCompleter is scriptedSchemaCompleter plus a shared, mutex
// guarded log so tests can assert interleaving between provider calls and
// emitted events, not just counts.
type orderedSchemaCompleter struct {
	mu      *sync.Mutex
	log     *[]string
	replies []string
	i       int
}

func (c *orderedSchemaCompleter) Name() string { return "ordered-schema" }
func (c *orderedSchemaCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", errors.New("Chat unused")
}
func (c *orderedSchemaCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", errors.New("ChatStream unused")
}
func (c *orderedSchemaCompleter) ChatTurn(_ context.Context, _ provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	*c.log = append(*c.log, fmt.Sprintf("call:%d", c.i+1))
	c.mu.Unlock()
	var r string
	if c.i >= len(c.replies) {
		r = `{"ok":true}`
	} else {
		r = c.replies[c.i]
	}
	c.i++
	return &provider.Response{Content: r, FinishReason: "stop"}, nil
}

func newOrderedRetryHandler(replies []string, retryMax int, mu *sync.Mutex, log *[]string) *subagents.MultiStepHandler {
	reg := tools.NewRegistry()
	return &subagents.MultiStepHandler{
		Completer:      &orderedSchemaCompleter{mu: mu, log: log, replies: replies},
		FullRegistry:   reg,
		Model:          "m",
		MaxSteps:       10,
		SchemaRetryMax: retryMax,
		OutputSchema:   schemaObject(),
		OnEvent: func(e agent.Event) {
			switch e.Kind {
			case agent.EventSchemaRetry:
				mu.Lock()
				*log = append(*log, "retry:"+e.Detail)
				mu.Unlock()
			case agent.EventAssistantReset:
				mu.Lock()
				*log = append(*log, "reset:"+e.Detail)
				mu.Unlock()
			}
		},
	}
}

func invokeOrderedRetry(t *testing.T, h *subagents.MultiStepHandler) (map[string]any, error) {
	t.Helper()
	input, _ := json.Marshal("do the work")
	out, err := h.Invoke(context.Background(), runtime.Request{
		ID: "task-retry-visibility", Name: "worker", Kind: runtime.Subagent, Input: input,
		OutputSchema: schemaObject(),
	})
	var payload map[string]any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &payload)
	}
	return payload, err
}

// A: first reply fails validation, second succeeds. The retry-visible event
// must fire exactly once, carrying attempt 1 of (retryMax+1)=3, and must
// land in the log BEFORE the second provider call.
func TestSchemaRetryVisibility_FiresBeforeRetryCall(t *testing.T) {
	var mu sync.Mutex
	var log []string
	h := newOrderedRetryHandler([]string{`not json`, `{"ok":true}`}, 2, &mu, &log)

	payload, err := invokeOrderedRetry(t, h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload["schema"] != "ok" {
		t.Fatalf("want schema ok after retry, got %#v", payload)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	wantSeq := []string{"call:1", "retry:schema validation failed on attempt 1/3, retrying...", "reset:", "call:2"}
	if len(got) != len(wantSeq) {
		t.Fatalf("log = %#v, want %#v", got, wantSeq)
	}
	for i, w := range wantSeq {
		if got[i] != w {
			t.Fatalf("log[%d] = %q, want %q (full log %#v)", i, got[i], w, got)
		}
	}
}

// B: every attempt fails validation, exhausting SchemaRetryMax. The
// retry-visible event must fire once PER retry attempt (not zero, not
// once-total), and the exhaustion error/result shape must be unchanged.
func TestSchemaRetryVisibility_FiresOncePerAttemptOnExhaustion(t *testing.T) {
	var mu sync.Mutex
	var log []string
	distinctInvalid := []string{`nope`, `still nope`, `and again`}
	h := newOrderedRetryHandler(distinctInvalid, 2, &mu, &log)

	payload, err := invokeOrderedRetry(t, h)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation (exhaustion behavior must be unchanged)", err)
	}
	if payload["schema"] != "violation" {
		t.Fatalf("schema = %v, want violation (payload=%#v)", payload["schema"], payload)
	}
	if _, hasOutput := payload["output"]; hasOutput {
		t.Fatalf("output must be deleted on schema violation, got %#v", payload)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	// 3 provider calls (attempt 0, 1, 2) but only 2 retries fire: the retry
	// event announces a re-entry ABOUT to happen, and attempt 2's failure
	// exhausts the budget instead of retrying again.
	retryCount := 0
	for _, l := range got {
		if len(l) >= 6 && l[:6] == "retry:" {
			retryCount++
		}
	}
	if retryCount != 2 {
		t.Fatalf("retry event count = %d, want exactly 2 (log=%#v)", retryCount, got)
	}
	wantSeq := []string{
		"call:1",
		"retry:schema validation failed on attempt 1/3, retrying...",
		"reset:",
		"call:2",
		"retry:schema validation failed on attempt 2/3, retrying...",
		"reset:",
		"call:3",
		"reset:schema retry budget exhausted: the reply was never accepted",
	}
	if len(got) != len(wantSeq) {
		t.Fatalf("log = %#v, want %#v", got, wantSeq)
	}
	for i, w := range wantSeq {
		if got[i] != w {
			t.Fatalf("log[%d] = %q, want %q (full log %#v)", i, got[i], w, got)
		}
	}
}

// D: every attempt fails validation. This is the discriminator for the
// exhaustion-path reset gap: the two mid-retry resets already fired before
// this fix; the fix adds exactly one more, for the FINAL rejected attempt,
// so the "completed" bubble that attempt's finalizeSDKTurn already
// published on the wire gets retracted instead of lingering mislabeled.
func TestSchemaRetryVisibility_ExhaustionEmitsAssistantResetForFinalAttempt(t *testing.T) {
	var mu sync.Mutex
	var log []string
	distinctInvalid := []string{`nope`, `still nope`, `and again`}
	h := newOrderedRetryHandler(distinctInvalid, 2, &mu, &log)

	_, err := invokeOrderedRetry(t, h)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation", err)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	resetCount := 0
	var lastReset string
	for _, l := range got {
		if len(l) >= 6 && l[:6] == "reset:" {
			resetCount++
			lastReset = l
		}
	}
	if resetCount != 3 {
		t.Fatalf("reset event count = %d, want exactly 3 (2 mid-retry + 1 for the final exhausted attempt); log=%#v", resetCount, got)
	}
	wantLast := "reset:schema retry budget exhausted: the reply was never accepted"
	if lastReset != wantLast {
		t.Fatalf("final reset = %q, want %q (log=%#v)", lastReset, wantLast, got)
	}
}

// E: the "no progress" early exit (a retry produced the identical invalid
// output as the previous attempt) must also retract its wire-published
// "completed" bubble, and must not be confused with budget exhaustion - the
// error text and reset Detail are distinct.
func TestSchemaRetryVisibility_NoProgressEmitsAssistantReset(t *testing.T) {
	var mu sync.Mutex
	var log []string
	// Every call returns the SAME invalid text: attempt 0 sets lastInvalid,
	// attempt 1's candidate equals it, so the no-progress branch exits at
	// attempt 1 - before the retry budget (2) would otherwise be exhausted.
	h := newOrderedRetryHandler([]string{`nope`, `nope`, `nope`}, 2, &mu, &log)

	_, err := invokeOrderedRetry(t, h)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation", err)
	}
	if err == nil || !strings.Contains(err.Error(), "no progress on schema repair") {
		t.Fatalf("err = %v, want the unchanged 'no progress on schema repair' message", err)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	resetCount := 0
	var lastReset string
	for _, l := range got {
		if len(l) >= 6 && l[:6] == "reset:" {
			resetCount++
			lastReset = l
		}
	}
	if resetCount != 2 {
		t.Fatalf("reset event count = %d, want exactly 2 (1 mid-retry + 1 for the no-progress exit); log=%#v", resetCount, got)
	}
	wantLast := "reset:schema repair made no progress: the reply was never accepted"
	if lastReset != wantLast {
		t.Fatalf("final reset = %q, want %q (log=%#v)", lastReset, wantLast, got)
	}
}

// F: regression guard - the success-after-one-retry path (test A above)
// must keep emitting exactly the one mid-retry reset it emitted before this
// fix, never an extra one, proving the new terminal-failure resets cannot
// accidentally fire on a path that ends in success.
func TestSchemaRetryVisibility_SuccessAfterRetryStillEmitsExactlyOneReset(t *testing.T) {
	var mu sync.Mutex
	var log []string
	h := newOrderedRetryHandler([]string{`not json`, `{"ok":true}`}, 2, &mu, &log)

	_, err := invokeOrderedRetry(t, h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	resetCount := 0
	for _, l := range got {
		if len(l) >= 6 && l[:6] == "reset:" {
			resetCount++
		}
	}
	if resetCount != 1 {
		t.Fatalf("reset event count = %d, want exactly 1 (log=%#v)", resetCount, got)
	}
}

// C: first reply already passes validation - no retry needed. The
// retry-visible event must never fire, proving the signal is retry-specific
// rather than emitted unconditionally on every reply.
func TestSchemaRetryVisibility_NeverFiresWhenValidFirstTry(t *testing.T) {
	var mu sync.Mutex
	var log []string
	h := newOrderedRetryHandler([]string{`{"ok":true}`}, 2, &mu, &log)

	payload, err := invokeOrderedRetry(t, h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload["schema"] != "ok" {
		t.Fatalf("want schema ok, got %#v", payload)
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()

	if len(got) != 1 || got[0] != "call:1" {
		t.Fatalf("log = %#v, want exactly [call:1] (no retry event)", got)
	}
}
