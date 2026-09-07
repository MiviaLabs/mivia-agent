package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkplan "github.com/MiviaLabs/mivia-ai-sdk/context/plan"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// echoingSummaryProvider counts every provider call and echoes the
// request's source excerpts into the objective. Two different dropped
// sets therefore produce two different summaries, so a memo that
// returns a stale value for every input fails the positive control.
// errs supplies a per-call error sequence; a call past the sequence
// succeeds.
//
// Call counting is at the PROVIDER, one layer below the adapter.
// contextmgr.Summarizer.Summarize already retries a retryable failure
// once inline (summaryMaxAttempts), so one adapter attempt that fails
// retryably costs TWO provider calls.
type echoingSummaryProvider struct {
	calls    int
	errs     []error
	requests []contextmgr.SummaryRequest
}

func (p *echoingSummaryProvider) Summarize(_ context.Context, request contextmgr.SummaryRequest) (contextmgr.Summary, error) {
	index := p.calls
	p.calls++
	p.requests = append(p.requests, request)
	if index < len(p.errs) && p.errs[index] != nil {
		return contextmgr.Summary{}, p.errs[index]
	}
	texts := make([]string, 0, len(request.SourceExcerpts))
	for _, excerpt := range request.SourceExcerpts {
		texts = append(texts, excerpt.Text)
	}
	return contextmgr.Summary{
		Version:     request.Input.Version,
		Objective:   "objective of " + strings.Join(texts, "|"),
		State:       request.Input.State,
		SourceRange: request.SourceRange,
	}, nil
}

// alwaysFailingSummaryProvider counts calls and always returns err.
type alwaysFailingSummaryProvider struct {
	calls int
	err   error
}

func (p *alwaysFailingSummaryProvider) Summarize(context.Context, contextmgr.SummaryRequest) (contextmgr.Summary, error) {
	p.calls++
	return contextmgr.Summary{}, p.err
}

func sdkMessagesOf(contents ...string) []sdkshape.Message {
	out := make([]sdkshape.Message, 0, len(contents))
	for index, content := range contents {
		role := sdkshape.RoleUser
		if index%2 == 1 {
			role = sdkshape.RoleAssistant
		}
		out = append(out, sdkshape.Message{Role: role, Content: content})
	}
	return out
}

// TestSDKSummarizerAdapterMemoizesRepeatedCalls pins the memo: two
// Summarize calls over the same dropped set issue exactly one
// underlying summarizer call and return the byte-identical result.
// The memo also re-stashes the pending outcome, so a confirming
// observer call still grounds the compaction.
func TestSDKSummarizerAdapterMemoizesRepeatedCalls(t *testing.T) {
	summaryProvider := &echoingSummaryProvider{}
	a, l := newAdapterFixture(t, summaryProvider)
	msgs := sdkMessagesOf("dropped one", "dropped two")

	first, err := a.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("first Summarize() = %v, want nil", err)
	}
	second, err := a.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("second Summarize() = %v, want nil", err)
	}
	if summaryProvider.calls != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 for one memoized compaction", summaryProvider.calls)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("memoized result differs:\n%+v\nvs\n%+v", first, second)
	}
	if l.sdkPendingCompaction == nil {
		t.Fatal("memo replay did not re-stash the pending outcome")
	}
	if !l.sdkPendingCompaction.summarized {
		t.Fatal("re-stashed outcome is not marked summarized")
	}
}

