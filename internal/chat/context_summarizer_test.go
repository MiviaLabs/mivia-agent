package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestSetSummarizerReplacesOnlyTheSummarizer pins SetSummarizer's contract:
// it swaps the context manager's Summarizer field in place, leaving
// principal/revision/store/every other manager field untouched.
func TestSetSummarizerReplacesOnlyTheSummarizer(t *testing.T) {
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
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.CurrentSummarizerBinding(); ok {
		t.Fatal("session reports a summarizer before any was set")
	}
	revisionBefore := session.contextHead

	summarizer := plainSummarySummarizer(t, &chatSummaryProvider{})
	session.SetSummarizer(summarizer)

	binding, ok := session.CurrentSummarizerBinding()
	if !ok {
		t.Fatal("SetSummarizer did not publish a summarizer")
	}
	if binding.Provider != summarizer.Binding.Provider || binding.Model != summarizer.Binding.Model {
		t.Fatalf("summarizer binding = %+v, want %+v", binding, summarizer.Binding)
	}
	if session.contextHead != revisionBefore {
		t.Fatalf("SetSummarizer changed the context revision: before=%+v after=%+v", revisionBefore, session.contextHead)
	}
	if session.contextPrincipal != principal {
		t.Fatal("SetSummarizer changed the context principal")
	}

	session.SetSummarizer(nil)
	if _, ok := session.CurrentSummarizerBinding(); ok {
		t.Fatal("SetSummarizer(nil) did not clear the summarizer")
	}
}

// TestSetSummarizerNoopWithoutContextManager guards the defensive nil check:
// a session with context disabled must not panic when SetSummarizer runs.
func TestSetSummarizerNoopWithoutContextManager(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "system"}, &fakeCompleter{out: "answer"})
	summarizer := plainSummarySummarizer(t, &chatSummaryProvider{})
	session.SetSummarizer(summarizer)
	if _, ok := session.CurrentSummarizerBinding(); ok {
		t.Fatal("SetSummarizer published a summarizer with no context manager")
	}
}
