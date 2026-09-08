package uiadapter

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// ---------------------------------------------------------------------
// Part A: mergeToolCallSteps
// ---------------------------------------------------------------------

func TestMergeToolCallSteps(t *testing.T) {
	cases := []struct {
		name  string
		steps []toolCallStepMirror
		want  []toolCallSummary
	}{
		{
			name: "complete pair",
			steps: []toolCallStepMirror{
				{ToolCallID: "A", Name: "grep", Kind: "start", Input: "in"},
				{ToolCallID: "A", Kind: "end", Output: "out"},
			},
			want: []toolCallSummary{
				{ToolCallID: "A", Name: "grep", Input: "in", Output: "out", Incomplete: false},
			},
		},
		{
			name: "interleaved distinct ids",
			steps: []toolCallStepMirror{
				{ToolCallID: "A", Name: "grep", Kind: "start", Input: "a-in"},
				{ToolCallID: "B", Name: "read", Kind: "start", Input: "b-in"},
				{ToolCallID: "A", Kind: "end", Output: "a-out"},
				{ToolCallID: "B", Kind: "end", Output: "b-out"},
			},
			want: []toolCallSummary{
				{ToolCallID: "A", Name: "grep", Input: "a-in", Output: "a-out", Incomplete: false},
				{ToolCallID: "B", Name: "read", Input: "b-in", Output: "b-out", Incomplete: false},
			},
		},
		{
			name: "start only is incomplete",
			steps: []toolCallStepMirror{
				{ToolCallID: "A", Name: "grep", Kind: "start", Input: "in"},
			},
			want: []toolCallSummary{
				{ToolCallID: "A", Name: "grep", Input: "in", Incomplete: true},
			},
		},
		{
			name: "same-id overlapping opens pair LIFO into two rows",
			steps: []toolCallStepMirror{
				{ToolCallID: "X", Name: "grep", Kind: "start", Input: "first-in"},
				{ToolCallID: "X", Name: "grep", Kind: "start", Input: "second-in"},
				{ToolCallID: "X", Kind: "end", Output: "closes-second"},
				{ToolCallID: "X", Kind: "end", Output: "closes-first"},
			},
			want: []toolCallSummary{
				{ToolCallID: "X", Name: "grep", Input: "first-in", Output: "closes-first", Incomplete: false},
				{ToolCallID: "X", Name: "grep", Input: "second-in", Output: "closes-second", Incomplete: false},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeToolCallSteps(tc.steps)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeToolCallSteps() =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

// TestMergeToolCallSteps_EdgeCases covers the remaining mergeToolCallSteps
// branches (duplicate-id re-emit, dangling end, unknown kind, empty input).
// Split from TestMergeToolCallSteps to keep both functions under the
// go-structure soft line cap; the two together are one logical table.
func TestMergeToolCallSteps_EdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		steps []toolCallStepMirror
		want  []toolCallSummary
	}{
		{
			name: "duplicate-id sequential re-emit yields two rows",
			steps: []toolCallStepMirror{
				{ToolCallID: "X", Name: "grep", Kind: "start", Input: "first-in"},
				{ToolCallID: "X", Kind: "end", Output: "first-out"},
				{ToolCallID: "X", Name: "grep", Kind: "start", Input: "second-in"},
				{ToolCallID: "X", Kind: "end", Output: "second-out"},
			},
			want: []toolCallSummary{
				{ToolCallID: "X", Name: "grep", Input: "first-in", Output: "first-out", Incomplete: false},
				{ToolCallID: "X", Name: "grep", Input: "second-in", Output: "second-out", Incomplete: false},
			},
		},
		{
			name: "end with no matching start yields nothing",
			steps: []toolCallStepMirror{
				{ToolCallID: "A", Kind: "end", Output: "orphan"},
			},
			want: nil,
		},
		{
			name: "unknown kind is skipped",
			steps: []toolCallStepMirror{
				{ToolCallID: "A", Name: "grep", Kind: "progress"},
			},
			want: nil,
		},
		{
			name:  "empty input yields empty output",
			steps: nil,
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeToolCallSteps(tc.steps)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeToolCallSteps() =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Part B: SubagentTranscriptConversation.History() resolution
// ---------------------------------------------------------------------

// buildPendingConversation returns a reconstructed conversation carrying a
// pending sourceToolCallsRef and the given resolver, with a prompt message
// plus the placeholder assistant notice ahead of it - exactly the shape
// registerDispatchedTask produces for a ref-only result.
func buildPendingConversation(resolver toolCallContentResolver) *SubagentTranscriptConversation {
	history := []ports.Message{
		{Role: "user", Text: "do the work", At: time.Now()},
		{Role: "assistant", Text: toolCallsRecordedNotice, At: time.Now()},
	}
	c := newReconstructedConversation("worker", ports.ModelInfo{Name: "worker"}, history)
	c.setPendingToolCalls("ref:abc", resolver)
	return c
}

func TestHistory_NilResolver_FallsBackToNotice(t *testing.T) {
	c := buildPendingConversation(nil)

	hist := c.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(hist), hist)
	}
	if hist[1].Text != toolCallsRecordedNotice {
		t.Fatalf("notice text changed: got %q", hist[1].Text)
	}
	c.mu.Lock()
	resolved := c.resolved
	c.mu.Unlock()
	if resolved {
		t.Fatal("resolved must stay false when no resolver is wired")
	}
}

// countingErrorResolver returns an error every call and counts calls, to
// prove the failure-caching contract: a failed attempt sets
// resolveAttempted so History() never re-issues LoadContent after the
// first failure.
type countingErrorResolver struct {
	calls int32
}

func (r *countingErrorResolver) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	atomic.AddInt32(&r.calls, 1)
	return nil, context.DeadlineExceeded
}

func TestHistory_ResolverError_FallsBackAndDoesNotRetry(t *testing.T) {
	resolver := &countingErrorResolver{}
	c := buildPendingConversation(resolver)

	hist1 := c.History()
	if hist1[1].Text != toolCallsRecordedNotice {
		t.Fatalf("first call: notice changed: got %q", hist1[1].Text)
	}
	hist2 := c.History()
	if hist2[1].Text != toolCallsRecordedNotice {
		t.Fatalf("second call: notice changed: got %q", hist2[1].Text)
	}

	if got := atomic.LoadInt32(&resolver.calls); got != 1 {
		t.Fatalf("LoadContent called %d times, want exactly 1 (failure must be cached, not retried)", got)
	}
	c.mu.Lock()
	resolved := c.resolved
	attempted := c.resolveAttempted
	c.mu.Unlock()
	if resolved {
		t.Fatal("resolved must stay false on a failed resolve")
	}
	if !attempted {
		t.Fatal("resolveAttempted must be set after a failed resolve, to suppress retries")
	}
}

type staticResolver struct {
	raw []byte
	err error
}

func (r staticResolver) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	return r.raw, r.err
}

func TestHistory_MalformedJSON_FallsBackNoPanic(t *testing.T) {
	c := buildPendingConversation(staticResolver{raw: []byte("not json")})

	hist := c.History()
	if hist[1].Text != toolCallsRecordedNotice {
		t.Fatalf("notice changed on malformed JSON: got %q", hist[1].Text)
	}
	c.mu.Lock()
	resolved := c.resolved
	c.mu.Unlock()
	if resolved {
		t.Fatal("resolved must stay false on malformed JSON")
	}
}

func TestHistory_SuccessfulResolve_ReplacesNoticeInPlace(t *testing.T) {
	steps := []toolCallStepMirror{
		{ToolCallID: "call-1", Name: "grep", Kind: "start", Input: `{"pattern":"x"}`},
		{ToolCallID: "call-1", Kind: "end", Output: "3 matches"},
	}
	raw, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	c := buildPendingConversation(staticResolver{raw: raw})

	hist := c.History()
	if len(hist) != 2 {
		t.Fatalf("expected exactly 2 messages (prompt + resolved assistant), got %d: %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].Text != "do the work" {
		t.Fatalf("prompt message unexpectedly changed: %+v", hist[0])
	}
	if hist[1].Role != "assistant" {
		t.Fatalf("expected assistant message, got %+v", hist[1])
	}
	if hist[1].Text != "" {
		t.Fatalf("expected placeholder notice text cleared, got %q", hist[1].Text)
	}
	if len(hist[1].ToolCalls) != 1 {
		t.Fatalf("expected 1 resolved tool call, got %+v", hist[1].ToolCalls)
	}
	tc := hist[1].ToolCalls[0]
	if tc.ID != "call-1" || tc.Name != "grep" || tc.Output != "3 matches" {
		t.Fatalf("resolved tool call fields wrong: %+v", tc)
	}

	c.mu.Lock()
	resolved := c.resolved
	c.mu.Unlock()
	if !resolved {
		t.Fatal("resolved must be true after a successful resolve")
	}
}

// countingSuccessResolver returns a fixed valid payload and counts calls,
// to prove a second History() call after a successful resolve does not
// re-issue LoadContent.
type countingSuccessResolver struct {
	raw   []byte
	calls int32
}

func (r *countingSuccessResolver) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	atomic.AddInt32(&r.calls, 1)
	return r.raw, nil
}

func TestHistory_SecondCallAfterSuccess_DoesNotReresolve(t *testing.T) {
	steps := []toolCallStepMirror{
		{ToolCallID: "call-1", Name: "grep", Kind: "start"},
		{ToolCallID: "call-1", Kind: "end", Output: "done"},
	}
	raw, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &countingSuccessResolver{raw: raw}
	c := buildPendingConversation(resolver)

	_ = c.History()
	_ = c.History()

	if got := atomic.LoadInt32(&resolver.calls); got != 1 {
		t.Fatalf("LoadContent called %d times, want exactly 1", got)
	}
}

// TestHistory_ConcurrentCalls_RaceClean pins the concurrency contract with
// -race, and TestResolveToolCallsPending_DoubleCheckSkipsDuplicateApply
// below deterministically exercises the specific double-checked-lock
// branch inside resolveToolCallsPending: a resolution that finishes (or
// gives up) via a DIFFERENT path while this call's own LoadContent was
// still in flight must not re-apply or overwrite that outcome.
func TestHistory_ConcurrentCalls_RaceClean(t *testing.T) {
	steps := []toolCallStepMirror{
		{ToolCallID: "call-1", Name: "grep", Kind: "start"},
		{ToolCallID: "call-1", Kind: "end", Output: "done"},
	}
	raw, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	c := buildPendingConversation(staticResolver{raw: raw})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.History()
		}()
	}
	wg.Wait()

	hist := c.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 messages after concurrent resolves, got %d: %+v", len(hist), hist)
	}
	if len(hist[1].ToolCalls) != 1 {
		t.Fatalf("expected 1 resolved tool call after concurrent resolves, got %+v", hist[1].ToolCalls)
	}
}

