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

func catalogMessages(msgs []provider.Message) ([]byte, error) {
	return contextstate.MarshalCanonical(msgs)
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
		TokenCount: info.TokenCount, MessageCount: info.MessageCount, ChunkCount: 1}
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
	return catalog.SaveSession(context.Background(), principal, name, data, selection.Model, selection.ProviderName, turns, provider.MessagesTokens(msgs), len(msgs))
}

func (s *Session) loadContextCatalog(name string) error {
	catalog, principal, ok := s.contextCatalogState()
	if !ok {
		return fmt.Errorf("context session catalog is not configured")
	}
	data, info, err := catalog.LoadSession(context.Background(), principal, name)
	if err != nil {
		return fmt.Errorf("load session %q: %w", name, err)
	}
	msgs, err := decodeCatalogMessages(data)
	if err != nil {
		return err
	}
	factory := s.bindingFactorySnapshot()
	if factory != nil {
		selection := s.CurrentSelection()
		if selection.ProviderName == info.Provider && selection.Model == info.Model {
			token := s.captureOperationToken("catalog-load:" + name)
			return s.adoptLoadedMessages(token, msgs)
		}
		binding, err := factory(info.Provider, info.Model)
		if err != nil {
			return fmt.Errorf("prepare session binding: %w", err)
		}
		if err := s.SwitchBinding(binding); err != nil {
			return err
		}
		return s.adoptLoadedMessages(s.captureOperationToken("catalog-load:"+name), msgs)
	}
	token := s.captureOperationToken("catalog-load:" + name)
	return s.publishLoadedMessages(token, msgs, info.Model)
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
