// Package chat implements multi-turn sessions with disk persistence.
package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// Session persistence constants.
const (
	// ChunkMessageThreshold is the max messages per chunk file.
	// When saving, if messages exceed this, we split into multiple
	// chunk_XXXX.jsonl files for efficient storage and loading.
	ChunkMessageThreshold = 500

	// chunkFilePattern is the glob pattern for chunk files.
	chunkFilePattern = "chunk_*.jsonl"

	// chunkFileName formats a chunk file name by index.
	chunkFileName = "chunk_%04d.jsonl"

	// metaFileName is the metadata file inside a session directory.
	metaFileName = "meta.json"

	// AutoSaveName is the reserved name prefix for auto-save on exit.
	AutoSaveName = "__last__"

	// AutoSaveKeep is the maximum number of auto-saved sessions to retain.
	// Older auto-saves beyond this count are pruned on each exit.
	// Set high to prevent silent data loss across many sessions.
	AutoSaveKeep = 50

	// TurnSaveKeep is the maximum number of per-turn crash-recovery snapshots
	// to retain. Turn snapshots exist only so an unexpected kill does not lose
	// the current conversation, and each holds a full transcript copy, so the
	// budget is far smaller than AutoSaveKeep. Without a budget they were never
	// pruned at all: one directory per turn, forever.
	TurnSaveKeep = 5

	// turnSaveMarker distinguishes a per-turn crash-recovery snapshot from an
	// exit auto-save. It is embedded in the directory name.
	turnSaveMarker = "_turn_"

	// autoSaveTimeFormat is the timestamp suffix appended to AutoSaveName.
	// Includes milliseconds for uniqueness across rapid exits.
	autoSaveTimeFormat = "20060102T150405.000"

	// autoSaveLegacyTimeFormat is the second-precision stamp used before
	// milliseconds were added. Still on disk in existing workspaces.
	autoSaveLegacyTimeFormat = "20060102T150405"
)

// sessionIOLocks prevents readers and writers of the same session directory
// from colliding on platforms whose rename semantics reject replacing an open
// file (notably Windows). Different session directories remain concurrent.
var sessionIOLocks sync.Map // map[string]*sync.RWMutex

func sessionIOLock(dir string) *sync.RWMutex {
	lock, _ := sessionIOLocks.LoadOrStore(filepath.Clean(dir), &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

// cleanupSessionIOLock removes the I/O lock for a session directory.
// Called after session deletion to prevent unbounded sync.Map growth.
func cleanupSessionIOLock(dir string) {
	sessionIOLocks.Delete(filepath.Clean(dir))
}

// SessionInfo is the public metadata for a saved session.
type SessionInfo struct {
	Name         string    `json:"name"`
	Model        string    `json:"model"`
	Provider     string    `json:"provider"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	TurnCount    int       `json:"turn_count"`
	TokenCount   int       `json:"token_count"`
	ChunkCount   int       `json:"chunk_count"`
	MessageCount int       `json:"message_count"`
}

// sessionMeta is the on-disk metadata shape (extensible).
type sessionMeta struct {
	Name         string    `json:"name"`
	Model        string    `json:"model"`
	Provider     string    `json:"provider"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	TurnCount    int       `json:"turn_count"`
	TokenCount   int       `json:"token_count"`
	ChunkCount   int       `json:"chunk_count"`
	MessageCount int       `json:"message_count"`
}

// --- Session methods ---

// sanitizeSessionName prevents path traversal and encoding issues.
func sanitizeSessionName(name string) string {
	name = strings.TrimSpace(name)
	// Remove null bytes first.
	name = strings.ReplaceAll(name, "\x00", "")
	// Replace ".." before "/" to catch parent-refs.
	name = strings.ReplaceAll(name, "..", "_")
	// Replace path separators and other dangerous chars.
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, ":", "_")
	if name == "" || name == "." || name == "_" {
		return "unnamed"
	}
	return name
}

// Save persists the current session to disk under the given name.
// If messages exceed ChunkMessageThreshold, they are split into
// multiple chunk_XXXX.jsonl files. The metadata is written atomically.
//
// Concurrency safety: Save snapshots s.Messages under the internal
// mutex for the minimal time needed to copy them, then releases the
// lock for all file I/O. This prevents data races with any concurrent
// mutation of s.Messages (e.g. from SendUser) while also never
// blocking the session during disk operations.
func (s *Session) Save(name string) error {
	name = sanitizeSessionName(name)
	if s.SessionDir == "" && s.sessionStore == nil {
		return fmt.Errorf("session directory not set")
	}

	// Snapshot messages and model under lock, copied so I/O is lock-free.
	s.mu.Lock()
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	model := s.model
	providerName := s.Completer.Name()
	s.mu.Unlock()

	// If a session store is wired, delegate to it.
	if s.sessionStore != nil {
		return s.sessionStore.Save(name, msgs, model, providerName)
	}

	// Fallback: direct file I/O for backward compat.
	dir := filepath.Join(s.SessionDir, name)
	ioLock := sessionIOLock(dir)
	ioLock.Lock()
	defer ioLock.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	chunkCount, err := writeSessionChunks(dir, msgs)
	if err != nil {
		return err
	}

	// Count user turns (actual conversational turns).
	turnCount := 0
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			turnCount++
		}
	}

	// Build metadata. Preserve original CreatedAt if this is a re-save.
	createdAt := time.Now()
	if existingMeta, err := readMetaJSON(dir); err == nil {
		createdAt = existingMeta.CreatedAt
	}

	meta := sessionMeta{
		Name:         name,
		Model:        model,
		Provider:     providerName,
		CreatedAt:    createdAt,
		UpdatedAt:    time.Now(),
		TurnCount:    turnCount,
		TokenCount:   provider.MessagesTokens(msgs),
		ChunkCount:   chunkCount,
		MessageCount: len(msgs),
	}

	if err := writeMetaJSON(dir, meta); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	return nil
}

