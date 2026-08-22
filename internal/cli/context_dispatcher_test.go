package cli

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type dispatcherPreparationProbe struct{ prepares int }

func (p *dispatcherPreparationProbe) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.prepares++
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	return contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, false, "dispatcher-prep-test")
}

func (*dispatcherPreparationProbe) Discard(contextmgr.Preparation) {}

func TestDispatcherInjectsIsolatedContextManager(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "nested-session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("null", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := &dispatcherPreparationProbe{}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry: tools.NewRegistry(), Completer: nullCompleter{}, Model: "model",
		Config: config.DefaultSubagentConfig, ContextPreparationManager: probe,
		ContextPreparationInput: contextmgr.PrepareInput{Principal: principal, Binding: binding, Budget: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	result := d.Invoke(context.Background(), runtime.Request{
		ID: "nested-context", Kind: runtime.Subagent, Name: cliorchestrate.HandlerMultiStep,
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
	defer cliorchestrate.CloseSharedSQLite(store)
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry: tools.NewRegistry(), Completer: nullCompleter{}, Model: "model",
		Config: config.SubagentConfig{StoreBackend: "memory", DefaultTimeout: 60}, SharedSQLite: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := cliorchestrate.OrchestrationRepoForDispatcher(d).(*ledger.StorageLedgerRepository)
	if !ok || repo.UnderlyingStore() != store {
		t.Fatalf("ledger store = %T/%p, want shared %p", repo.UnderlyingStore(), repo.UnderlyingStore(), store)
	}
	d.Close()
	principal, err := contextstate.NewPrincipal("workspace", "shared-session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("null", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatalf("borrowed ledger closed the shared store: %v", err)
	}
}

func TestOrchestrationStateClosesSharedSQLiteOnce(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "close-once.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cliorchestrate.CloseSharedSQLite(store); err != nil {
		t.Fatal(err)
	}
	if err := cliorchestrate.CloseSharedSQLite(store); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Events(context.Background(), "closed"); err == nil {
		t.Fatal("closed shared SQLite still accepted reads")
	}
}

var _ provider.Completer = nullCompleter{}
