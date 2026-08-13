package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func (s *Session) contextCatalogState() (contextstate.SessionCatalog, contextstate.Principal, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	catalog, ok := s.contextStore.(contextstate.SessionCatalog)
	return catalog, s.contextPrincipal, ok && s.contextEnabledLocked()
}

// catalogMessages builds the canonical payload that lands in the context
// catalog's chat_sessions row. It is durable, operator-visible state, so
// assistant ReasoningContent is redacted on the copy before marshaling while
// visible Content stays intact (redactReasoningForPersistence is an identity
// without an installed policy).
func catalogMessages(msgs []provider.Message) ([]byte, error) {
	return contextstate.MarshalCanonical(redactReasoningForPersistence(msgs))
}

func decodeCatalogMessages(data []byte) ([]provider.Message, error) {
	var msgs []provider.Message
	if err := contextstate.UnmarshalCanonical(data, &msgs); err != nil {
		return nil, fmt.Errorf("decode session messages: %w", err)
	}
	if err := provider.ValidateToolPairing(msgs); err != nil {
		return nil, fmt.Errorf("session message shape: %w", err)
	}
	return msgs, nil
}

func sessionInfoFromCatalog(info contextstate.SessionCatalogInfo) SessionInfo {
	created, _ := time.Parse(time.RFC3339Nano, info.CreatedAt)
	updated, _ := time.Parse(time.RFC3339Nano, info.UpdatedAt)
	return SessionInfo{SessionID: info.SessionID, Title: info.Title, Name: info.Name, Model: info.Model, Provider: info.Provider,
		CreatedAt: created, UpdatedAt: updated, TurnCount: info.TurnCount,
		TokenCount: info.TokenCount, MessageCount: info.MessageCount, ChunkCount: 1,
		Dir: info.Dir, Worktree: info.Worktree, WorktreeRoute: info.WorktreeRoute,
		WorktreeInstance: info.WorktreeInstance}
}

// SetContextSessionTitle changes display metadata for a durable context session.
func (s *Session) SetContextSessionTitle(sessionID, title string) error {
	s.mu.RLock()
	instance := s.contextWorktree
	s.mu.RUnlock()
	return s.SetContextSessionTitleInWorktree(sessionID, title, instance)
}

// SetContextSessionTitleInWorktree changes title metadata in the given worktree.
func (s *Session) SetContextSessionTitleInWorktree(sessionID, title string, instance contextstate.WorktreeInstance) error {
	catalog, principal, ok := s.contextCatalogState()
	if !ok {
		return fmt.Errorf("context session titles are not configured")
	}
	title, err := contextstate.NormalizeSessionTitle(title)
	if err != nil {
		return err
	}
	titles, ok := catalog.(contextstate.SessionTitleCatalog)
	if !ok {
		return fmt.Errorf("context session titles are not configured")
	}
	return titles.SetSessionTitle(context.Background(), principal, sessionID, title, instance)
}

func (s *Session) saveContextSession(name string, msgs []provider.Message, selection ModelBinding) error {
	catalog, principal, ok := s.contextCatalogState()
	if !ok {
		return fmt.Errorf("context session catalog is not configured")
	}
	data, err := catalogMessages(msgs)
	if err != nil {
		return err
	}
	turns := 0
	for _, msg := range msgs {
		if msg.Role == provider.RoleUser {
			turns++
		}
	}
	if err := catalog.SaveSession(context.Background(), principal, name, data, selection.Model, selection.ProviderName, turns, provider.MessagesTokens(msgs), len(msgs), s.sessionSaveOptions()); err != nil {
		return err
	}
	return s.persistAdmission(name)
}

// sessionSaveOptions captures the current directory context for a named
// snapshot save. The zero value (no directory) is valid for callers that
// cannot resolve one.
func (s *Session) sessionSaveOptions() contextstate.SessionSaveOptions {
	s.mu.RLock()
	instance := s.contextWorktree
	dir := s.contextSessionDir
	s.mu.RUnlock()
	if !instance.IsZero() {
		return contextstate.SessionSaveOptions{Dir: dir, Worktree: instance.Worktree, WorktreeInstance: instance}
	}
	dir, worktree := currentDirContext()
	return contextstate.SessionSaveOptions{Dir: dir, Worktree: worktree}
}