func writeSessionChunks(dir string, msgs []provider.Message) (int, error) {
	count := chunkCountFor(len(msgs))
	if count == 0 {
		// Remove any pre-existing chunks from previous saves.
		if oldChunks, err := filepath.Glob(filepath.Join(dir, chunkFilePattern)); err == nil {
			for _, f := range oldChunks {
				_ = os.Remove(f)
			}
		}
		return 0, nil
	}
	// Stage every chunk first, then swap them in. Deleting the old chunks up
	// front (as this did) means any mid-write failure leaves meta.json pointing
	// at files that no longer exist — an unloadable session — and truncating a
	// chunk in place leaves a readable prefix whose trailing tool results are
	// gone, which the API rejects on every later turn.
	staged := make([]string, 0, count)
	defer func() {
		for _, tmp := range staged {
			_ = os.Remove(tmp) // no-op once renamed
		}
	}()
	for i := 0; i < count; i++ {
		start, end := i*ChunkMessageThreshold, (i+1)*ChunkMessageThreshold
		if end > len(msgs) {
			end = len(msgs)
		}
		tmp := filepath.Join(dir, fmt.Sprintf(chunkFileName, i)) + ".tmp"
		if err := writeJSONL(tmp, msgs[start:end]); err != nil {
			return 0, fmt.Errorf("write chunk %d: %w", i, err)
		}
		staged = append(staged, tmp)
	}
	if oldChunks, err := filepath.Glob(filepath.Join(dir, chunkFilePattern)); err == nil {
		for _, f := range oldChunks {
			_ = os.Remove(f)
		}
	}
	for i, tmp := range staged {
		if err := os.Rename(tmp, filepath.Join(dir, fmt.Sprintf(chunkFileName, i))); err != nil {
			return 0, fmt.Errorf("commit chunk %d: %w", i, err)
		}
	}
	return count, nil
}

func chunkCountFor(n int) int {
	if n <= 0 {
		return 0
	}
	if n <= ChunkMessageThreshold {
		return 1
	}
	return (n + ChunkMessageThreshold - 1) / ChunkMessageThreshold
}

