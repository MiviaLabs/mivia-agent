// Package chat implements multi-turn sessions with disk persistence.
package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// SessionStore is the persistence interface for chat sessions.
// Implementations must be safe for concurrent use.
type SessionStore interface {
	// Save persists messages under the given name.
	// If the name already exists, it is overwritten (updated_at refreshed).
	Save(name string, msgs []provider.Message, model, providerName string) error
	// Load retrieves messages previously saved under name.
	// Returns ErrSessionNotFound if the session does not exist.
	Load(name string) ([]provider.Message, error)
	// LoadWithInfo retrieves one session's messages and metadata from the same
	// persisted revision.
	LoadWithInfo(name string) ([]provider.Message, SessionInfo, error)
	// List returns metadata for all saved sessions, sorted by most recently
	// updated first. Returns an empty slice if no sessions exist.
	List() ([]SessionInfo, error)
	// Delete removes a saved session by name. Returns ErrSessionNotFound
	// if the session does not exist.
	Delete(name string) error
}

// ErrSessionNotFound is returned when a session does not exist on disk.
var ErrSessionNotFound = errors.New("session not found")

// FileSessionStore implements SessionStore on the local filesystem.
// Each session is stored as a directory under root with:
//   - chunk_XXXX.jsonl files containing JSONL-encoded messages
//   - meta.json containing session metadata
//
// Safe for concurrent use via sessionIOLocks (per-directory RWMutex).
type FileSessionStore struct {
	dir string
}

// NewFileSessionStore creates a new FileSessionStore rooted at dir.
// The directory is created if it does not exist. Returns an error if
// dir is empty or cannot be created.
func NewFileSessionStore(dir string) (*FileSessionStore, error) {
	if dir == "" {
		return nil, errors.New("session directory cannot be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	return &FileSessionStore{dir: dir}, nil
}

// compile-time check: FileSessionStore satisfies SessionStore.
var _ SessionStore = (*FileSessionStore)(nil)

// Save persists messages under the given session name.
// If messages exceed ChunkMessageThreshold, they are split into
// multiple chunk_XXXX.jsonl files. Metadata is written atomically.
// Re-saving an existing name preserves the original CreatedAt timestamp.
func (fs *FileSessionStore) Save(name string, msgs []provider.Message, model, providerName string) error {
	name = sanitizeSessionName(name)
	if name == "" {
		return errors.New("session name cannot be empty")
	}

	// Reasoning redaction for disk happens exactly once, inside
	// writeSessionChunks below (which also serves Session.Save's file
	// fallback), so every sink — this store branch, SaveManager's
	// SaveAfterTurn/SaveLast, and the fallback — applies the same pass once
	// and never mutates the caller's slice.
	dir := filepath.Join(fs.dir, name)
	// The worktree probe spawns git; capture it before taking the session I/O
	// lock so a slow git call never holds the lock for other saves to this
	// session directory.
	ctxDir, ctxWorktree := currentDirContext()
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

	// Count user turns.
	turnCount := 0
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			turnCount++
		}
	}

	// Preserve fields this write does not own if re-saving: CreatedAt, and the
	// admitted tool set, which is written by SaveAdmission through the same
	// meta.json and would otherwise be wiped by an unrelated transcript save.
	createdAt := time.Now()
	var admission *contextstate.SessionAdmission
	if existingMeta, err := readMetaJSON(dir); err == nil {
		createdAt = existingMeta.CreatedAt
		admission = existingMeta.ToolAdmission
	}
	meta := sessionMeta{
		ToolAdmission: admission,
		Name:          name,
		Model:         model,
		Provider:      providerName,
		CreatedAt:     createdAt,
		UpdatedAt:     time.Now(),
		TurnCount:     turnCount,
		TokenCount:    provider.MessagesTokens(msgs),
		ChunkCount:    chunkCount,
		MessageCount:  len(msgs),
		Dir:           ctxDir,
		Worktree:      ctxWorktree,
	}

	if err := writeMetaJSON(dir, meta); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	return nil
}

// Load retrieves messages previously saved under name.
// Returns ErrSessionNotFound if the session does not exist.
func (fs *FileSessionStore) Load(name string) ([]provider.Message, error) {
	msgs, _, err := fs.LoadWithInfo(name)
	return msgs, err
}

// LoadWithInfo retrieves one session's messages and metadata while holding the
// session directory read lock, so callers never combine different revisions.
func (fs *FileSessionStore) LoadWithInfo(name string) ([]provider.Message, SessionInfo, error) {
	name = sanitizeSessionName(name)
	dir := filepath.Join(fs.dir, name)

	ioLock := sessionIOLock(dir)
	ioLock.RLock()
	defer ioLock.RUnlock()

	// Check existence by looking for meta.json.
	meta, err := readMetaJSON(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, SessionInfo{}, fmt.Errorf("%w: %q", ErrSessionNotFound, name)
		}
		return nil, SessionInfo{}, fmt.Errorf("read meta for %q: %w", name, err)
	}

	var msgs []provider.Message
	for i := 0; i < meta.ChunkCount; i++ {
		chunkPath := filepath.Join(dir, fmt.Sprintf(chunkFileName, i))
		chunkMsgs, err := readJSONL(chunkPath)
		if err != nil {
			return nil, SessionInfo{}, fmt.Errorf("read chunk %d for %q: %w", i, name, err)
		}
		msgs = append(msgs, chunkMsgs...)
	}

	return msgs, SessionInfo{
		Name:         meta.Name,
		Model:        meta.Model,
		Provider:     meta.Provider,
		CreatedAt:    meta.CreatedAt,
		UpdatedAt:    meta.UpdatedAt,
		TurnCount:    meta.TurnCount,
		TokenCount:   meta.TokenCount,
		ChunkCount:   meta.ChunkCount,
		MessageCount: meta.MessageCount,
	}, nil
}

// List returns metadata for all saved sessions, sorted by most recently
// updated first (newest first). Returns an empty slice if no sessions exist.
// Corrupt sessions (missing meta.json) are silently skipped.
func (fs *FileSessionStore) List() ([]SessionInfo, error) {
	entries, err := os.ReadDir(fs.dir)
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
		metaPath := filepath.Join(fs.dir, e.Name(), metaFileName)
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			continue
		}
		meta, err := readMetaJSON(filepath.Join(fs.dir, e.Name()))
		if err != nil {
			continue
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
			Dir:          meta.Dir,
			Worktree:     meta.Worktree,
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})

	return infos, nil
}

// Delete removes a saved session by name. Returns ErrSessionNotFound
// if the session does not exist.
func (fs *FileSessionStore) Delete(name string) error {
	name = sanitizeSessionName(name)
	dir := filepath.Join(fs.dir, name)

	ioLock := sessionIOLock(dir)
	ioLock.Lock()
	defer ioLock.Unlock()

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %q", ErrSessionNotFound, name)
	}

	err := os.RemoveAll(dir)
	if err == nil {
		cleanupSessionIOLock(dir)
	}
	return err
}
