package chat

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// ListSessions returns metadata for all saved sessions, sorted by most recently updated.
func (s *Session) ListSessions() ([]SessionInfo, error) {
	catalog, principal, ok := s.contextCatalogState()
	if !ok {
		return nil, fmt.Errorf("context session catalog is not configured")
	}
	s.mu.RLock()
	instance := s.contextWorktree
	s.mu.RUnlock()
	var infos []contextstate.SessionCatalogInfo
	var err error
	if !instance.IsZero() {
		scoped, scopedOK := catalog.(contextstate.WorktreeSessionCatalog)
		if !scopedOK {
			return nil, fmt.Errorf("worktree session catalog is not configured")
		}
		infos, err = scoped.ListWorktreeSessions(context.Background(), principal, instance)
	} else {
		infos, err = catalog.ListSessions(context.Background(), principal)
	}
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, sessionInfoFromCatalog(info))
	}
	fillSessionTitles(context.Background(), catalog, principal, out)
	return out, nil
}

// DeleteSession removes a saved session.
func (s *Session) DeleteSession(name string) error {
	catalog, principal, ok := s.contextCatalogState()
	if !ok {
		return fmt.Errorf("context session catalog is not configured")
	}
	s.mu.RLock()
	instance := s.contextWorktree
	s.mu.RUnlock()
	if !instance.IsZero() {
		scoped, scopedOK := catalog.(contextstate.WorktreeSessionCatalog)
		if !scopedOK {
			return fmt.Errorf("worktree session catalog is not configured")
		}
		return scoped.DeleteWorktreeSessionSnapshot(context.Background(), principal, sanitizeSessionName(name), instance)
	}
	return catalog.DeleteSessionSnapshot(context.Background(), principal, sanitizeSessionName(name))
}

// SaveLast is a permanent no-op: the legacy file-store's own auto-save-on-exit
// mechanism (SaveManager) is gone, and context-enabled sessions - the only
// kind that exist in production, since SetContextManager is always called -
// commit durably to the context catalog as each turn happens, with no
// separate "save on exit" step. Kept as a stable, harmless call for its
// existing callers (internal/uiadapter/runner.go, internal/clichat/chat_repl.go).
func (s *Session) SaveLast() error {
	return nil
}
