package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func compactSummaryRange(t *testing.T) contextstate.SourceRange {
	t.Helper()
	start := contextstate.SourceID{SessionID: "sess-compact", Sequence: 4}
	end := contextstate.SourceID{SessionID: "sess-compact", Sequence: 9}
	sourceRange, err := contextstate.NewSourceRange(start, end)
	if err != nil {
		t.Fatal(err)
	}
	return sourceRange
}

// TestBuildCompactSummaryRequestFields pins the manual-compact summary
// request: the objective is the latest user message, the evidence is the
// content-free omitted-message diff, and every transport field comes from the
// captured summarizer policy.
func TestBuildCompactSummaryRequestFields(t *testing.T) {
	summarizer := plainSummarySummarizer(t, &chatSummaryProvider{})
	redaction := contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}}
	pre := []provider.Message{
		{Role: provider.RoleUser, Content: "old question"},
		{Role: provider.RoleAssistant, Content: "old answer"},
		{Role: provider.RoleUser, Content: "latest objective"},
	}
	retained := []provider.Message{pre[2]}
	request, err := buildCompactSummaryRequest(summarizer, redaction, 777, pre, retained, compactSummaryRange(t))
	if err != nil {
		t.Fatal(err)
	}
	if request.Input.Objective != "latest objective" {
		t.Fatalf("objective = %q, want the latest user message", request.Input.Objective)
	}
	if len(request.Input.Evidence) != 2 {
		t.Fatalf("evidence = %v, want the two omitted messages", request.Input.Evidence)
	}
	if request.Provider != "fake" || request.Model != "model" {
		t.Fatalf("transport binding = %s/%s, want fake/model", request.Provider, request.Model)
	}
	if len(request.EndpointAllowlist) != 1 || request.EndpointAllowlist[0] != "https://summary.invalid" {
		t.Fatalf("endpoint allowlist = %v", request.EndpointAllowlist)
	}
	if request.Input.PolicyDigest != summarizer.Policy.PolicyDigest {
		t.Fatal("request digest differs from the captured policy digest")
	}
	if request.Budget != 777 {
		t.Fatalf("budget = %d, want the passed budget", request.Budget)
	}
	if request.OutputLimit != agent.SummaryOutputLimitTokens {
		t.Fatalf("output limit = %d", request.OutputLimit)
	}
	if !request.RedactionPolicy.Configured {
		t.Fatal("request does not carry the session redaction policy")
	}
}

// TestBuildCompactSummaryRequestDedupesEvidence pins the duplicate guard:
// many omitted messages share one size bucket, and the raw host diff would
// repeat identical evidence items the envelope validator refuses.
func TestBuildCompactSummaryRequestDedupesEvidence(t *testing.T) {
	summarizer := plainSummarySummarizer(t, &chatSummaryProvider{})
	redaction := contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}}
	body := strings.Repeat("same size body ", 10)
	pre := []provider.Message{
		{Role: provider.RoleUser, Content: body},
		{Role: provider.RoleUser, Content: body},
		{Role: provider.RoleUser, Content: body},
		{Role: provider.RoleUser, Content: "latest objective"},
	}
	retained := []provider.Message{pre[3]}
	request, err := buildCompactSummaryRequest(summarizer, redaction, 4096, pre, retained, compactSummaryRange(t))
	if err != nil {
		t.Fatalf("duplicate-size omitted messages broke the request: %v", err)
	}
	if len(request.Input.Evidence) != 1 {
		t.Fatalf("evidence = %v, want one deduplicated item", request.Input.Evidence)
	}
}

// TestApplyCompactSummarySuccess pins the applied outcome of a successful
// manual-compact summary: bounded durable metadata in the host-redacted
// shape, and a rendered context-summary message for the live session.
func TestApplyCompactSummarySuccess(t *testing.T) {
	fake := &chatSummaryProvider{}
	summarizer := plainSummarySummarizer(t, fake)
	redaction := contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}}
	pre := []provider.Message{
		{Role: provider.RoleUser, Content: "old question"},
		{Role: provider.RoleUser, Content: "latest objective"},
	}
	metadata, injected, ok := applyCompactSummary(context.Background(), summarizer, redaction, 0, pre, pre[1:], compactSummaryRange(t))
	if !ok {
		t.Fatal("applyCompactSummary refused a wired summarizer")
	}
	if len(fake.requests) != 1 {
		t.Fatalf("summary provider calls = %d, want 1", len(fake.requests))
	}
	if !strings.Contains(string(metadata), "host-redacted") {
		t.Fatalf("metadata = %s, want the host-redacted status", metadata)
	}
	if injected.Name != agent.SummaryMessageName {
		t.Fatalf("injected message name = %q, want %q", injected.Name, agent.SummaryMessageName)
	}
	if !strings.Contains(injected.Content, "[host-injected context summary") {
		t.Fatalf("injected content = %q, want the host summary frame", injected.Content)
	}
}

// TestApplyCompactSummaryDegradesOnProviderError pins the never-fail rule at
// the unit boundary: a provider error returns ok=false with no metadata and
// no message, so the structural compact continues unchanged.
func TestApplyCompactSummaryDegradesOnProviderError(t *testing.T) {
	fake := &chatSummaryProvider{err: context.Canceled}
	summarizer := plainSummarySummarizer(t, fake)
	redaction := contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}}
	pre := []provider.Message{{Role: provider.RoleUser, Content: "objective"}}
	metadata, injected, ok := applyCompactSummary(context.Background(), summarizer, redaction, 0, pre, pre, compactSummaryRange(t))
	if ok || metadata != nil || injected.Name != "" {
		t.Fatalf("provider error produced ok=%v metadata=%d name=%q, want a clean degrade", ok, len(metadata), injected.Name)
	}
}
