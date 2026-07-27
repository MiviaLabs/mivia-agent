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

	// autoSaveTimeFormat is the timestamp suffix appended to AutoSaveName.
	// Includes milliseconds for uniqueness across rapid exits.
	autoSaveTimeFormat = "20060102T150405.000"
)

// sessionIOLocks prevents readers and writers of the same session directory
// from colliding on platforms whose rename semantics reject replacing an open
// file (notably Windows). Different session directories remain concurrent.
var sessionIOLocks sync.Map // map[string]*sync.RWMutex

func sessionIOLock(dir string) *sync.RWMutex {
	lock, _ := sessionIOLocks.LoadOrStore(filepath.Clean(dir), &sync.RWMutex{})
	return lock.(*sync.RWMutex)
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
	if s.SessionDir == "" {
		return fmt.Errorf("session directory not set")
	}

	// Snapshot messages and model under lock, copied so I/O is lock-free.
	s.mu.Lock()
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	model := s.Model
	providerName := s.Completer.Name()
	s.mu.Unlock()

	// Everything below is pure file I/O — no lock held.
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
	if oldChunks, err := filepath.Glob(filepath.Join(dir, chunkFilePattern)); err == nil {
		for _, f := range oldChunks {
			_ = os.Remove(f)
		}
	}
	for i := 0; i < count; i++ {
		start, end := i*ChunkMessageThreshold, (i+1)*ChunkMessageThreshold
		if end > len(msgs) {
			end = len(msgs)
		}
		if err := writeJSONL(filepath.Join(dir, fmt.Sprintf(chunkFileName, i)), msgs[start:end]); err != nil {
			return 0, fmt.Errorf("write chunk %d: %w", i, err)
		}
	}
	return count, nil
}

func chunkCountFor(n int) int {
	if n <= ChunkMessageThreshold {
		return 1
	}
	return (n + ChunkMessageThreshold - 1) / ChunkMessageThreshold
}

// Load restores session messages from disk. Replaces current messages.
// The system prompt from the saved session is restored as-is.
//
// Concurrency safety: file I/O happens without the lock. The lock is
// only held to assign the final result to s.Messages and s.Model.
func (s *Session) Load(name string) error {
	name = sanitizeSessionName(name)
	if s.SessionDir == "" {
		return fmt.Errorf("session directory not set")
	}

	// Read metadata and chunk files without holding the lock.
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
	s.Model = meta.Model
	s.Messages = msgs
	s.mu.Unlock()

	return nil
}

// ListSessions returns metadata for all saved sessions, sorted by most recently updated.
func (s *Session) ListSessions() ([]SessionInfo, error) {
	if s.SessionDir == "" {
		return nil, fmt.Errorf("session directory not set")
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
	if s.SessionDir == "" {
		return fmt.Errorf("session directory not set")
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

// HasAutoSave checks whether any auto-saved session exists on disk.
func (s *Session) HasAutoSave() bool {
	if s.SessionDir == "" {
		return false
	}
	infos, err := s.ListSessions()
	if err != nil {
		return false
	}
	for _, si := range infos {
		if IsAutoSaveName(si.Name) {
			return true
		}
	}
	return false
}

// IsAutoSaveName reports whether name matches the auto-save prefix.
func IsAutoSaveName(name string) bool {
	return strings.HasPrefix(name, AutoSaveName) && len(name) >= len(AutoSaveName)
}

// LatestAutoSaveName returns the name of the most recent auto-save session,
// or empty string if none exist. The bare __last__ name is returned as-is for
// backward compatibility with pre-rolling-save sessions.
func (s *Session) LatestAutoSaveName() string {
	infos, err := s.ListSessions()
	if err != nil {
		return ""
	}
	latest := ""
	var latestTime time.Time
	for _, si := range infos {
		if !IsAutoSaveName(si.Name) {
			continue
		}
		if latest == "" || si.UpdatedAt.After(latestTime) {
			latest = si.Name
			latestTime = si.UpdatedAt
		}
	}
	return latest
}

// pruneAutoSaves removes orphaned auto-saves beyond AutoSaveKeep.
func (s *Session) pruneAutoSaves() {
	if s.SessionDir == "" {
		return
	}
	cleanupOrphanedSessions(s.SessionDir)

	infos, err := s.ListSessions()
	if err != nil {
		return
	}
	var autoInfos []SessionInfo
	for _, si := range infos {
		if IsAutoSaveName(si.Name) {
			autoInfos = append(autoInfos, si)
		}
	}
	// ListSessions returns most-recent first; the tail is the oldest.
	if len(autoInfos) <= AutoSaveKeep {
		return
	}
	toDelete := autoInfos[AutoSaveKeep:] // oldest entries
	for _, si := range toDelete {
		_ = s.DeleteSession(si.Name)
	}
}

// SaveLast saves the session as auto-save on exit; prunes old auto-saves.
// Skips if session has no meaningful history or no session dir.
func (s *Session) SaveLast() error {
	if s.SessionDir == "" {
		return nil // silently skip if no persistence configured
	}
	// Only save if there are messages beyond the initial system prompt.
	s.mu.Lock()
	hasContent := len(s.Messages) > 1
	s.mu.Unlock()
	if !hasContent {
		return nil
	}
	// Unique name without sleeping: timestamp + numeric suffix if the path exists.
	base := AutoSaveName + time.Now().Format(autoSaveTimeFormat)
	name := base
	for i := 0; i < 1000; i++ {
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Stat(filepath.Join(s.SessionDir, name)); os.IsNotExist(err) {
			break
		}
	}
	if err := s.Save(name); err != nil {
		return err
	}
	s.pruneAutoSaves()
	return nil
}
