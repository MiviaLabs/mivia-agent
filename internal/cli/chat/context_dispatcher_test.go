package chat

import (
	"context"
	"encoding/json"
	"github.com/MiviaLabs/mivia-agent/internal/cli/orchestrate"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/context/manager"
	"github.com/MiviaLabs/mivia-agent/internal/context/state"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type dispatcherPreparationProbe struct{ prepares int }

func (p *dispatcherPreparationProbe) Prepare(_ context.Context, input manager.PrepareInput) (manager.Preparation, error) {
	p.prepares++
	rangeValue := state.SourceRange{
		Start: state.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   state.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	return manager.CapturePreparation(input, manager.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, false, "dispatcher-prep-test")
}

func (*dispatcherPreparationProbe) Discard(manager.Preparation) {}

func TestDispatcherInjectsIsolatedContextManager(t *testing.T) {
	principal, err := state.NewPrincipal("workspace", "nested-session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := state.NewBindingRevision("null", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := &dispatcherPreparationProbe{}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry: tools.NewRegistry(), Completer: nullCompleter{}, Model: "model",
		Config: config.DefaultSubagentConfig, ContextPreparationManager: probe,
		ContextPreparationInput: manager.PrepareInput{Principal: principal, Binding: binding, Budget: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	result := d.Invoke(context.Background(), runtime.Request{
		ID: "nested-context", Kind: runtime.Subagent, Name: orchestrate.HandlerMultiStep,
		Input: json.RawMessage(`"task"`), SessionID: principal.SessionID,
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if probe.prepares != 1 {
		t.Fatalf("preparation calls = %d, want 1", probe.prepares)
	}
}

func TestSharedSQLiteInjectedIntoChatAndLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	store, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer orchestrate.CloseSharedSQLite(store)
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry: tools.NewRegistry(), Completer: nullCompleter{}, Model: "model",
		Config: config.SubagentConfig{StoreBackend: "memory", DefaultTimeout: 60}, SharedSQLite: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := orchestrate.OrchestrationRepoForDispatcher(d).(*ledger.StorageLedgerRepository)
	if !ok || repo.UnderlyingStore() != store {
		t.Fatalf("ledger store = %T/%p, want shared %p", repo.UnderlyingStore(), repo.UnderlyingStore(), store)
	}
	d.Close()
	principal, err := state.NewPrincipal("workspace", "shared-session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := state.NewBindingRevision("null", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), state.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatalf("borrowed ledger closed the shared store: %v", err)
	}
}

func TestOrchestrationStateClosesSharedSQLiteOnce(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "close-once.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := orchestrate.CloseSharedSQLite(store); err != nil {
		t.Fatal(err)
	}
	if err := orchestrate.CloseSharedSQLite(store); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Events(context.Background(), "closed"); err == nil {
		t.Fatal("closed shared SQLite still accepted reads")
	}
}

var _ provider.Completer = nullCompleter{}
