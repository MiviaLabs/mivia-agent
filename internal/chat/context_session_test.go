package chat

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type contextPreparationProbe struct {
	prepares int
	discards int
}

func (p *contextPreparationProbe) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.prepares++
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	active, err := contextstate.MarshalCanonical(input.Messages)
	if err != nil {
		return contextmgr.Preparation{}, err
	}
	return contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		ActiveContext: active, SourceRange: rangeValue,
	}, input.Messages, false, "prepare-test")
}

func (p *contextPreparationProbe) Discard(contextmgr.Preparation) { p.discards++ }

type contextPublisherProbe struct {
	commits int
	err     error
}

type recoveryFailurePreparation struct{ err error }

func (p recoveryFailurePreparation) Prepare(ctx context.Context, _ contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	if err := ctx.Err(); err != nil {
		return contextmgr.Preparation{}, err
	}
	return contextmgr.Preparation{}, p.err
}

func (recoveryFailurePreparation) Discard(contextmgr.Preparation) {}

func (p *contextPublisherProbe) Commit(_ context.Context, _ contextmgr.Preparation, _ contextmgr.TurnResult) error {
	p.commits++
	return p.err
}

func contextSessionManager(t *testing.T, session *Session, publisherErr error) (*contextPreparationProbe, *contextPublisherProbe) {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	prep := &contextPreparationProbe{}
	pub := &contextPublisherProbe{err: publisherErr}
	manager := &contextmgr.ContextManager{
		PreparationManager: prep, CheckpointPublisher: pub, Enabled: true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	return prep, pub
}

func TestPlainTurnUsesPreparationTransaction(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	prep, pub := contextSessionManager(t, session, nil)
	if _, err := session.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatal(err)
	}
	if prep.prepares != 1 || pub.commits != 1 || prep.discards != 1 {
		t.Fatalf("prepares=%d commits=%d discards=%d", prep.prepares, pub.commits, prep.discards)
	}
	if session.Store() != nil || session.SessionDir != "" {
		t.Fatal("context-enabled turn retained a legacy JSONL store")
	}
}

func TestContextPreparationRetainsWorktreeInstance(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := session.SetContextWorktreeBinding(instance); err != nil {
		t.Fatal(err)
	}
	contextSessionManager(t, session, nil)
	_, input, ok := session.ContextPreparation()
	if !ok {
		t.Fatal("ContextPreparation unavailable")
	}
	if input.WorktreeInstance != instance {
		t.Fatalf("preparation worktree instance = %+v, want %+v", input.WorktreeInstance, instance)
	}
}

func TestBoundSessionSaveOptionsRetainSetupDirectory(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDir) })
	if err := os.Chdir(dirA); err != nil {
		t.Fatal(err)
	}
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := session.SetContextWorktreeBinding(instance); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dirB); err != nil {
		t.Fatal(err)
	}
	options := session.sessionSaveOptions()
	if options.Dir != dirA {
		t.Fatalf("save directory = %q, want retained %q", options.Dir, dirA)
	}
	if options.WorktreeInstance != instance || options.Worktree != instance.Worktree {
		t.Fatalf("save binding = %+v, %q; want %+v", options.WorktreeInstance, options.Worktree, instance)
	}
}

func TestCheckpointFailureDoesNotFallbackToJSONL(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	prep, pub := contextSessionManager(t, session, errors.New("checkpoint failed"))
	if _, err := session.SendUser(context.Background(), "question", io.Discard); err == nil {
		t.Fatal("checkpoint failure was swallowed")
	}
	if pub.commits != 1 || prep.discards != 1 || session.MessagesCount() != 0 {
		t.Fatalf("commits=%d discards=%d messages=%d", pub.commits, prep.discards, session.MessagesCount())
	}
	if session.Store() != nil || session.SessionDir != "" {
		t.Fatal("checkpoint failure fell back to legacy JSONL")
	}
}

func TestInterruptedPreparationFailureIsNotReportedAsCheckpointConflict(t *testing.T) {
	want := errors.New("recovery preparation failed")
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{PreparationManager: recoveryFailurePreparation{err: want}, CheckpointPublisher: &contextPublisherProbe{}, Enabled: true}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = session.SendUser(ctx, "question", io.Discard)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want recovery error", err)
	}
	if errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Fatalf("recovery error was misreported as checkpoint conflict: %v", err)
	}
}

func TestContextSessionCatalogSaveLoadRestoresHistory(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", Models: []string{"model", "other"}}, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{PreparationManager: contextmgr.StructuralPreparationManager{}, CheckpointPublisher: contextmgr.PreparationCommitter{Store: store}, Enabled: true}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	session.SetBindingFactory(func(providerName, model string) (ModelBinding, error) {
		return ModelBinding{ProviderName: providerName, Model: model, Completer: &fakeCompleter{out: "answer"}}, nil
	})
	session.mu.Lock()
	session.Messages = []provider.Message{{Role: provider.RoleUser, Content: "old question"}, {Role: provider.RoleAssistant, Content: "old answer"}}
	session.mu.Unlock()
	if err := session.Save("named"); err != nil {
		t.Fatal(err)
	}
	if err := session.SwitchBinding(ModelBinding{ProviderName: "fake", Model: "other", Completer: &fakeCompleter{out: "other"}}); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	session.Messages = []provider.Message{{Role: provider.RoleUser, Content: "other question"}, {Role: provider.RoleAssistant, Content: "other answer"}}
	session.mu.Unlock()
	if err := session.Save("other"); err != nil {
		t.Fatal(err)
	}
	_ = session.Clear()
	for i, name := range []string{"named", "other", "named", "other", "named"} {
		if err := session.Load(name); err != nil {
			t.Fatalf("load %d (%s): %v", i, name, err)
		}
		msgs := session.MessagesCopy()
		want := "old question"
		if name == "other" {
			want = "other question"
		}
		if len(msgs) != 2 || msgs[0].Content != want {
			t.Fatalf("load %d (%s) restored history = %#v", i, name, msgs)
		}
	}
}

func TestContextManagerLoadsStoreHeadWhenAttachedSecond(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	store := &contextHeadProbeStore{revision: contextstate.Revision{Session: 4, Durable: 3, Source: 8}}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextManager(&contextmgr.ContextManager{PreparationManager: &contextPreparationProbe{}, CheckpointPublisher: &contextPublisherProbe{}, Enabled: true}, principal); err != nil {
		t.Fatal(err)
	}
	if session.contextHead != store.revision {
		t.Fatalf("context head = %+v, want %+v", session.contextHead, store.revision)
	}
}

type contextHeadProbeStore struct {
	revision contextstate.Revision
	loaded   contextstate.Principal
}

func (s *contextHeadProbeStore) EnsureSession(context.Context, contextstate.EnsureSessionRequest) error {
	return nil
}

func (s *contextHeadProbeStore) Commit(context.Context, contextstate.CommitRequest) error { return nil }

func (s *contextHeadProbeStore) Advance(context.Context, contextstate.AdvanceRequest) error {
	return nil
}

func (s *contextHeadProbeStore) Load(_ context.Context, principal contextstate.Principal, _ string) (contextstate.Snapshot, error) {
	s.loaded = principal
	return contextstate.Snapshot{Revision: s.revision}, nil
}

// Keep the probe tied to the provider-facing contract used by the session.
var _ provider.Completer = (*fakeCompleter)(nil)
var _ contextstate.Store = (*contextHeadProbeStore)(nil)
