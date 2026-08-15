package contextmgr

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

type summaryProviderFunc func(context.Context, SummaryRequest) (Summary, error)

func (f summaryProviderFunc) Summarize(ctx context.Context, request SummaryRequest) (Summary, error) {
	return f(ctx, request)
}

func TestSummarizerUsesCapturedProviderAndTimeout(t *testing.T) {
	request := summaryTestRequest(t)
	request.RedactionPolicy = contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}}
	binding, err := contextstate.NewBindingRevision("provider", "model", 3)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := summaryProviderFunc(func(ctx context.Context, got SummaryRequest) (Summary, error) {
		calls.Add(1)
		if got.Input.Objective != request.Input.Objective || got.Provider != request.Provider || got.Model != request.Model {
			t.Errorf("provider received an unexpected request: %+v", got)
		}
		<-ctx.Done()
		return Summary{}, ctx.Err()
	})
	policy := contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: true, Provider: "provider", Model: "model",
		CredentialScope: "captured-scope", NetworkEnabled: true, EndpointAllowlist: []string{"https://summary.invalid"},
		PolicyDigest: request.Input.PolicyDigest,
	}
	summarizer, err := NewSummarizer(provider, binding, policy)
	if err != nil {
		t.Fatal(err)
	}
	summarizer.Timeout = 20 * time.Millisecond
	_, err = summarizer.Summarize(context.Background(), request)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v, want deadline exceeded", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one", calls.Load())
	}
}

func TestSummarizerDeniesUnconfiguredPolicyWithoutProviderCall(t *testing.T) {
	request := summaryTestRequest(t)
	var calls atomic.Int32
	provider := summaryProviderFunc(func(context.Context, SummaryRequest) (Summary, error) {
		calls.Add(1)
		return Summary{}, nil
	})
	binding, _ := contextstate.NewBindingRevision("provider", "model", 1)
	summarizer, err := NewSummarizer(provider, binding, contextstate.PolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := summarizer.Summarize(context.Background(), request); !errors.Is(err, contextstate.ErrSummaryUnavailable) {
		t.Fatalf("unconfigured policy error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("provider was called while summary policy was unavailable")
	}
}

func TestSummarizerRejectsStaleBinding(t *testing.T) {
	request := summaryTestRequest(t)
	request.RedactionPolicy = contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}}
	binding, _ := contextstate.NewBindingRevision("provider", "model", 1)
	provider := summaryProviderFunc(func(context.Context, SummaryRequest) (Summary, error) {
		return Summary{}, nil
	})
	summarizer, err := NewSummarizer(provider, binding, contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: true, NetworkEnabled: true, CredentialScope: "scope",
		Provider: "provider", Model: "other-model", EndpointAllowlist: request.EndpointAllowlist,
		PolicyDigest: request.Input.PolicyDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := summarizer.Summarize(context.Background(), request); !errors.Is(err, contextstate.ErrStaleBinding) {
		t.Fatalf("stale binding error = %v", err)
	}
}

// TestSummaryRunsWithoutAConfiguredRedactionPolicy pins the opt-out default's
// second half. Compaction drops messages permanently, so the summary is the
// only record of what was removed - it must not be silently disabled by a
// workspace that never wrote a [privacy] section. The summary is derived from
// conversation content the SAME provider already received in full, so
// refusing to send it while sending the conversation protected nothing.
//
// What redaction still governs is DURABILITY, not availability: without a
// configured policy the checkpoint keeps only a digest, never summary
// content. That split is asserted here so relaxing the gate cannot be
// mistaken for relaxing the persistence rule.
func TestSummaryRunsWithoutAConfiguredRedactionPolicy(t *testing.T) {
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	policy := contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: false, NetworkEnabled: true,
		Provider: "provider", Model: "model", CredentialScope: "scope",
		EndpointAllowlist: []string{"https://summary.invalid"},
	}
	request := summaryTestRequest(t)
	request.RedactionPolicy = contextstate.RedactionPolicy{}
	echo := summaryProviderFunc(func(_ context.Context, req SummaryRequest) (Summary, error) {
		return Summary{
			Version: req.Input.Version, Objective: "objective", State: "state",
			SourceRange: req.SourceRange,
		}, nil
	})
	summarizer, err := NewSummarizer(echo, binding, policy)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := summarizer.Summarize(context.Background(), request)
	if err != nil {
		t.Fatalf("an unconfigured redaction policy disabled the summary: %v", err)
	}

	// Durability protection survives: no summary content in the metadata.
	metadata, err := summary.Metadata(policy.RedactionConfigured)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "\"summary\"") {
		t.Fatalf("summary content persisted without a configured redaction policy: %s", metadata)
	}
	if !strings.Contains(string(metadata), "structural-only") {
		t.Fatalf("metadata did not record the structural-only redaction status: %s", metadata)
	}
}