// loadContextCatalog loads a saved context-catalog session's messages.
//
// readOnly distinguishes a genuine resume (Load, about to keep issuing
// turns against this Session - reclaims write ownership and durably
// publishes/advances a live binding for the session's provider/model) from
// a read-only display load (LoadReadOnly - a "sessions show" reader that
// will never write again). A read-only load skips both: reclaiming
// ownership it will never use is an unwanted side effect of merely
// viewing a session, and publishing a binding through SwitchBinding
// requires a working Completer and durably advances the context store's
// binding revision - neither of which a display-only caller has any
// business doing, and mismatched with the currently-active default
// provider/model (e.g. after switching models mid-session, see
// mivia-agent-desktop's ModelSwitcherButton), it failed outright before
// this parameter existed ("incomplete model binding", then "stale
// binding: context binding changed" once a synthetic Completer worked
// around the first failure) - the raw messages were never actually lost,
// this path just couldn't reach them without behaving like a live resume.
func (s *Session) loadContextCatalog(name string, readOnly bool) (bool, error) {
	catalog, principal, ok := s.contextCatalogState()
	if !ok {
		return false, fmt.Errorf("context session catalog is not configured")
	}
	s.mu.RLock()
	instance := s.contextWorktree
	s.mu.RUnlock()
	var data []byte
	var info contextstate.SessionCatalogInfo
	var err error
	if !instance.IsZero() {
		scoped, ok := catalog.(contextstate.WorktreeSessionCatalog)
		if !ok {
			return false, fmt.Errorf("worktree session catalog is not configured")
		}
		data, info, err = scoped.LoadWorktreeSession(context.Background(), principal, name, instance)
	} else {
		data, info, err = catalog.LoadSession(context.Background(), principal, name)
	}
	if err != nil {
		return false, fmt.Errorf("load session %q: %w", name, err)
	}
	isContextSession := info.SessionID != ""
	// A loaded live context session's history now sits in memory, but every
	// commit still authorizes against s.contextPrincipal - which, until this
	// point, is still the fresh principal this process minted at startup for
	// its OWN (different, empty) session id. Left alone, the turn that just
	// loaded this history would durably commit it right back out under that
	// unrelated id: sessions list would show two records - the resumed one,
	// frozen at its pre-load state, and a new one holding the merged result -
	// instead of one record whose turn count grew. Reclaiming the loaded
	// session's ownership before adopting its messages closes that gap.
	if !readOnly && isContextSession && info.SessionID != principal.SessionID {
		if err := s.reclaimContextSession(info.SessionID); err != nil {
			return false, fmt.Errorf("resume session %q: %w", name, err)
		}
	}
	msgs, err := decodeCatalogMessages(data)
	if err != nil {
		return false, err
	}
	if readOnly {
		token := s.captureOperationToken("catalog-load:" + name)
		return isContextSession, s.adoptLoadedMessages(token, msgs)
	}
	factory := s.bindingFactorySnapshot()
	if factory != nil {
		selection := s.CurrentSelection()
		if selection.ProviderName == info.Provider && selection.Model == info.Model {
			token := s.captureOperationToken("catalog-load:" + name)
			return isContextSession, s.adoptLoadedMessages(token, msgs)
		}
		binding, err := factory(info.Provider, info.Model)
		if err != nil {
			return false, fmt.Errorf("prepare session binding: %w", err)
		}
		if err := s.SwitchBinding(binding); err != nil {
			return false, err
		}
		return isContextSession, s.adoptLoadedMessages(s.captureOperationToken("catalog-load:"+name), msgs)
	}
	token := s.captureOperationToken("catalog-load:" + name)
	return isContextSession, s.publishLoadedMessages(token, msgs, info.Model)
}

// reclaimContextSession takes over write ownership of an existing, live
// context session identified by sessionID by minting this process a fresh
// Principal for it and installing that Principal as s.contextPrincipal (with
// s.SessionID kept in lock-step, per contextEnabledLocked's invariant). The
// session's original owner capability lived only in the process that created
// it and cannot be recovered, so contextstate.SessionReclaimer performs the
// re-authorization store-side, scoped the same way LoadSession already is:
// by workspace, subject and the session's own id, not by proving the old
// capability. See contextstate.SessionReclaimer's doc comment for the full
// trust-boundary argument.
func (s *Session) reclaimContextSession(sessionID string) error {
	s.mu.RLock()
	store := s.contextStore
	workspaceID, subjectID := s.contextPrincipal.WorkspaceID, s.contextPrincipal.SubjectID
	oldSessionID := s.SessionID
	s.mu.RUnlock()
	reclaimer, ok := store.(contextstate.SessionReclaimer)
	if !ok {
		return fmt.Errorf("context store does not support resuming a live session")
	}
	principal, err := contextstate.NewPrincipal(workspaceID, sessionID, subjectID)
	if err != nil {
		return err
	}
	snapshot, err := reclaimer.ReclaimSession(context.Background(), principal, sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SessionID != oldSessionID {
		return ErrStaleOperation
	}
	s.SessionID = sessionID
	s.contextPrincipal = principal
	s.contextHead = snapshot.Revision
	return nil
}

// adoptLoadedMessages replaces history after the binding has already been
// selected. Binding publication and history publication are deliberately
// separate: SwitchBinding advances the context revision exactly once, while
// loading a snapshot must not publish the same binding a second time.
func (s *Session) adoptLoadedMessages(token OperationToken, msgs []provider.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tokenCurrentLocked(token) {
		return ErrStaleOperation
	}
	if s.activeTurns > 0 {
		return fmt.Errorf("cannot load a session while work is active")
	}
	s.turnID++
	s.invalidateLocked()
	s.Messages = provider.RepairToolPairing(msgs)
	return nil
}
