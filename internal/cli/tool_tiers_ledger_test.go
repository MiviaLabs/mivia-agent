package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestSessionSurfaceCleanupWithoutAgentState(t *testing.T) {
	// A tools-off or hand-built caller owns no ledger store; cleanup must still
	// close the live dispatcher rather than skip it or panic.
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	dispatcher, err := runtime.NewToolDispatcher(tierRegistry("read_file"), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	dispatcher.OnClose(func() { close(closed) })
	sess.SetDispatcher(dispatcher)
	sessionSurfaceCleanup(sess, nil)()
	select {
	case <-closed:
	default:
		t.Fatal("cleanup did not close the live dispatcher")
	}
	if got := ledgerRepoOf(nil); got != nil {
		t.Fatalf("ledgerRepoOf(nil) = %v, want no repo", got)
	}
}

// TestAdoptSessionLedgerRepoSkipsASharedStore: when the context store is wired
// the ledger adapter borrows it, so opening a second one would leave an owner
// with nothing to own.
func TestAdoptSessionLedgerRepoSkipsASharedStore(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	state := &AgentSessionState{}
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	adoptSessionLedgerRepo(sess, config.DefaultSubagentConfig, state,
		sessionRouting{Context: ContextDispatcherWiring{SharedSQLite: store}})
	if state.LedgerRepo != nil || state.OwnedLedgerStore() != nil {
		t.Fatal("a shared context store must not be shadowed by a session-owned ledger")
	}
	// No agent state to hold one is likewise a no-op rather than a leak.
	adoptSessionLedgerRepo(sess, config.DefaultSubagentConfig, nil, sessionRouting{})

	// The routing may not carry the store even when the session has one -
	// the second guard reads it back off the session rather than trusting
	// the caller to have plumbed it.
	contextual := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	if err := enableSessionContext(contextual, t.TempDir(), store, &config.Resolved{}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultSubagentConfig
	cfg.StoreBackend = "sqlite"
	unwired := &AgentSessionState{}
	adoptSessionLedgerRepo(contextual, cfg, unwired, sessionRouting{})
	if unwired.LedgerRepo != nil || unwired.OwnedLedgerStore() != nil {
		t.Fatal("the session's own context store must not be shadowed either")
	}
}

// sqliteLedgerConfig is a subagent config whose ledger backend is a real
// throwaway SQLite file, so the session actually owns a durable store.
func sqliteLedgerConfig(t *testing.T) config.SubagentConfig {
	t.Helper()
	cfg := config.DefaultSubagentConfig
	cfg.StoreBackend = "sqlite"
	cfg.StorePath = filepath.Join(t.TempDir(), "ledger.db")
	return cfg
}

// TestSessionOwnedLedgerRepoStaysRecoverable (R4-1): the coordinator reaches
// startup recovery through an optional-interface assertion on the repository it
// was handed. A wrapper that embeds ledger.LedgerRepository does not promote
// Recover, so wrapping silently turns interrupted-run recovery off.
func TestSessionOwnedLedgerRepoStaysRecoverable(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	state := &AgentSessionState{}
	adoptSessionLedgerRepo(sess, sqliteLedgerConfig(t), state, sessionRouting{})
	t.Cleanup(func() { releaseSessionLedgerRepo(state) })
	if state.LedgerRepo == nil {
		t.Fatal("no session-owned ledger repository was adopted")
	}
	recoverer, ok := state.LedgerRepo.(interface {
		Recover(ctx context.Context) ([]ledger.RecoveredRun, error)
	})
	if !ok {
		t.Fatal("the session-owned ledger repository hides Recover from the coordinator")
	}
	if _, err := recoverer.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
}

// TestInitCoordinatorLeavesACallerOwnedStoreOpen (R4-1): closing the dispatcher
// must not close a durable repository the caller owns - the session keeps using
// it across every surface rebuild. Ownership is decided by who opened the
// store, not by what concrete type the coordinator recognises.
func TestInitCoordinatorLeavesACallerOwnedStoreOpen(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewStorageLedgerRepository(store)
	t.Cleanup(func() { _ = repo.Close() })
	dispatcher, err := runtime.NewToolDispatcher(tierRegistry("read_file"), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	initCoordinator(dispatcher, config.DefaultSubagentConfig, repo)
	dispatcher.Close()
	if _, err := repo.Recover(context.Background()); err != nil {
		t.Fatalf("the dispatcher closed a caller-owned ledger store: %v", err)
	}
}

// TestAttachFailureReleasesTheSessionOwnedLedgerStore (R4-3): the ledger store
// is opened before the dispatcher is built, and a failed build returns no
// cleanup, so the error path is the only place that can close it.
func TestAttachFailureReleasesTheSessionOwnedLedgerStore(t *testing.T) {
	dir := t.TempDir()
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	// read_output is a ledger tool the dispatcher registers itself; a collision
	// fails the build after the store has been adopted.
	sess.Tools = tierRegistry("read_output")
	state := &AgentSessionState{}
	cleanup, err := attachSessionDispatcher(sess, dir, "m", sqliteLedgerConfig(t), state, skills.NewRegistry(), sessionRouting{})
	if err == nil {
		t.Fatal("expected the dispatcher build to fail")
	}
	if cleanup != nil {
		t.Fatal("a failed attach handed back a cleanup the caller is told not to use")
	}
	if state.LedgerRepo != nil || state.OwnedLedgerStore() != nil {
		t.Fatal("the session-owned ledger store leaked on the attach error path")
	}
}

// TestReleaseSessionLedgerRepoClosesTheStore pins what "release" means: the
// store is really closed, not merely forgotten.
func TestReleaseSessionLedgerRepoClosesTheStore(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	state := &AgentSessionState{}
	adoptSessionLedgerRepo(sess, sqliteLedgerConfig(t), state, sessionRouting{})
	store := state.OwnedLedgerStore()
	if store == nil {
		t.Fatal("no durable store was adopted")
	}
	releaseSessionLedgerRepo(state)
	if state.LedgerRepo != nil || state.OwnedLedgerStore() != nil {
		t.Fatal("release left the freed repository reachable")
	}
	if _, err := store.Recover(context.Background()); err == nil {
		t.Fatal("release did not close the store")
	}
}

// TestInitCoordinatorClosesOnlyItsOwnStore: the coordinator opens a durable
// store when no repo is supplied, and only that store is its to close. A
// caller-supplied repo outlives the dispatcher (round-4: the old type-assertion
// closed whatever it recognized, which is what forced the wrapper that then
// hid Recover from the coordinator).
func TestInitCoordinatorClosesOnlyItsOwnStore(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultSubagentConfig
	cfg.StoreBackend = "sqlite"
	cfg.StorePath = filepath.Join(dir, "orchestration.db")
	dispatcher, err := runtime.NewToolDispatcher(tierRegistry("read_file"), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	// No repo argument at all: the coordinator opens (and owns) its own.
	initCoordinator(dispatcher, cfg)
	repo, ok := coordinatorRepos.Load(dispatcher)
	if !ok {
		t.Fatal("initCoordinator registered no repo")
	}
	if _, isMemory := repo.(*ledger.MemoryLedgerRepository); isMemory {
		t.Skip("sqlite backend fell back to memory; nothing owned to close")
	}
	dispatcher.Close()
	if _, still := coordinatorRepos.Load(dispatcher); still {
		t.Fatal("the close hook did not deregister the coordinator repo")
	}
	// The store it opened is closed, so a fresh open of the same file works.
	reopened, err := storage.OpenSQLite(cfg.StorePath)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	_ = reopened.Close()
}

func TestReleaseSessionLedgerRepoWithoutState(t *testing.T) {
	// The failure path can run before any agent state exists; releasing must
	// be a no-op rather than a nil dereference.
	releaseSessionLedgerRepo(nil)
	releaseSessionLedgerRepo(&AgentSessionState{})
}