// TestApplyResolvedToolCalls_SkipsTrailingNonAssistantMessage exercises
// applyResolvedToolCalls' defensive skip-non-assistant branch: it walks
// history from the end looking for the last ASSISTANT message, so a
// message appended after the placeholder (which registerDispatchedTask
// itself never produces, but the function does not assume) must be
// skipped rather than mistaken for the placeholder's slot.
func TestApplyResolvedToolCalls_SkipsTrailingNonAssistantMessage(t *testing.T) {
	c := newReconstructedConversation("worker", ports.ModelInfo{Name: "worker"}, []ports.Message{
		{Role: "user", Text: "do the work", At: time.Now()},
		{Role: "assistant", Text: toolCallsRecordedNotice, At: time.Now()},
		{Role: "user", Text: "a trailing message after the placeholder", At: time.Now()},
	})

	c.mu.Lock()
	c.applyResolvedToolCalls([]toolCallSummary{
		{ToolCallID: "call-1", Name: "grep", Output: "done"},
	})
	c.mu.Unlock()

	hist := c.History()
	if len(hist) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(hist), hist)
	}
	if hist[2].Role != "user" || hist[2].Text != "a trailing message after the placeholder" {
		t.Fatalf("trailing user message unexpectedly changed: %+v", hist[2])
	}
	if hist[1].Text != "" {
		t.Fatalf("expected placeholder notice cleared on the assistant message, got %q", hist[1].Text)
	}
	if len(hist[1].ToolCalls) != 1 {
		t.Fatalf("expected resolved tool call applied to the assistant message, got %+v", hist[1].ToolCalls)
	}
}

