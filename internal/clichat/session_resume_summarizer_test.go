package clichat

import (
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestSessionResumeRefreshesTheSummarizer pins the fix for the resume half of
// the stale-summarizer bug (TestPublishModelSwitchRefreshesTheSummarizer pins
// the /model half): setupChatSessionContext calls enableSessionContext, which
// captures the summarizer against whatever binding the session was
// constructed with, BEFORE chat_command.go's runChat calls sess.Load for
// --session. A resumed session whose saved provider/model differs from that
// startup binding left every compaction for the rest of the process
// summarizing through the pre-resume model/completer - runChat now calls
// cliagents.RefreshSummarizerAfterModelSwitch right after sess.Load succeeds,
// mirroring what uiadapter/session_pool.go already did for TUI resume.
func TestSessionResumeRefreshesTheSummarizer(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	res.ProviderName = "provider-a"
	res.Model = "model-a"

	session := chat.NewSession(res, namedStubCompleter{name: "provider-a"})
	if err := enableSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	before, ok := session.CurrentSummarizerBinding()
	if !ok || before.Provider != "provider-a" || before.Model != "model-a" {
		t.Fatalf("setup summarizer binding = %+v, ok=%v, want provider-a/model-a", before, ok)
	}

	// A resumed session's Load publishes whatever provider/model the saved
	// row carries via the binding factory - built directly here rather than
	// through a real store round-trip, matching how
	// TestPublishModelSwitchRefreshesTheSummarizer drives SwitchBinding
	// directly instead of through the /model command.
	switched := chat.ModelBinding{
		ProviderName: "provider-b",
		Model:        "model-b",
		Completer:    namedStubCompleter{name: "provider-b"},
		Profile:      config.ModelSpec{Name: "model-b", ContextWindowTokens: chat.DefaultMaxContextTokens},
	}
	if err := session.SwitchBinding(switched); err != nil {
		t.Fatalf("SwitchBinding (simulating Load's publishLoadedSession): %v", err)
	}
	// The call runChat now makes right after a successful sess.Load.
	refreshSummarizerAfterModelSwitch(session, res)

	after, ok := session.CurrentSummarizerBinding()
	if !ok {
		t.Fatal("summarizer was cleared instead of refreshed")
	}
	if after.Provider != "provider-b" || after.Model != "model-b" {
		t.Fatalf("summarizer binding after resume = %+v, want provider-b/model-b (stale binding bug)", after)
	}
}