// TestSDKSummarizerAdapterDifferentDroppedSetsResummarize is the
// positive control for the memo: a different dropped set is a
// different compaction, so it summarizes again and returns a
// different result.
func TestSDKSummarizerAdapterDifferentDroppedSetsResummarize(t *testing.T) {
	summaryProvider := &echoingSummaryProvider{}
	a, _ := newAdapterFixture(t, summaryProvider)

	first, err := a.Summarize(context.Background(), sdkMessagesOf("dropped one", "dropped two"))
	if err != nil {
		t.Fatalf("first Summarize() = %v, want nil", err)
	}
	second, err := a.Summarize(context.Background(), sdkMessagesOf("a different dropped message", "and another"))
	if err != nil {
		t.Fatalf("second Summarize() = %v, want nil", err)
	}
	if summaryProvider.calls != 2 {
		t.Fatalf("provider calls = %d, want exactly 2 for two distinct dropped sets", summaryProvider.calls)
	}
	if first.Objective == second.Objective {
		t.Fatalf("both dropped sets produced the same objective %q; the memo returned a stale value", first.Objective)
	}
}

// TestSDKSummarizerAdapterRetriesOnceThenSucceeds proves the one
// in-call retry: the first adapter attempt fails retryably (two
// provider calls, the inner Summarizer's own inline retry included),
// the retry succeeds on the third provider call, and Summarize
// returns the success.
func TestSDKSummarizerAdapterRetriesOnceThenSucceeds(t *testing.T) {
	transient := &provider.TransientError{Err: errors.New("connection reset")}
	summaryProvider := &echoingSummaryProvider{errs: []error{transient, transient}}
	a, l := newAdapterFixture(t, summaryProvider)

	got, err := a.Summarize(context.Background(), sdkMessagesOf("dropped one", "dropped two"))
	if err != nil {
		t.Fatalf("Summarize() = %v, want nil after one retry", err)
	}
	if got.Objective == "" {
		t.Fatal("Summarize() returned an empty objective after a successful retry")
	}
	if summaryProvider.calls != 3 {
		t.Fatalf("provider calls = %d, want exactly 3 (attempt 1 = 2 inline calls, retry = 1)", summaryProvider.calls)
	}
	if l.summaryFailureReason != "" {
		t.Fatalf("summaryFailureReason = %q, want empty after a successful retry", l.summaryFailureReason)
	}
	if l.sdkPendingCompaction == nil || !l.sdkPendingCompaction.summarized {
		t.Fatalf("pending = %+v, want a summarized outcome after a successful retry", l.sdkPendingCompaction)
	}
}

// TestSDKSummarizerAdapterRetryableFailureTwiceFailsClosed proves the
// retry is bounded at exactly one: two failed adapter attempts cost
// four provider calls and never a third attempt. The error returns
// unwrapped, so the SDK fails the compaction closed.
func TestSDKSummarizerAdapterRetryableFailureTwiceFailsClosed(t *testing.T) {
	transient := &provider.TransientError{Err: errors.New("connection reset")}
	summaryProvider := &alwaysFailingSummaryProvider{err: transient}
	a, l := newAdapterFixture(t, summaryProvider)

	_, err := a.Summarize(context.Background(), sdkMessagesOf("dropped one", "dropped two"))
	if err == nil {
		t.Fatal("Summarize() = nil, want the retryable failure")
	}
	if errors.Is(err, sdkplan.ErrSummarySkipped) {
		t.Fatalf("err = %v, want NOT errors.Is ErrSummarySkipped: a retryable failure must fail closed", err)
	}
	if summaryProvider.calls != 4 {
		t.Fatalf("provider calls = %d, want exactly 4 (2 adapter attempts x 2 inline calls), never a third attempt", summaryProvider.calls)
	}
	if l.summaryFailureReason != contextmgr.SummaryReasonTransport {
		t.Fatalf("summaryFailureReason = %q, want %q", l.summaryFailureReason, contextmgr.SummaryReasonTransport)
	}
	if l.sdkPendingCompaction != nil {
		t.Fatalf("pending = %+v, want nil: a retryable failure records no outcome to confirm", l.sdkPendingCompaction)
	}
}