// blockingResolver blocks LoadContent until release is closed, and
// signals started once entered, so a test can force resolveToolCallsPending's
// unlocked LoadContent window to overlap with another goroutine mutating
// resolved/resolveAttempted directly.
type blockingResolver struct {
	started chan struct{}
	release chan struct{}
	raw     []byte
}

func (r *blockingResolver) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	close(r.started)
	<-r.release
	return r.raw, nil
}

// TestResolveToolCallsPending_DoubleCheckSkipsDuplicateApply deterministically
// exercises resolveToolCallsPending's post-LoadContent double-check: while
// this call's own LoadContent is still blocked in flight, another path
// completes the resolution (sets resolved=true) directly. When the blocked
// call's LoadContent finally returns, its own (different) payload must NOT
// be re-applied over what already landed - the re-check under the
// re-acquired lock must see resolved=true and return without touching
// history again.
func TestResolveToolCallsPending_DoubleCheckSkipsDuplicateApply(t *testing.T) {
	winningSteps := []toolCallStepMirror{
		{ToolCallID: "winner", Name: "grep", Kind: "start"},
		{ToolCallID: "winner", Kind: "end", Output: "winner-output"},
	}
	losingRaw, err := json.Marshal([]toolCallStepMirror{
		{ToolCallID: "loser", Name: "bash", Kind: "start"},
		{ToolCallID: "loser", Kind: "end", Output: "loser-output"},
	})
	if err != nil {
		t.Fatal(err)
	}

	resolver := &blockingResolver{
		started: make(chan struct{}),
		release: make(chan struct{}),
		raw:     losingRaw,
	}
	c := buildPendingConversation(resolver)

	done := make(chan struct{})
	go func() {
		c.resolveToolCallsPending()
		close(done)
	}()

	<-resolver.started // the blocked call has copied fields and is in LoadContent

	// Simulate a different path completing the resolution while the above
	// call is still blocked, applying the WINNING payload directly.
	c.mu.Lock()
	c.applyResolvedToolCalls(mergeToolCallSteps(winningSteps))
	c.resolved = true
	c.resolveAttempted = true
	c.mu.Unlock()

	close(resolver.release) // let the blocked call's LoadContent return
	<-done

	hist := c.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(hist), hist)
	}
	if len(hist[1].ToolCalls) != 1 || hist[1].ToolCalls[0].ID != "winner" {
		t.Fatalf("expected the winning resolution to survive untouched, got %+v", hist[1].ToolCalls)
	}
}

