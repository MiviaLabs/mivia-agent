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
	return SessionInfo{Name: info.Name, Model: info.Model, Provider: info.Provider,
		CreatedAt: created, UpdatedAt: updated, TurnCount: info.TurnCount,
		TokenCount: info.TokenCount, MessageCount: info.MessageCount, ChunkCount: 1,
		Dir: info.Dir, Worktree: info.Worktree, WorktreeRoute: info.WorktreeRoute,
		WorktreeInstance: info.WorktreeInstance}
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

func (s *Session) loadContextCatalog(name string) (bool, error) {
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
	msgs, err := decodeCatalogMessages(data)
	if err != nil {
		return false, err
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