// Load restores session messages from disk. Replaces current messages.
// The system prompt from the saved session is restored as-is.
//
// Concurrency safety: file I/O happens without the lock. The lock is
// only held to assign the final result to s.Messages and s.model.
func (s *Session) Load(name string) error {
	name = sanitizeSessionName(name)
	if s.SessionDir == "" && s.sessionStore == nil {
		return fmt.Errorf("session directory not set")
	}

	// If a session store is wired, delegate.
	if s.sessionStore != nil {
		msgs, info, err := s.sessionStore.LoadWithInfo(name)
		if err != nil {
			return fmt.Errorf("load session %q: %w", name, err)
		}
		s.mu.Lock()
		// See resetSystem: a wholesale replacement must invalidate in-flight
		// turns, or a stale writeback reverts the load.
		s.turnID++
		s.Messages = provider.RepairToolPairing(msgs)
		s.restoreModelLocked(info.Model)
		s.mu.Unlock()
		return nil
	}

	// Fallback: direct I/O.
	dir := filepath.Join(s.SessionDir, name)
	ioLock := sessionIOLock(dir)
	ioLock.RLock()
	defer ioLock.RUnlock()
	meta, err := readMetaJSON(dir)
	if err != nil {
		return fmt.Errorf("session %q: %w", name, err)
	}

	// Read all chunk files in order.
	var msgs []provider.Message
	for i := 0; i < meta.ChunkCount; i++ {
		chunkPath := filepath.Join(dir, fmt.Sprintf(chunkFileName, i))
		chunkMsgs, err := readJSONL(chunkPath)
		if err != nil {
			return fmt.Errorf("read chunk %d: %w", i, err)
		}
		msgs = append(msgs, chunkMsgs...)
	}

	// Now lock briefly to assign.
	s.mu.Lock()
	s.turnID++
	s.restoreModelLocked(meta.Model)
	s.Messages = provider.RepairToolPairing(msgs)
	s.mu.Unlock()

	return nil
}

// ListSessions returns metadata for all saved sessions, sorted by most recently updated.
func (s *Session) ListSessions() ([]SessionInfo, error) {
	if s.SessionDir == "" && s.sessionStore == nil {
		return nil, fmt.Errorf("session directory not set")
	}

	if s.sessionStore != nil {
		return s.sessionStore.List()
	}

	// Fallback: direct I/O.
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
		// Skip directories that don't have meta.json (not valid sessions).
		metaPath := filepath.Join(s.SessionDir, e.Name(), metaFileName)
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			continue
		}
		meta, err := readMetaJSON(filepath.Join(s.SessionDir, e.Name()))
		if err != nil {
			continue // skip corrupt sessions gracefully
		}
		infos = append(infos, SessionInfo{
			Name:         meta.Name,
			Model:        meta.Model,
			Provider:     meta.Provider,
			CreatedAt:    meta.CreatedAt,
			UpdatedAt:    meta.UpdatedAt,
			TurnCount:    meta.TurnCount,
			TokenCount:   meta.TokenCount,
			ChunkCount:   meta.ChunkCount,
			MessageCount: meta.MessageCount,
		})
	}

	// Sort by most recently updated first.
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})

	return infos, nil
}

// DeleteSession removes a saved session directory from disk.
func (s *Session) DeleteSession(name string) error {
	name = sanitizeSessionName(name)
	if s.SessionDir == "" && s.sessionStore == nil {
		return fmt.Errorf("session directory not set")
	}

	if s.sessionStore != nil {
		return s.sessionStore.Delete(name)
	}

	// Fallback: direct I/O.
	dir := filepath.Join(s.SessionDir, name)
	ioLock := sessionIOLock(dir)
	ioLock.Lock()
	defer ioLock.Unlock()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("session %q not found", name)
	}

	return os.RemoveAll(dir)
}

// SaveLast saves the session as auto-save on exit; prunes old auto-saves.
// Skips if session has no meaningful history or no session dir.
func (s *Session) SaveLast() error {
	if s.SessionDir == "" {
		return nil // silently skip if no persistence configured
	}
	// Only save if there are messages beyond the initial system prompt.
	s.mu.RLock()
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	model := s.model
	hasContent := len(msgs) > 1
	s.mu.RUnlock()
	if !hasContent {
		return nil
	}

	// If a SaveManager is wired, delegate to it.
	if s.saveManager != nil {
		return s.saveManager.SaveOnExitWithModel(msgs, model)
	}

	// Fallback: direct save via SessionDir (backward compat for unwired sessions).
	name := uniqAutoSaveName(s.SessionDir, "")
	if err := s.Save(name); err != nil {
		return err
	}
	s.pruneAutoSaves()
	return nil
}
