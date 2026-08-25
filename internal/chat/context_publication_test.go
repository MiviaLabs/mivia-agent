package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
)

func TestSummaryUnavailableReasonWhenSummarized(t *testing.T) {
	if reason := summaryUnavailableReason(contextTurnConfig{}, true, ""); reason != "" {
		t.Fatalf("summarized compaction must carry no reason, got %q", reason)
	}
}

func TestSummaryUnavailableReasonUsesSetupCause(t *testing.T) {
	cfg := contextTurnConfig{
		manager: &contextmgr.ContextManager{SummaryUnavailableReason: "the provider endpoint (base_url) did not resolve"},
	}
	reason := summaryUnavailableReason(cfg, false, "")
	if reason != "the provider endpoint (base_url) did not resolve" {
		t.Fatalf("expected the setup-time cause, got %q", reason)
	}
}

func TestSummaryUnavailableReasonFallsBackWhenManagerHasNoCause(t *testing.T) {
	cfg := contextTurnConfig{manager: &contextmgr.ContextManager{}}
	reason := summaryUnavailableReason(cfg, false, "")
	if reason != "no summarizer is configured for this session" {
		t.Fatalf("expected the generic not-configured fallback, got %q", reason)
	}
}

func TestSummaryUnavailableReasonWhenSummarizerWiredButCallFailed(t *testing.T) {
	cfg := contextTurnConfig{
		summarizer: &contextmgr.Summarizer{},
		manager:    &contextmgr.ContextManager{SummaryUnavailableReason: "should be ignored once a summarizer is wired"},
	}
	reason := summaryUnavailableReason(cfg, false, "")
	if reason != "the summary call failed or produced nothing usable for this compaction" {
		t.Fatalf("expected the runtime-failure reason, got %q", reason)
	}
}

func TestSummaryUnavailableReasonPrefersTheClassifiedFailure(t *testing.T) {
	cfg := contextTurnConfig{
		summarizer: &contextmgr.Summarizer{},
		manager:    &contextmgr.ContextManager{SummaryUnavailableReason: "should be ignored"},
	}
	reason := summaryUnavailableReason(cfg, false, contextmgr.SummaryReasonTimeout)
	if reason != contextmgr.SummaryReasonTimeout {
		t.Fatalf("expected classified reason %q, got %q", contextmgr.SummaryReasonTimeout, reason)
	}
}

func TestSummaryUnavailableReasonIgnoresAFailureReasonWhenSummarized(t *testing.T) {
	cfg := contextTurnConfig{
		summarizer: &contextmgr.Summarizer{},
	}
	if reason := summaryUnavailableReason(cfg, true, contextmgr.SummaryReasonTimeout); reason != "" {
		t.Fatalf("summarized compaction must carry no reason even if failureReason passed, got %q", reason)
	}
}

func TestSummaryUnavailableReasonIgnoresAFailureReasonWithNoSummarizerWired(t *testing.T) {
	cfg := contextTurnConfig{
		manager: &contextmgr.ContextManager{SummaryUnavailableReason: "no credential scope configured"},
	}
	reason := summaryUnavailableReason(cfg, false, contextmgr.SummaryReasonTimeout)
	if reason != "no credential scope configured" {
		t.Fatalf("expected setup-time cause with nil summarizer, got %q", reason)
	}
}
