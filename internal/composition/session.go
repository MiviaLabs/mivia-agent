package composition

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// SessionInput carries every value BuildSession needs to construct a working
// chat.Session: the resolved config and completer chat.NewSession itself
// takes, the registry and dispatcher inputs BuildRegistry/BuildDispatcher
// already accept, and the checkpoint store BuildSession opens and wires as
// the session's context store.
type SessionInput struct {
	// Config is the resolved workspace configuration. Required:
	// chat.NewSession reads it for the model, prompt, and token budgets.
	Config *config.Resolved
	// Completer is the provider completer the session's initial binding
	// uses. May be nil (chat.NewSession accepts a nil completer for
	// construction), but a nil completer cannot run a turn.
	Completer provider.Completer

	// Registry configures the tool registry BuildRegistry builds. Its
	// Workspace field may be nil, which yields a registry with no
	// filesystem tools.
	Registry RegistryInput
	// Dispatcher configures the dispatcher BuildDispatcher builds.
	// Dispatcher.Registry is overwritten with the registry BuildSession just
	// built, so the caller does not need to set it.
	Dispatcher DispatcherInput

	// EventBus is the session's event bus. Nil mints a fresh events.New().
	EventBus *events.Bus

	// StorePath is the SQLite file BuildSession opens for the session's
	// checkpoint store. Required: chat.Session has no durable checkpoint
	// history without one.
	StorePath string
	// WorkspaceID identifies the checkpoint principal's workspace scope.
	// Ignored when Principal is already bound.
	WorkspaceID string
	// SubjectID identifies the checkpoint principal's subject scope. Empty
	// defaults to the session's own SessionID. Ignored when Principal is
	// already bound.
	SubjectID string
	// Principal, when bound (IsBound() true), is used as-is instead of
	// minting a fresh one from WorkspaceID/SubjectID. A caller that needs to
	// read its own checkpoint back later (contextstate.Principal's capability
	// is random per mint - see contextstate.NewPrincipal - and store.Load
	// rejects a principal whose capability does not match what was written)
	// must supply the same Principal value it will read with.
	Principal contextstate.Principal
}

// BuildSession wires a chat.Session end to end: the completer chat.NewSession
// takes directly, the tool registry (BuildRegistry), the dispatcher
// (BuildDispatcher), the event bus, and a SQLite-backed checkpoint store. It
// returns the checkpoint principal actually installed (in.Principal as-is,
// or a freshly minted one), so a caller can read its own checkpoint back
// through the returned store. The caller owns the store's Close; the session
// holds no reference that outlives it.
func BuildSession(in SessionInput) (*chat.Session, *storage.SQLite, contextstate.Principal, error) {
	if in.Config == nil {
		return nil, nil, contextstate.Principal{}, fmt.Errorf("build session: config is required")
	}
	sess := chat.NewSession(in.Config, in.Completer)
	sess.UseTools = true

	registry, err := BuildRegistry(in.Registry)
	if err != nil {
		return nil, nil, contextstate.Principal{}, fmt.Errorf("build session: %w", err)
	}
	sess.Tools = registry

	dispatcherInput := in.Dispatcher
	dispatcherInput.Registry = registry
	dispatcher, err := BuildDispatcher(dispatcherInput)
	if err != nil {
		return nil, nil, contextstate.Principal{}, fmt.Errorf("build session: %w", err)
	}
	sess.SetDispatcher(dispatcher)

	bus := in.EventBus
	if bus == nil {
		bus = events.New()
	}
	sess.EventBus = bus

	store, principal, err := buildSessionCheckpointStore(sess, in)
	if err != nil {
		return nil, nil, contextstate.Principal{}, fmt.Errorf("build session: %w", err)
	}
	return sess, store, principal, nil
}

// buildSessionCheckpointStore opens in.StorePath and installs it as sess's
// context store and checkpoint publisher, the same
// StructuralPreparationManager/PreparationCommitter pairing
// internal/cli/context_setup_session.go's enableSessionContext installs (no
// LLM summarizer: BuildSession stays structural-only, matching an
// unconfigured [context.summary] workspace).
func buildSessionCheckpointStore(sess *chat.Session, in SessionInput) (*storage.SQLite, contextstate.Principal, error) {
	if in.StorePath == "" {
		return nil, contextstate.Principal{}, fmt.Errorf("store path is required")
	}
	store, err := storage.OpenSQLite(in.StorePath)
	if err != nil {
		return nil, contextstate.Principal{}, fmt.Errorf("open checkpoint store: %w", err)
	}
	principal := in.Principal
	if !principal.IsBound() {
		subjectID := in.SubjectID
		if subjectID == "" {
			subjectID = sess.SessionID
		}
		principal, err = contextstate.NewPrincipal(in.WorkspaceID, sess.SessionID, subjectID)
		if err != nil {
			_ = store.Close()
			return nil, contextstate.Principal{}, fmt.Errorf("mint checkpoint principal: %w", err)
		}
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
		UsageWriter:         storage.NewUsageWriter(store, principal.WorkspaceID),
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		_ = store.Close()
		return nil, contextstate.Principal{}, fmt.Errorf("set context manager: %w", err)
	}
	if err := sess.SetContextStore(store); err != nil {
		_ = store.Close()
		return nil, contextstate.Principal{}, fmt.Errorf("set context store: %w", err)
	}
	return store, principal, nil
}
