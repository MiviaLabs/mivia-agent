package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// ListSessions returns metadata for all saved sessions, sorted by most recently updated.
func (s *Session) ListSessions() ([]SessionInfo, error) {
	if catalog, principal, ok := s.contextCatalogState(); ok {
		infos, err := catalog.ListSessions(context.Background(), principal)
		if err != nil {
			return nil, err
		}
		out := make([]SessionInfo, 0, len(infos))
		for _, info := range infos {
			out = append(out, sessionInfoFromCatalog(info))
		}
		return out, nil
	}
	if s.SessionDir == "" && s.sessionStore == nil {
		return nil, fmt.Errorf("session directory not set")
	}
	if s.sessionStore != nil {
		return s.sessionStore.List()
	}
	entries, err := os.ReadDir(s.SessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var infos []SessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(s.SessionDir, e.Name(), metaFileName)
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			continue
		}
		meta, err := readMetaJSON(filepath.Join(s.SessionDir, e.Name()))
		if err != nil {
			continue
		}
		infos = append(infos, SessionInfo{Name: meta.Name, Model: meta.Model, Provider: meta.Provider, CreatedAt: meta.CreatedAt, UpdatedAt: meta.UpdatedAt, TurnCount: meta.TurnCount, TokenCount: meta.TokenCount, ChunkCount: meta.ChunkCount, MessageCount: meta.MessageCount})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].UpdatedAt.After(infos[j].UpdatedAt) })
	return infos, nil
}

// DeleteSession removes a saved session.
func (s *Session) DeleteSession(name string) error {
	if s.ContextEnabled() {
		catalog, principal, ok := s.contextCatalogState()
		if !ok {
			return fmt.Errorf("context session catalog is not configured")
		}
		return catalog.DeleteSessionSnapshot(context.Background(), principal, sanitizeSessionName(name))
	}
	name = sanitizeSessionName(name)
	if s.SessionDir == "" && s.sessionStore == nil {
		return fmt.Errorf("session directory not set")
	}
	if s.sessionStore != nil {
		return s.sessionStore.Delete(name)
	}
	dir := filepath.Join(s.SessionDir, name)
	ioLock := sessionIOLock(dir)
	ioLock.Lock()
	defer ioLock.Unlock()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("session %q not found", name)
	}
	return os.RemoveAll(dir)
}

// SaveLast saves the session as auto-save on exit and prunes old auto-saves.
func (s *Session) SaveLast() error {
	if s.ContextEnabled() {
		return nil
	}
	if s.SessionDir == "" {
		return nil
	}
	s.mu.Lock()
	s.captureBindingLocked()
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	selection := s.binding
	hasContent := len(msgs) > 1
	s.mu.Unlock()
	if !hasContent {
		return nil
	}
	if s.saveManager != nil {
		return s.saveManager.SaveOnExitWithSelection(msgs, selection.ProviderName, selection.Model)
	}
	name := uniqAutoSaveName(s.SessionDir, "")
	if err := s.Save(name); err != nil {
		return err
	}
	s.pruneAutoSaves()
	return nil
}
