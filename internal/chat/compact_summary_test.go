package chat

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/storage"

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

// TestManualCompactSummaryStaysLoadable pins the resume-shape contract: the
// summary message a manual compact appends to the live history must survive
// the load path's message-shape validation (provider.ValidateToolPairing
// refuses NAMED user messages, and every restore path runs it). A named
// summary message made the session unresumable after one more turn.
func TestManualCompactSummaryStaysLoadable(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "system"}, &fakeCompleter{out: "answer"})
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Summarizer:          plainSummarySummarizer(t, &chatSummaryProvider{}),
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	// Production installs the session redaction policy beside the manager
	// (configureSessionContext); the summary gate refuses requests without it.
	session.SetContextRedactionPolicy(contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}})
	if _, err := session.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		session.Messages = append(session.Messages,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("old question ", 20)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("old answer ", 20)},
		)
	}
	if err := session.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := session.Messages[len(session.Messages)-1]; !strings.Contains(last.Content, "[host-injected context summary") {
		t.Fatalf("fixture sanity: post-compact tail = %q, want the summary message", last.Content)
	}
	if _, err := session.SendUser(context.Background(), "next question", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateToolPairing(session.Messages); err != nil {
		t.Fatalf("post-compact history is not loadable: %v", err)
	}
}

// TestManualCompactSummarySurvivesRestartWithoutFurtherTurns pins the
// durable-delivery contract for the manual compact: the rendered summary
// message must be part of the committed checkpoint's active context, so a
// fresh session loading the store (a restart with no further turn) still
// receives it. The summary is host-generated - it has no source event of its
// own - so the checkpoint is its only durable carrier; live memory alone must
// not be the delivery mechanism.
func TestManualCompactSummarySurvivesRestartWithoutFurtherTurns(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "system"}, &fakeCompleter{out: "answer"})
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Summarizer:          plainSummarySummarizer(t, &chatSummaryProvider{}),
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	session.SetContextRedactionPolicy(contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}})
	if _, err := session.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		session.Messages = append(session.Messages,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("old question ", 20)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("old answer ", 20)},
		)
	}
	if err := session.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := session.Messages[len(session.Messages)-1]; !strings.Contains(last.Content, "[host-injected context summary") {
		t.Fatalf("fixture sanity: post-compact tail = %q, want the summary message", last.Content)
	}

	// Restart: a fresh session bound to the same principal loads the durable
	// checkpoint without any further turn.
	restarted := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "system"}, &fakeCompleter{out: "answer"})
	restarted.SessionID = session.SessionID
	if err := restarted.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := restarted.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if err := restarted.loadContextSnapshot("restart-without-further-turns"); err != nil {
		t.Fatalf("restart load: %v", err)
	}
	for _, m := range restarted.Messages {
		if strings.Contains(m.Content, "[host-injected context summary") {
			return // the summary survived the restart
		}
	}
	t.Fatalf("restarted session lost the manual-compact summary; loaded %d messages:\n%v", len(restarted.Messages), restarted.Messages)
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
