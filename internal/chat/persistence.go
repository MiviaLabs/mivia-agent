// Package chat implements multi-turn sessions with disk persistence.
package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// Session persistence constants.
const (
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
	SessionID    string    `json:"session_id,omitempty"`
	Title        string    `json:"title,omitempty"`
	Name         string    `json:"name"`
	Model        string    `json:"model"`
	Provider     string    `json:"provider"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	TurnCount    int       `json:"turn_count"`
	TokenCount   int       `json:"token_count"`
	ChunkCount   int       `json:"chunk_count"`
	MessageCount int       `json:"message_count"`
	// Dir is the absolute directory the session was created or used in.
	Dir string `json:"dir,omitempty"`
	// Worktree is the mivia worktree name when Dir lies inside one.
	Worktree string `json:"worktree,omitempty"`
	// WorktreeRoute starts a new chat session in Dir. It has no transcript.
	WorktreeRoute bool `json:"worktree_route,omitempty"`
	// WorktreeInstance retains the exact managed worktree for picker actions.
	WorktreeInstance contextstate.WorktreeInstance `json:"worktree_instance,omitempty"`
}

// Reference returns the durable ID when it exists, else the legacy save name.
func (s SessionInfo) Reference() string {
	if s.SessionID != "" {
		return s.SessionID
	}
	return s.Name
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
	// Dir is the absolute directory the session was created or used in.
	Dir string `json:"dir,omitempty"`
	// Worktree is the mivia worktree name when Dir lies inside one.
	Worktree string `json:"worktree,omitempty"`
	// ToolAdmission is the deferred-tool admitted set for this snapshot (plan
	// tools/05 D3). Absent on sessions that admitted nothing.
	ToolAdmission *contextstate.SessionAdmission `json:"tool_admission,omitempty"`
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
	if s.ContextEnabled() {
		name = sanitizeSessionName(name)
		s.mu.Lock()
		s.captureBindingLocked()
		msgs := cloneContextMessages(s.Messages)
		selection := s.binding
		s.mu.Unlock()
		return s.saveContextSession(name, msgs, selection)
	}
	name = sanitizeSessionName(name)
	if s.SessionDir == "" && s.sessionStore == nil {
		return fmt.Errorf("session directory not set")
	}

	// Snapshot messages and model under lock, copied so I/O is lock-free.
	s.mu.Lock()
	s.captureBindingLocked()
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	selection := s.binding
	s.mu.Unlock()

	// If a session store is wired, delegate to it.
	if s.sessionStore != nil {
		if err := s.sessionStore.Save(name, msgs, selection.Model, selection.ProviderName); err != nil {
			return err
		}
		return s.persistAdmission(name)
	}

	// Fallback: direct file I/O for backward compat.
	return s.saveToSessionDir(name, msgs, selection)
}

// saveToSessionDir writes the transcript chunks and metadata directly under
// SessionDir (the legacy path used when no session store is wired).
func (s *Session) saveToSessionDir(name string, msgs []provider.Message, selection ModelBinding) error {
	dir := filepath.Join(s.SessionDir, name)
	// The directory the session lives in is the process working directory,
	// not the session storage directory. Both names are needed in this
	// function, so they stay distinct.
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
		Model:        selection.Model,
		Provider:     selection.ProviderName,
		CreatedAt:    createdAt,
		UpdatedAt:    time.Now(),
		TurnCount:    turnCount,
		TokenCount:   provider.MessagesTokens(msgs),
		ChunkCount:   chunkCount,
		MessageCount: len(msgs),
		Dir:          ctxDir,
		Worktree:     ctxWorktree,
	}
	if record := s.admissionRecord(); len(record.Names) > 0 {
		meta.ToolAdmission = &record
	}

	if err := writeMetaJSON(dir, meta); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	// Chunks at or beyond the newly committed count are stale (a larger
	// previous snapshot, or an emptied session). Remove them only now that
	// meta.json references the new count, so a failed save never leaves
	// meta.json pointing at deleted chunk files.
	removeStaleChunkFiles(dir, chunkCount)

	return nil
}

// Load replaces this session's history, binding and tool surface with a saved
// snapshot's. It is a surface mutation and takes the same kind of exclusion the
// other ones do: no turn may be running when it starts, and none may start
// while it runs. Without it the admission replay below - which clears the
// admitted set and then decides from a snapshot taken outside the lock that
// guards the surface - raced a live turn's own publication and wrote a stale
// decision over it, leaving the registry advertising tools the session neither
// reports nor persists (plan tools/05).
func (s *Session) Load(name string) error {
	release, err := s.BeginSessionLoad()
	if err != nil {
		return err
	}
	defer release()
	return s.loadReserved(name, false)
}

// LoadReadOnly loads a saved session's messages for display only - the
// context-catalog counterpart of Load for a caller (a "sessions show"
// reader) that will never issue a turn against this Session and so must
// never take on any of Load's durable side effects: reclaiming the loaded
// session's write ownership, or publishing/advancing a live model binding
// for a provider/model this process has no working completer for. See
// loadContextCatalog's readOnly parameter for what specifically changes.
func (s *Session) LoadReadOnly(name string) error {
	release, err := s.BeginSessionLoad()
	if err != nil {
		return err
	}
	defer release()
	return s.loadReserved(name, true)
}

// loadReserved performs the load with the session already reserved.
func (s *Session) loadReserved(name string, readOnly bool) error {
	if s.ContextEnabled() {
		resolved := sanitizeSessionName(name)
		isContextSession, err := s.loadContextCatalog(resolved, readOnly)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.loadedContextSession = isContextSession
		s.mu.Unlock()
		// Replay the admitted tool surface synchronously, before this session
		// can issue its first request (plan tools/05 D3/R2-3).
		s.replayAdmission(resolved)
		return nil
	}
	name = sanitizeSessionName(name)
	s.mu.Lock()
	s.loadedContextSession = false
	s.mu.Unlock()
	if s.SessionDir == "" && s.sessionStore == nil {
		return fmt.Errorf("session directory not set")
	}
	var err error
	if s.sessionStore != nil {
		err = s.loadFromStore(name)
	} else {
		err = s.loadFromFiles(name)
	}
	if err != nil {
		return err
	}
	s.replayAdmission(name)
	return nil
}

func (s *Session) loadFromStore(name string) error {
	token := s.captureOperationToken("load:" + name)
	msgs, info, err := s.sessionStore.LoadWithInfo(name)
	if err != nil {
		return fmt.Errorf("load session %q: %w", name, err)
	}
	factory := s.bindingFactorySnapshot()
	if factory == nil {
		return s.publishLoadedMessages(token, msgs, info.Model)
	}
	binding, err := factory(info.Provider, info.Model)
	if err != nil {
		return fmt.Errorf("prepare session binding: %w", err)
	}
	return s.publishLoadedSession(token, binding, msgs, nil)
}

func (s *Session) loadFromFiles(name string) error {
	token := s.captureOperationToken("load:" + name)
	dir := filepath.Join(s.SessionDir, name)
	ioLock := sessionIOLock(dir)
	ioLock.RLock()
	defer ioLock.RUnlock()
	meta, err := readMetaJSON(dir)
	if err != nil {
		return fmt.Errorf("session %q: %w", name, err)
	}
	var msgs []provider.Message
	for i := 0; i < meta.ChunkCount; i++ {
		chunkPath := filepath.Join(dir, fmt.Sprintf(chunkFileName, i))
		chunkMsgs, readErr := readJSONL(chunkPath)
		if readErr != nil {
			return fmt.Errorf("read chunk %d: %w", i, readErr)
		}
		msgs = append(msgs, chunkMsgs...)
	}
	factory := s.bindingFactorySnapshot()
	if factory == nil {
		return s.publishLoadedMessages(token, msgs, meta.Model)
	}
	binding, err := factory(meta.Provider, meta.Model)
	if err != nil {
		return fmt.Errorf("prepare session binding: %w", err)
	}
	return s.publishLoadedSession(token, binding, msgs, nil)
}

func (s *Session) bindingFactorySnapshot() func(string, string) (ModelBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bindingFactory
}

// publishLoadedSession publishes binding/msgs into memory without touching
// any durable context-store CAS - the caller's row already reflects this
// exact binding (a loaded session republishing its own saved state, not a
// live model switch), so there is nothing to advance.
//
// generation pins the published binding's ModelGeneration when the caller
// already knows the durable value it must match (e.g. a reclaimed context
// session's own row - see context_catalog.go's loadContextCatalog); nil
// falls back to the original "one past whatever this process already had"
// numbering, correct only when nothing durable needs to agree with it (the
// non-context-catalog, local-file session loader's case).
func (s *Session) publishLoadedSession(token OperationToken, binding ModelBinding, msgs []provider.Message, generation *uint64) error {
	s.mu.Lock()
	if !s.tokenCurrentLocked(token) {
		s.mu.Unlock()
		closeUnpublishedDispatcher(binding.Dispatcher, nil)
		return ErrStaleOperation
	}
	if s.activeTurns > 0 || !s.bindingAllowsLocked(binding.ProviderName, binding.Model) {
		s.mu.Unlock()
		if binding.Dispatcher != nil {
			binding.Dispatcher.Close()
		}
		return fmt.Errorf("saved provider/model is not configured or session is busy")
	}
	binding.RequestedPromptTokens = s.requestedPromptCap
	binding.PromptBudgetTokens = promptBudget(binding.Profile, s.MaxTokens, s.operatorPromptCap, s.requestedPromptCap)
	if binding.PromptBudgetTokens <= 0 {
		s.mu.Unlock()
		if binding.Dispatcher != nil {
			binding.Dispatcher.Close()
		}
		return fmt.Errorf("saved provider/model has no usable prompt budget")
	}
	if generation != nil {
		binding.ModelGeneration = *generation
	} else {
		binding.ModelGeneration = s.binding.ModelGeneration + 1
	}
	outgoing := s.prefixIdentity
	old := s.publishBindingLocked(binding)
	s.invalidateLocked()
	s.turnID++
	s.Messages = provider.RepairToolPairing(msgs)
	// A loaded session republishes the binding: recapture and emit exactly
	// one reset so the restore is observable and the cache stays fresh
	// (audit RC-1, INV-68-2).
	incoming := s.capturePrefixIdentityLocked()
	s.prefixIdentity = incoming
	reset := s.buildPrefixResetLocked(outgoing, incoming, false)
	s.mu.Unlock()
	if old.Dispatcher != nil && old.Dispatcher != binding.Dispatcher {
		old.Dispatcher.Close()
	}
	publishPrefixResetEvent(s.EventBus, s.SessionID, reset)
	return nil
}

func (s *Session) publishLoadedMessages(token OperationToken, msgs []provider.Message, model string) error {
	s.mu.Lock()
	if !s.tokenCurrentLocked(token) {
		s.mu.Unlock()
		return ErrStaleOperation
	}
	if len(s.catalog) > 0 {
		s.mu.Unlock()
		return fmt.Errorf("session binding factory is required for configured model catalogs")
	}
	s.turnID++
	s.invalidateLocked()
	s.Messages = provider.RepairToolPairing(msgs)
	previousModel := s.binding.Model
	outgoing := s.prefixIdentity
	s.restoreModelLocked(model)
	if s.binding.Model != previousModel {
		s.binding.ModelGeneration++
	}
	// A restored model is wire-affecting: recapture and emit exactly one
	// reset when the model changed (audit RC-1, INV-68-2).
	incoming := s.capturePrefixIdentityLocked()
	s.prefixIdentity = incoming
	reset := s.buildPrefixResetLocked(outgoing, incoming, false)
	s.mu.Unlock()
	publishPrefixResetEvent(s.EventBus, s.SessionID, reset)
	return nil
}