// ---------------------------------------------------------------------
// Part C: wire-contract drift guard
// ---------------------------------------------------------------------

// TestToolCallStepMirrorMatchesRealTypeFieldForField proves toolCallStepMirror
// stays in sync with internal/subagents.ToolCallStep on the wire: a real
// value marshaled through the production type must unmarshal into the
// mirror with every field intact, AND the exact JSON key set produced must
// be {ToolCallID, Name, Kind, Input, Output, At} - no more, no fewer. The
// second check is what catches a FUTURE field added to the real type
// without a matching mirror update; a plain round-trip on known values
// alone would not.
func TestToolCallStepMirrorMatchesRealTypeFieldForField(t *testing.T) {
	real := subagents.ToolCallStep{
		ToolCallID: "call-99",
		Name:       "bash",
		Kind:       "end",
		Input:      `{"cmd":"go test"}`,
		Output:     "ok",
		At:         time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	raw, err := json.Marshal(real)
	if err != nil {
		t.Fatalf("marshal real type: %v", err)
	}

	var mirrored toolCallStepMirror
	if err := json.Unmarshal(raw, &mirrored); err != nil {
		t.Fatalf("unmarshal into mirror: %v", err)
	}
	if mirrored.ToolCallID != real.ToolCallID ||
		mirrored.Name != real.Name ||
		mirrored.Kind != real.Kind ||
		mirrored.Input != real.Input ||
		mirrored.Output != real.Output ||
		!mirrored.At.Equal(real.At) {
		t.Fatalf("mirror drifted from real type:\n  real:     %+v\n  mirrored: %+v", real, mirrored)
	}

	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal into generic map: %v", err)
	}
	wantKeys := map[string]bool{
		"ToolCallID": true,
		"Name":       true,
		"Kind":       true,
		"Input":      true,
		"Output":     true,
		"At":         true,
	}
	if len(generic) != len(wantKeys) {
		t.Fatalf("wire key count = %d, want %d; keys: %v", len(generic), len(wantKeys), keysOf(generic))
	}
	for k := range generic {
		if !wantKeys[k] {
			t.Fatalf("subagents.ToolCallStep gained an unmirrored field %q - update toolCallStepMirror to match", k)
		}
	}
	for k := range wantKeys {
		if _, ok := generic[k]; !ok {
			t.Fatalf("expected wire key %q missing from marshaled subagents.ToolCallStep", k)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
