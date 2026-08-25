package contextmgr

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// Summary failure sentinels. Each wraps contextstate.ErrInvalidDTO so existing
// errors.Is(err, contextstate.ErrInvalidDTO) assertions continue to hold while
// allowing exact classification of the failure mode.
var (
	ErrSummaryReplyMalformed   = fmt.Errorf("%w: summary reply is not usable", contextstate.ErrInvalidDTO)
	ErrSummaryEchoMismatch     = fmt.Errorf("%w: summary reply does not echo the request", contextstate.ErrInvalidDTO)
	ErrSummaryOutputTooLarge   = fmt.Errorf("%w: summary output exceeds its bound", contextstate.ErrInvalidDTO)
	ErrSummaryRedactionRefused = fmt.Errorf("%w: summary rejected by redaction policy", contextstate.ErrInvalidDTO)
)

// Closed, content-free vocabulary of summary failure reasons.
// These constants are rendered verbatim in compaction event details and durable ledger records.
const (
	SummaryReasonTimeout          = "the summary call timed out"
	SummaryReasonTransport        = "the summary call could not reach the provider"
	SummaryReasonCancelled        = "the turn was cancelled before the summary returned"
	SummaryReasonReplyMalformed   = "the summary reply was not valid summary JSON"
	SummaryReasonEchoMismatch     = "the summary reply did not echo the request identity"
	SummaryReasonOutputTooLarge   = "the summary reply exceeded its output bound"
	SummaryReasonRedactionRefused = "the summary was refused by the redaction policy"
	SummaryReasonPolicyRefused    = "the summary policy is not enabled for this session"
	SummaryReasonBindingChanged   = "the summary binding or policy changed mid-turn"
	SummaryReasonRequestInvalid   = "the host could not build a valid summary request"
	SummaryReasonHostState        = "the host turn state was unreadable"
	SummaryReasonMetadataTooLarge = "the summary metadata exceeded its persistence bound"
	SummaryReasonOverBudget       = "the summary did not fit the remaining context budget"
	SummaryReasonUnclassified     = "the summary call failed for an unclassified reason"
)

// ClassifySummaryFailure maps a summary error to one of the closed SummaryReason* constants.
// It never includes raw error text or model output in the returned reason.
func ClassifySummaryFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return SummaryReasonCancelled
	}
	// Reply-shape sentinels MUST be tested before provider.IsTransient.
	// decodeSummaryReply embeds %v of JSON parse errors, and provider.IsTransient
	// scans error text for words like "overloaded" or "http 503". Checking sentinels
	// first prevents a malformed reply containing those words from misclassifying.
	if errors.Is(err, ErrSummaryRedactionRefused) {
		return SummaryReasonRedactionRefused
	}
	if errors.Is(err, ErrSummaryEchoMismatch) {
		return SummaryReasonEchoMismatch
	}
	if errors.Is(err, ErrSummaryOutputTooLarge) {
		return SummaryReasonOutputTooLarge
	}
	if errors.Is(err, ErrSummaryReplyMalformed) {
		return SummaryReasonReplyMalformed
	}
	if errors.Is(err, contextstate.ErrStaleBinding) {
		return SummaryReasonBindingChanged
	}
	if errors.Is(err, contextstate.ErrSummaryUnavailable) {
		return SummaryReasonPolicyRefused
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return SummaryReasonTimeout
	}
	if provider.IsTransient(err) {
		return SummaryReasonTransport
	}
	if errors.Is(err, contextstate.ErrInvalidDTO) {
		return SummaryReasonRequestInvalid
	}
	return SummaryReasonUnclassified
}

// RetryableSummaryFailure reports whether a summary error represents a transient failure
// that should be retried. Permanent failures (reply shape errors, redaction refusal,
// stale bindings, policy refusal, cancellation) return false immediately.
func RetryableSummaryFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Reply-shape errors and policy refusals are non-retryable.
	// Retrying with temperature 0.0 and identical inputs produces identical results.
	if errors.Is(err, ErrSummaryRedactionRefused) ||
		errors.Is(err, ErrSummaryEchoMismatch) ||
		errors.Is(err, ErrSummaryOutputTooLarge) ||
		errors.Is(err, ErrSummaryReplyMalformed) ||
		errors.Is(err, contextstate.ErrStaleBinding) ||
		errors.Is(err, contextstate.ErrSummaryUnavailable) {
		return false
	}
	if provider.IsTransient(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}
