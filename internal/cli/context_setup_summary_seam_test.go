package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// cliSummaryProvider returns a short validated summary for every request.
type cliSummaryProvider struct {
	calls int
}

func (p *cliSummaryProvider) Summarize(_ context.Context, request contextmgr.SummaryRequest) (contextmgr.Summary, error) {
	p.calls++
	return contextmgr.Summary{
		Version:     request.Input.Version,
		Objective:   "summarized objective",
		State:       request.Input.State,
		SourceRange: request.SourceRange,
	}, nil
}

func cliSummarySummarizer(t *testing.T, provider contextmgr.SummaryProvider) *contextmgr.Summarizer {
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

// cliSummaryBuilder derives the commit-time summary request from the
// preparation itself, mirroring the documented Phase 1 seam.
func cliSummaryBuilder(summarizer *contextmgr.Summarizer) contextmgr.SummaryRequestBuilder {
	return func(preparation contextmgr.Preparation) (contextmgr.SummaryRequest, error) {
		objective := ""
		for index := len(preparation.Messages) - 1; index >= 0; index-- {
			if preparation.Messages[index].Role == provider.RoleUser {
				objective = agent.SummaryFieldText(preparation.Messages[index].Content)
				break
			}
		}
		return contextmgr.BuildSummaryRequest(contextmgr.SummaryBuildInput{
			Version:           contextmgr.SummarySchemaVersion,
			Objective:         objective,
			SourceRange:       preparation.Token.Range,
			PolicyDigest:      summarizer.Policy.PolicyDigest,
			Provider:          summarizer.Binding.Provider,
			Model:             summarizer.Binding.Model,
			EndpointAllowlist: summarizer.Policy.EndpointAllowlist,
			RedactionPolicy:   contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}},
			Budget:            4096,
			OutputLimit:       agent.SummaryOutputLimitTokens,
		})
	}
}

// TestContextSetupSummarySeamStructuralDefault pins the production wiring: with
// no SummaryProvider, enableSessionContext routes a structural-only manager
// through the seam (nil Summarizer, committer without summary wiring) and an
// enabled session commits a checkpoint with no summary metadata - exactly
// today's behavior.
func TestContextSetupSummarySeamStructuralDefault(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var captured *contextmgr.ContextManager
	original := setContextManagerForSetup
	setContextManagerForSetup = func(session *chat.Session, manager *contextmgr.ContextManager, principal contextstate.Principal, _ ...contextstate.PolicySnapshot) error {
		captured = manager
		return session.SetContextManager(manager, principal)
	}
	t.Cleanup(func() { setContextManagerForSetup = original })

	res := &config.Resolved{Model: "model", ProviderName: "fake", SystemPrompt: "sys"}
	session := chat.NewSession(res, stubAgentCompleter{})
	if err := enableSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("context manager was not routed through the setup seam")
	}
	if captured.Summarizer != nil {
		t.Fatal("setup wired a Summarizer without a summary provider")
	}
	committer, ok := captured.CheckpointPublisher.(contextmgr.PreparationCommitter)
	if !ok {
		t.Fatalf("checkpoint publisher = %T, want PreparationCommitter", captured.CheckpointPublisher)
	}
	if committer.Summarizer != nil || committer.SummaryBuilder != nil {
		t.Fatal("structural-only committer carries summary wiring")
	}

	if _, err := session.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	_, input, ok := session.ContextPreparation()
	if !ok {
		t.Fatal("session is not context-enabled")
	}
	snapshot, err := store.Load(context.Background(), input.Principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Active.SummaryMetadata) != 0 {
		t.Fatal("structural-only turn persisted summary metadata")
	}
}

// TestContextSetupSummarySeamPersistsMetadata drives the commit-time builder
// seam through the session: with a fake Summarizer and builder wired into the
// captured manager, a compacted turn's CommitPreparation persists validated
// SummaryMetadata on the durable checkpoint.
func TestContextSetupSummarySeamPersistsMetadata(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fakeProvider := &cliSummaryProvider{}
	summarizer := cliSummarySummarizer(t, fakeProvider)
	original := setContextManagerForSetup
	setContextManagerForSetup = func(session *chat.Session, manager *contextmgr.ContextManager, principal contextstate.Principal, _ ...contextstate.PolicySnapshot) error {
		committer, ok := manager.CheckpointPublisher.(contextmgr.PreparationCommitter)
		if !ok {
			return fmt.Errorf("checkpoint publisher = %T, want PreparationCommitter", manager.CheckpointPublisher)
		}
		committer.Summarizer = summarizer
		committer.SummaryBuilder = cliSummaryBuilder(summarizer)
		manager.CheckpointPublisher = committer
		manager.Summarizer = summarizer
		return session.SetContextManager(manager, principal)
	}
	t.Cleanup(func() { setContextManagerForSetup = original })

	res := &config.Resolved{Model: "model", ProviderName: "fake", SystemPrompt: "sys"}
	session := chat.NewSession(res, stubAgentCompleter{})
	if err := enableSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	// Seed history, then tighten the budget so the next turn's preparation
	// compacts and the commit-time builder runs.
	if _, err := session.SendUser(context.Background(), "first "+strings.Repeat("x", 2000), io.Discard); err != nil {
		t.Fatal(err)
	}
	next := "second question"
	cost, err := provider.EstimatePromptCost(append(session.MessagesCopy(), provider.Message{Role: provider.RoleUser, Content: next}), nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetPromptBudget(cost); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUser(context.Background(), next, io.Discard); err != nil {
		t.Fatal(err)
	}

	_, input, ok := session.ContextPreparation()
	if !ok {
		t.Fatal("session is not context-enabled")
	}
	snapshot, err := store.Load(context.Background(), input.Principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Active.SummaryMetadata) == 0 {
		t.Fatal("compacted turn did not persist SummaryMetadata through the builder seam")
	}
	if fakeProvider.calls == 0 {
		t.Fatal("commit-time Summarizer was never called")
	}
	// The durable ActiveContext DOES carry the rendered summary (INV-AG-39):
	// compaction dropped the summarized messages for good, so the checkpoint
	// is the summary's only durable carrier. What must stay summary-free is
	// source projection, which INV-AG-32 covers and
	// TestPlainChatInjectsSummaryIntoStreamRequest asserts.
	if !strings.Contains(string(snapshot.Active.ActiveContext), "[host-injected context summary") {
		t.Fatal("ActiveContext dropped the summary of the compacted messages")
	}
}
