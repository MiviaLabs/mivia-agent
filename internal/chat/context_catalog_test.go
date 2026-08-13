package chat

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// catalogBindingFactory returns a Session binding factory that always
// resolves to a working fakeCompleter for the requested provider/model, so
// tests can exercise a real provider/model switch and a real live resume
// without a network provider - mirroring how the mivia CLI's own binding
// factory works for "mivia chat" (see internal/cli/sessions_command.go's
// newCatalogSession, which does the same for the read-only "sessions show"
// path).
func catalogBindingFactory() func(string, string) (ModelBinding, error) {
	return func(providerName, model string) (ModelBinding, error) {
		return ModelBinding{
			ProviderName: providerName,
			Model:        model,
			Completer:    &fakeCompleter{out: "answer from " + providerName},
			Profile:      config.ModelSpec{Name: model, ContextWindowTokens: DefaultMaxContextTokens},
		}, nil
	}
}

// wireCatalogSession builds a Session against a shared SQLite context store,
// mirroring resyncSessionContext but taking the store as a parameter so two
// independent Session instances (simulating two separate "mivia chat"
// process invocations) can share one durable catalog.
func wireCatalogSession(t *testing.T, store *storage.SQLite, res *config.Resolved, completer *fakeCompleter) *Session {
	t.Helper()
	session := NewSession(res, completer)
	session.SetBindingFactory(catalogBindingFactory())
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
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	return session
}

// TestLiveResumeSurvivesModelSwitchedAwayFromConfigDefault is the live
// resume ("mivia chat --session <id>") counterpart of
// TestSessionsShowSurvivesModelSwitchedAwayFromConfigDefault in
// internal/cli: reopening a session whose saved provider/model differs from
// a FRESH process's config default (e.g. after switching models
// mid-conversation via mivia-agent-desktop's ModelSwitcherButton, then
// reopening the thread later - a brand new process, with no memory of the
// switch, is exactly what the desktop app spawns on every resume) used to
// fail outright with "advance context binding: stale binding: context
// binding changed" - both at Load and, had Load's own CAS been skipped
// some other way, on the very next turn's checkpoint commit too. See
// context_catalog.go's loadContextCatalog and reclaimContextSession.
func TestLiveResumeSurvivesModelSwitchedAwayFromConfigDefault(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Process 1: the original conversation, switched away from the config
	// default provider/model mid-session. A context-catalog session never
	// needs an explicit Save - SendUser's own checkpoint commits durably
	// register it under its own system-assigned SessionID (SaveAfterTurn is
	// a no-op once ContextEnabled, see fencing.go), exactly the "mivia chat"
	// id a real desktop-app thread later resumes by (see
	// mivia-agent-desktop's AgentSessionSummary.session_id).
	original := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "hi from ollama"})
	if _, err := original.SendUser(context.Background(), "hello on ollama", io.Discard); err != nil {
		t.Fatalf("send on ollama: %v", err)
	}
	// Switched twice (not once): the first switch alone would leave the
	// row's generation coincidentally equal to a fresh resuming process's
	// own "start at 0, +1" default, masking a fix that merely republishes
	// s.binding's own stale generation instead of the reclaimed row's real
	// one. A second switch breaks that coincidence.
	for _, target := range []struct{ provider, model string }{
		{"openrouter", "some/other-model"},
		{"anthropic", "yet-another-model"},
	} {
		switched, err := catalogBindingFactory()(target.provider, target.model)
		if err != nil {
			t.Fatal(err)
		}
		if err := original.SwitchBinding(switched); err != nil {
			t.Fatalf("switch to %s: %v", target.model, err)
		}
		if _, err := original.SendUser(context.Background(), "hello on "+target.model, io.Discard); err != nil {
			t.Fatalf("send on %s: %v", target.model, err)
		}
	}

	// Process 2: a fresh session (mirroring a fresh "mivia chat --session
	// <id>" invocation) whose config default is back to "ollama" - the
	// resuming process has no memory of the earlier switch until it loads
	// the session's own saved binding.
	resumed := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "hi again"})

	if err := resumed.Load(original.SessionID); err != nil {
		t.Fatalf("Load(%s) = %v, want success (this used to fail: advance context binding: stale binding: context binding changed)", original.SessionID, err)
	}
	if got := resumed.CurrentSelection(); got.ProviderName != "anthropic" || got.Model != "yet-another-model" {
		t.Fatalf("resumed selection = %+v, want anthropic/yet-another-model", got)
	}
	if len(resumed.MessagesCopy()) == 0 {
		t.Fatal("resumed session has no messages after Load")
	}

	// The regression's other half: the CAS must also succeed on the very
	// next real turn, not just at Load - a generation mismatch left behind
	// by Load would otherwise surface the identical error one message
	// later, which is what the user actually hit (the failure came from
	// sending a prompt, not from opening the thread).
	if _, err := resumed.SendUser(context.Background(), "still there?", io.Discard); err != nil {
		t.Fatalf("send after resume = %v, want success", err)
	}
}
