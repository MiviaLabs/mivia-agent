package chat

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestLoadCannotOverwriteModelSwitch(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession(&config.Resolved{ProviderName: "p", Model: "current", Models: []string{"current", "next"}}, &fakeCompleter{out: "answer"})
	bindContextSession(t, sess, store)
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "before-switch"}}
	if err := sess.Save("saved"); err != nil {
		t.Fatal(err)
	}
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "before-switch"}}

	blocking := blockingCatalogStore{Store: store, SessionCatalog: store, started: make(chan struct{}), release: make(chan struct{})}
	sess.mu.Lock()
	sess.contextStore = blocking
	sess.mu.Unlock()

	result := make(chan error, 1)
	go func() { result <- sess.Load("saved") }()
	<-blocking.started
	if !sess.SelectModel("next") {
		t.Fatal("model switch was rejected")
	}
	close(blocking.release)
	if err := <-result; !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("stale load error = %v, want ErrStaleOperation", err)
	}
	if sess.CurrentModel() != "next" || !strings.Contains(historyBlob(sess), "before-switch") {
		t.Fatalf("stale load changed model/history: model=%q history=%s", sess.CurrentModel(), historyBlob(sess))
	}
}

func TestAutoSaveUsesCurrentModel(t *testing.T) {
	s, _ := contextCatalogSession(t)
	s.Messages = []provider.Message{{Role: provider.RoleUser, Content: "hello"}, {Role: provider.RoleAssistant, Content: "hi"}}
	s.mu.Lock()
	s.model = "selected-model"
	s.mu.Unlock()
	s.SaveAfterTurn()
	infos, err := s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != s.SessionID || infos[0].Model != "selected-model" {
		t.Fatalf("saved infos = %+v, want exactly one %q entry with model selected-model", infos, s.SessionID)
	}
}

func TestLoadModelPolicy(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// seedContextSnapshot's seeder session is constructed directly with
	// Model: "removed", so the binding it saves actually carries "removed" -
	// setting sess.binding.Model by hand instead would be undone by
	// captureBindingLocked, which resyncs binding.Model from sess.model
	// (config's Model: "current" here) on every Save.
	seedContextSnapshot(t, store, "saved", []provider.Message{{Role: provider.RoleUser, Content: "saved"}}, "p", "removed")

	// No binding factory: ModelRestoreNotice's rejection is set by
	// restoreModelLocked, which only runs on the no-factory publishLoadedMessages
	// path. With a factory, an unlisted model instead fails Load outright via
	// publishLoadedSession's bindingAllowsLocked check.
	sess := NewSession(&config.Resolved{ProviderName: "p", Model: "current", Models: []string{"current", "listed"}}, &fakeCompleter{out: "answer"})
	bindContextSession(t, sess, store)
	if err := sess.Load("saved"); err != nil {
		t.Fatal(err)
	}
	saved, current, ok := sess.ModelRestoreNotice()
	if sess.CurrentModel() != "current" || !ok || saved != "removed" || current != "current" {
		t.Fatalf("model=%q notice=(%q,%q,%v)", sess.CurrentModel(), saved, current, ok)
	}
}

func TestLoadRestoresListedAndUnrestrictedModels(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// seedContextSnapshot's throwaway seeder session carries the target model
	// directly from construction, so its own Save is never subject to
	// captureBindingLocked resyncing binding.Model back to a *different*
	// loader session's configured default.
	seedContextSnapshot(t, store, "listed", []provider.Message{{Role: provider.RoleUser, Content: "saved"}}, "p", "listed")
	seedContextSnapshot(t, store, "free", []provider.Message{{Role: provider.RoleUser, Content: "saved"}}, "p", "anything")

	managed := NewSession(&config.Resolved{ProviderName: "p", Model: "current", Models: []string{"current", "listed"}}, &fakeCompleter{out: "answer"})
	bindContextSession(t, managed, store)
	if err := managed.Load("listed"); err != nil {
		t.Fatal(err)
	}
	if managed.CurrentModel() != "listed" {
		t.Fatalf("managed=%q", managed.CurrentModel())
	}

	free := NewSession(&config.Resolved{ProviderName: "p", Model: "current"}, &fakeCompleter{out: "answer"})
	bindContextSession(t, free, store)
	if err := free.Load("free"); err != nil {
		t.Fatal(err)
	}
	if free.CurrentModel() != "anything" {
		t.Fatalf("free=%q", free.CurrentModel())
	}
}
