package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type recordingSummaryProvider struct {
	responses []summaryResponse
	requests  []SummaryRequest
	deadlines []time.Duration
}

type summaryResponse struct {
	summary Summary
	err     error
}

func (p *recordingSummaryProvider) Summarize(ctx context.Context, request SummaryRequest) (Summary, error) {
	p.requests = append(p.requests, request)
	if deadline, ok := ctx.Deadline(); ok {
		p.deadlines = append(p.deadlines, time.Until(deadline))
	} else {
		p.deadlines = append(p.deadlines, 0)
	}
	idx := len(p.requests) - 1
	if idx < len(p.responses) {
		return p.responses[idx].summary, p.responses[idx].err
	}
	if len(p.responses) > 0 {
		last := p.responses[len(p.responses)-1]
		return last.summary, last.err
	}
	return Summary{}, errors.New("no responses configured")
}

type classificationTestCase struct {
	name       string
	err        error
	wantReason string
	retryable  bool
}

func buildClassificationCases(t *testing.T, secret string) []classificationTestCase {
	t.Helper()
	request := summaryTestRequest(t)
	redactionReq := request
	redactionReq.RedactionPolicy = contextstate.RedactionPolicy{
		Configured: true,
		Classifier: func(data []byte) error {
			if strings.Contains(string(data), secret) {
				return errors.New("contains secret")
			}
			return nil
		},
	}
	_, redactionErr := ValidateSummary(Summary{
		Version: 1, Objective: "contains " + secret, State: "normal state", SourceRange: request.SourceRange,
	}, redactionReq)
	_, malformedErr := decodeSummaryReply("not json with " + secret)
	_, echoMismatchErr := ValidateSummary(Summary{Version: 9, Objective: "objective", State: "state", SourceRange: request.SourceRange}, request)
	_, outputTooLargeErr := ValidateSummary(Summary{
		Version: 1, Objective: strings.Repeat("a", 2000), State: "state", SourceRange: request.SourceRange,
	}, request)

	bindingReq := request
	bindingReq.Model = "mismatched-model"
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	summarizer, err := NewSummarizer(&recordingSummaryProvider{}, binding, contextstate.PolicySnapshot{
		SummaryEnabled: true, NetworkEnabled: true, CredentialScope: "scope", EndpointAllowlist: []string{"https://summary.invalid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, staleBindingErr := summarizer.Summarize(context.Background(), bindingReq)
	disabledSummarizer, err := NewSummarizer(&recordingSummaryProvider{}, binding, contextstate.PolicySnapshot{
		SummaryEnabled: false, NetworkEnabled: true, CredentialScope: "scope", EndpointAllowlist: []string{"https://summary.invalid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, summaryUnavailableErr := disabledSummarizer.Summarize(context.Background(), request)
	transientErr := &provider.TransientError{Err: errors.New("connection reset with " + secret)}

	return []classificationTestCase{
		{"nil error", nil, "", false},
		{"context canceled", context.Canceled, SummaryReasonCancelled, false},
		{"redaction refused", redactionErr, SummaryReasonRedactionRefused, false},
		{"reply malformed", malformedErr, SummaryReasonReplyMalformed, false},
		{"echo mismatch", echoMismatchErr, SummaryReasonEchoMismatch, false},
		{"output too large", outputTooLargeErr, SummaryReasonOutputTooLarge, false},
		{"stale binding", staleBindingErr, SummaryReasonBindingChanged, false},
		{"summary unavailable", summaryUnavailableErr, SummaryReasonPolicyRefused, false},
		{"deadline exceeded", context.DeadlineExceeded, SummaryReasonTimeout, true},
		{"transient transport error", transientErr, SummaryReasonTransport, true},
		{"raw invalid dto", contextstate.ErrInvalidDTO, SummaryReasonRequestInvalid, false},
		{"unclassified error", errors.New("random unclassified failure"), SummaryReasonUnclassified, false},
	}
}

func TestClassifySummaryFailureVocabularyIsClosedAndContentFree(t *testing.T) {
	const secret = "SECRET-TOKEN-12345"
	cases := buildClassificationCases(t, secret)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotReason := ClassifySummaryFailure(tc.err)
			if gotReason != tc.wantReason {
				t.Fatalf("ClassifySummaryFailure(%v) = %q, want %q", tc.err, gotReason, tc.wantReason)
			}
			if strings.Contains(gotReason, secret) {
				t.Fatalf("ClassifySummaryFailure leaked secret token: %q", gotReason)
			}
			if gotRetryable := RetryableSummaryFailure(tc.err); gotRetryable != tc.retryable {
				t.Fatalf("RetryableSummaryFailure(%v) = %v, want %v", tc.err, gotRetryable, tc.retryable)
			}
		})
	}
}

// TestClassifySummaryFailureNeverRetriesAMalformedReplyThatMentionsOverload pins
// the ordering hazard: a malformed JSON body mentioning words that provider.IsTransient
// checks for (like "overloaded") must still be classified as non-retryable SummaryReasonReplyMalformed.
func TestClassifySummaryFailureNeverRetriesAMalformedReplyThatMentionsOverload(t *testing.T) {
	_, err := decodeSummaryReply(`{"objective": "the server was overloaded"`)
	if err == nil {
		t.Fatal("expected decode error on malformed JSON")
	}
	reason := ClassifySummaryFailure(err)
	if reason != SummaryReasonReplyMalformed {
		t.Fatalf("ClassifySummaryFailure = %q, want %q", reason, SummaryReasonReplyMalformed)
	}
	if RetryableSummaryFailure(err) {
		t.Fatal("RetryableSummaryFailure returned true for malformed JSON mentioning overloaded")
	}
}

// TestSummaryReasonsFitTheCompactionEvent verifies all 14 constants satisfy the length
// bound (<= 256 bytes), contain no control characters, and pass events.NewCompactionEvent validation.
func TestSummaryReasonsFitTheCompactionEvent(t *testing.T) {
	reasons := []string{
		SummaryReasonTimeout,
		SummaryReasonTransport,
		SummaryReasonCancelled,
		SummaryReasonReplyMalformed,
		SummaryReasonEchoMismatch,
		SummaryReasonOutputTooLarge,
		SummaryReasonRedactionRefused,
		SummaryReasonPolicyRefused,
		SummaryReasonBindingChanged,
		SummaryReasonRequestInvalid,
		SummaryReasonHostState,
		SummaryReasonMetadataTooLarge,
		SummaryReasonOverBudget,
		SummaryReasonUnclassified,
	}

	source := contextstate.SourceID{SessionID: "session", Sequence: 1}
	sourceRange, err := contextstate.NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}

	for _, reason := range reasons {
		if len(reason) > 256 {
			t.Fatalf("reason %q exceeds 256 bytes (%d)", reason, len(reason))
		}
		for _, r := range reason {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("reason %q contains control char %q", reason, r)
			}
		}
		_, err := events.NewCompactionEvent(events.CompactionEventParams{
			Trigger:        "threshold",
			BeforeTokens:   1000,
			AfterTokens:    500,
			ElidedMessages: 2,
			ElidedBytes:    100,
			SourceRange:    sourceRange,
			SummaryVersion: 1,
			Summarized:     false,
			Reason:         reason,
		})
		if err != nil {
			t.Fatalf("NewCompactionEvent rejected reason %q: %v", reason, err)
		}
	}
}

func testSummarizer(t *testing.T, provider SummaryProvider) Summarizer {
	t.Helper()
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSummarizer(provider, binding, contextstate.PolicySnapshot{
		SummaryEnabled: true, NetworkEnabled: true, CredentialScope: "scope",
		EndpointAllowlist: []string{"https://summary.invalid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSummarizerRetriesOnceOnATransientFailure(t *testing.T) {
	request := summaryTestRequest(t)
	validSummary := Summary{Version: 1, Objective: "obj", State: "st", SourceRange: request.SourceRange}

	provider := &recordingSummaryProvider{
		responses: []summaryResponse{
			{err: &provider.TransientError{Err: errors.New("connection reset")}},
			{summary: validSummary},
		},
	}
	summarizer := testSummarizer(t, provider)

	summary, err := summarizer.Summarize(context.Background(), request)
	if err != nil {
		t.Fatalf("Summarize failed after transient retry: %v", err)
	}
	if summary.Value().Objective != "obj" {
		t.Fatalf("summary objective = %q, want %q", summary.Value().Objective, "obj")
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider call count = %d, want 2", len(provider.requests))
	}
}

func TestSummarizerDoesNotRetryAMalformedReply(t *testing.T) {
	request := summaryTestRequest(t)
	provider := &recordingSummaryProvider{
		responses: []summaryResponse{
			{err: fmt.Errorf("%w: bad json syntax", ErrSummaryReplyMalformed)},
			{err: fmt.Errorf("%w: bad json syntax", ErrSummaryReplyMalformed)},
		},
	}
	summarizer := testSummarizer(t, provider)

	_, err := summarizer.Summarize(context.Background(), request)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider call count = %d, want 1", len(provider.requests))
	}
	if reason := ClassifySummaryFailure(err); reason != SummaryReasonReplyMalformed {
		t.Fatalf("ClassifySummaryFailure = %q, want %q", reason, SummaryReasonReplyMalformed)
	}
}

func TestSummarizerDoesNotRetryARedactionRefusal(t *testing.T) {
	request := summaryTestRequest(t)
	request.RedactionPolicy = contextstate.RedactionPolicy{
		Configured: true,
		Classifier: func(data []byte) error {
			if strings.Contains(string(data), "refused") {
				return errors.New("refused")
			}
			return nil
		},
	}
	refusedSummary := Summary{Version: 1, Objective: "refused content", State: "state", SourceRange: request.SourceRange}
	provider := &recordingSummaryProvider{
		responses: []summaryResponse{
			{summary: refusedSummary},
		},
	}
	summarizer := testSummarizer(t, provider)

	_, err := summarizer.Summarize(context.Background(), request)
	if err == nil {
		t.Fatal("expected redaction refusal error")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider call count = %d, want 1", len(provider.requests))
	}
	if reason := ClassifySummaryFailure(err); reason != SummaryReasonRedactionRefused {
		t.Fatalf("ClassifySummaryFailure = %q, want %q", reason, SummaryReasonRedactionRefused)
	}
}

func TestSummarizerDoesNotRetryUnderAnExpiredCallerContext(t *testing.T) {
	request := summaryTestRequest(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	provider := &recordingSummaryProvider{
		responses: []summaryResponse{
			{err: context.DeadlineExceeded},
		},
	}
	summarizer := testSummarizer(t, provider)

	_, err := summarizer.Summarize(ctx, request)
	if err == nil {
		t.Fatal("expected deadline exceeded error")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider call count = %d, want 1 under expired caller ctx", len(provider.requests))
	}
}

func TestSummarizerRetryIsBoundedToTwoAttempts(t *testing.T) {
	request := summaryTestRequest(t)
	provider := &recordingSummaryProvider{
		responses: []summaryResponse{
			{err: &provider.TransientError{Err: errors.New("fault 1")}},
			{err: &provider.TransientError{Err: errors.New("fault 2")}},
			{err: &provider.TransientError{Err: errors.New("fault 3")}},
		},
	}
	summarizer := testSummarizer(t, provider)

	_, err := summarizer.Summarize(context.Background(), request)
	if err == nil {
		t.Fatal("expected error from persistent transient fault")
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider call count = %d, want exactly 2 (summaryMaxAttempts)", len(provider.requests))
	}
}

func TestSummarizerRetryGetsAFreshShorterWindow(t *testing.T) {
	request := summaryTestRequest(t)
	provider := &recordingSummaryProvider{
		responses: []summaryResponse{
			{err: &provider.TransientError{Err: errors.New("fault 1")}},
			{err: &provider.TransientError{Err: errors.New("fault 2")}},
		},
	}
	summarizer := testSummarizer(t, provider)

	_, _ = summarizer.Summarize(context.Background(), request)
	if len(provider.deadlines) != 2 {
		t.Fatalf("recorded deadlines count = %d, want 2", len(provider.deadlines))
	}
	// Attempt 1 should have ~20s timeout, attempt 2 should have ~10s timeout.
	if provider.deadlines[0] < 15*time.Second || provider.deadlines[0] > 21*time.Second {
		t.Fatalf("attempt 1 deadline = %v, expected ~20s", provider.deadlines[0])
	}
	if provider.deadlines[1] < 5*time.Second || provider.deadlines[1] > 11*time.Second {
		t.Fatalf("attempt 2 deadline = %v, expected ~10s", provider.deadlines[1])
	}
}
