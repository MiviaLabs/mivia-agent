package chat

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// chatSummaryProvider records every summary request and returns a short
// validated summary.
type chatSummaryProvider struct {
	requests []contextmgr.SummaryRequest
	err      error
}

func (p *chatSummaryProvider) Summarize(_ context.Context, request contextmgr.SummaryRequest) (contextmgr.Summary, error) {
	p.requests = append(p.requests, request)
	if p.err != nil {
		return contextmgr.Summary{}, p.err
	}
	return contextmgr.Summary{
		Version:     request.Input.Version,
		Objective:   "summarized objective",
		State:       request.Input.State,
		SourceRange: request.SourceRange,
	}, nil
}

func plainSummarySummarizer(t *testing.T, provider contextmgr.SummaryProvider) *contextmgr.Summarizer {
	t.Helper()
	binding, err := contextstate.NewBindingRevision("fake", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	summarizer, err := contextmgr.NewSummarizer(provider, binding, contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: true, Provider: "fake", Model: "model",
		CredentialScope: "scope", NetworkEnabled: true,
		EndpointAllowlist: []string{"https://summary.invalid"},
		PolicyDigest:      strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &summarizer
}

// capturingStreamCompleter records every plain-chat request so the test can
// inspect what the fake provider actually received.
type capturingStreamCompleter struct {
	requests []provider.Request
	err      error
}

func (c *capturingStreamCompleter) Name() string { return "capture" }
func (c *capturingStreamCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "answer", nil
}
func (c *capturingStreamCompleter) ChatStream(_ context.Context, req provider.Request, w io.Writer) (string, error) {
	c.requests = append(c.requests, req)
	if c.err != nil {
		return "", c.err
	}
	if w != nil {
		_, _ = io.WriteString(w, "answer")
	}
	return "answer", nil
}
func (c *capturingStreamCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "answer", FinishReason: "stop"}, nil
}

// newPlainContextSession builds a plain (non-agent) context-enabled session
// with an optional Summarizer wired into the ContextManager.
func newPlainContextSession(t *testing.T, store contextstate.Store, completer provider.Completer, summarizer *contextmgr.Summarizer) (*Session, contextstate.Principal) {
	t.Helper()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, completer)
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Summarizer:          summarizer,
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	session.SetContextRedactionPolicy(contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}})
	return session, principal
}

func requestsCarrySummary(requests []provider.Request) bool {
	for _, request := range requests {
		for _, message := range request.Messages {
			if message.Name == agent.SummaryMessageName {
				return true
			}
		}
	}
	return false
}

// TestPlainChatInjectsSummaryIntoStreamRequest is the plain-chat Phase 2
// proof: after a compacted preparation, the ChatStream request the fake
// completer receives carries the context-summary message, while the durable
// commit, the in-memory session, and the compaction event carry no summary
// content (INV-AG-32 omission stays).
func TestPlainChatInjectsSummaryIntoStreamRequest(t *testing.T) {
	store, _ := openSharedContextStore(t)
	provider := &chatSummaryProvider{}
	completer := &capturingStreamCompleter{}
	session, principal := newPlainContextSession(t, store, completer, plainSummarySummarizer(t, provider))

	if _, err := session.SendUser(context.Background(), "first question "+strings.Repeat("x", 2000), io.Discard); err != nil {
		t.Fatal(err)
	}
	next := "second question"
	forceCompactionBudget(t, session, next)
	if _, err := session.SendUser(context.Background(), next, io.Discard); err != nil {
		t.Fatal(err)
	}

	if len(completer.requests) < 2 {
		t.Fatalf("requests=%d, want two turns", len(completer.requests))
	}
	last := completer.requests[len(completer.requests)-1]
	found := false
	for _, message := range last.Messages {
		if message.Name == agent.SummaryMessageName {
			found = true
			if !strings.Contains(message.Content, "context summary") {
				t.Fatalf("summary content missing header: %q", message.Content)
			}
		}
	}
	if !found {
		t.Fatal("plain ChatStream request did not carry the context-summary message")
	}
	if len(provider.requests) == 0 {
		t.Fatal("summary provider was never called on the compacted plain turn")
	}

	// Durable commit contains no summary content.
	snapshot, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	active := string(snapshot.Active.ActiveContext)
	if strings.Contains(active, "context summary") || strings.Contains(active, agent.SummaryMessageName) {
		t.Fatalf("durable ActiveContext carries summary content: %s", active)
	}
	if len(snapshot.Active.SummaryMetadata) != 0 {
		t.Fatal("structural-only commit persisted summary metadata")
	}
	// The in-memory session history never adopted the ephemeral message.
	for _, message := range session.MessagesCopy() {
		if message.Name == agent.SummaryMessageName || strings.Contains(message.Content, "context summary") {
			t.Fatal("summary leaked into session history")
		}
	}
}

// TestPlainChatSummarizerErrorFallsBack pins that a summarizer error on the
// plain path falls back structural-only: no summary in the request, the turn
// still commits durably, and the session is not wedged.
func TestPlainChatSummarizerErrorFallsBack(t *testing.T) {
	store, _ := openSharedContextStore(t)
	provider := &chatSummaryProvider{err: errors.New("provider down")}
	completer := &capturingStreamCompleter{}
	session, principal := newPlainContextSession(t, store, completer, plainSummarySummarizer(t, provider))

	if _, err := session.SendUser(context.Background(), "first question "+strings.Repeat("x", 2000), io.Discard); err != nil {
		t.Fatal(err)
	}
	prior, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	next := "second question"
	forceCompactionBudget(t, session, next)
	if _, err := session.SendUser(context.Background(), next, io.Discard); err != nil {
		t.Fatalf("summarizer error failed the plain turn: %v", err)
	}
	if requestsCarrySummary(completer.requests) {
		t.Fatal("errored summarizer still injected a summary")
	}
	after, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision.Durable <= prior.Revision.Durable {
		t.Fatalf("durable revision did not advance despite summarizer error: before=%d after=%d", prior.Revision.Durable, after.Revision.Durable)
	}
	if strings.Contains(string(after.Active.ActiveContext), "context summary") {
		t.Fatal("fallback commit carried summary content")
	}
}