// TestSDKSummarizerAdapterNonRetryableFailureIssuesOneCall is the
// negative control for the retry: a non-retryable failure is never
// retried, by the inner Summarizer or by the adapter, and still
// skips.
func TestSDKSummarizerAdapterNonRetryableFailureIssuesOneCall(t *testing.T) {
	malformed := fmt.Errorf("%w: bad json", contextmgr.ErrSummaryReplyMalformed)
	summaryProvider := &alwaysFailingSummaryProvider{err: malformed}
	a, _ := newAdapterFixture(t, summaryProvider)

	_, err := a.Summarize(context.Background(), sdkMessagesOf("dropped one", "dropped two"))
	if !errors.Is(err, sdkplan.ErrSummarySkipped) {
		t.Fatalf("err = %v, want errors.Is ErrSummarySkipped", err)
	}
	if summaryProvider.calls != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 for a non-retryable failure", summaryProvider.calls)
	}
}

// TestSDKSummarizerAdapterEvidenceProvenance pins two separate facts
// so a later change cannot silently swap them: Evidence comes from
// the TurnState snapshot, and SourceExcerpts come from the dropped
// messages.
func TestSDKSummarizerAdapterEvidenceProvenance(t *testing.T) {
	summaryProvider := &echoingSummaryProvider{}
	a, l := newAdapterFixture(t, summaryProvider)
	const evidenceItem = "host recorded omitted tool output"
	if err := l.TurnState.AddEvidence(evidenceItem); err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}
	const droppedText = "the dropped conversation text"
	if _, err := a.Summarize(context.Background(), sdkMessagesOf(droppedText, "second dropped message")); err != nil {
		t.Fatalf("Summarize() = %v, want nil", err)
	}
	if len(summaryProvider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(summaryProvider.requests))
	}
	request := summaryProvider.requests[0]

	// Fact 1: Evidence is the TurnState snapshot's evidence.
	snapshot, err := l.TurnState.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !reflect.DeepEqual(request.Input.Evidence, snapshot.Evidence) {
		t.Fatalf("Input.Evidence = %v, want the TurnState snapshot evidence %v", request.Input.Evidence, snapshot.Evidence)
	}
	if !strings.Contains(strings.Join(request.Input.Evidence, "|"), evidenceItem) {
		t.Fatalf("Input.Evidence = %v, want it to carry %q", request.Input.Evidence, evidenceItem)
	}
	if strings.Contains(strings.Join(request.Input.Evidence, "|"), droppedText) {
		t.Fatalf("Input.Evidence = %v, want it free of dropped-message text", request.Input.Evidence)
	}

	// Fact 2: SourceExcerpts are the dropped messages.
	excerpts := make([]string, 0, len(request.SourceExcerpts))
	for _, excerpt := range request.SourceExcerpts {
		excerpts = append(excerpts, excerpt.Text)
	}
	joined := strings.Join(excerpts, "|")
	if !strings.Contains(joined, droppedText) {
		t.Fatalf("SourceExcerpts = %v, want them to carry the dropped message text %q", excerpts, droppedText)
	}
	if strings.Contains(joined, evidenceItem) {
		t.Fatalf("SourceExcerpts = %v, want them free of the TurnState evidence", excerpts)
	}
}

// TestSDKSummarizerMemoDoesNotLeakAcrossTurns proves the memo is
// turn-scoped state: resetTurnCompaction clears it, exactly as it
// clears every sibling compaction field.
func TestSDKSummarizerMemoDoesNotLeakAcrossTurns(t *testing.T) {
	summaryProvider := &echoingSummaryProvider{}
	a, l := newAdapterFixture(t, summaryProvider)
	msgs := sdkMessagesOf("dropped one", "dropped two")

	if _, err := a.Summarize(context.Background(), msgs); err != nil {
		t.Fatalf("first Summarize() = %v, want nil", err)
	}
	l.resetTurnCompaction()
	if _, err := a.Summarize(context.Background(), msgs); err != nil {
		t.Fatalf("second Summarize() = %v, want nil", err)
	}
	if summaryProvider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2: the memo must not survive resetTurnCompaction", summaryProvider.calls)
	}
}
