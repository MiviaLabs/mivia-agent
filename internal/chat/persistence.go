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
	"github.com/MiviaLabs/mivia-agent/internal/redact"
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

	return nil
}

// redactReasoningForPersistence returns a deep copy of msgs whose assistant
// ReasoningContent has passed through the process-wide redaction policy. It is
// applied to the bytes written to disk, never to host history: callers keep the
// raw reasoning for provider replay and only persist the redacted copy. The
// policy is read via redact.Current() semantics (redact.Text), which is an
// identity when no policy is installed, so unconfigured workspaces persist
// exactly what they always did.
func redactReasoningForPersistence(msgs []provider.Message) []provider.Message {
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		out[i].ToolCalls = append([]provider.ToolCall(nil), msgs[i].ToolCalls...)
		out[i].ReasoningContent = redact.Text(out[i].ReasoningContent)
	}
	return out
}

func writeSessionChunks(dir string, msgs []provider.Message) (int, error) {
	// The chunk bytes are durable, operator-visible state: redact reasoning
	// before it reaches the file. This covers the Session.Save file fallback
	// (and, idempotently, any store that pre-redacts and delegates here).
	msgs = redactReasoningForPersistence(msgs)
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
	// at files that no longer exist - an unloadable session - and truncating a
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
	return s.loadReserved(name)
}

// loadReserved performs the load with the session already reserved.
func (s *Session) loadReserved(name string) error {
	if s.ContextEnabled() {
		resolved := sanitizeSessionName(name)
		isContextSession, err := s.loadContextCatalog(resolved)
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
	return s.publishLoadedSession(token, binding, msgs)
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
	return s.publishLoadedSession(token, binding, msgs)
}

func (s *Session) bindingFactorySnapshot() func(string, string) (ModelBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bindingFactory
}

func (s *Session) publishLoadedSession(token OperationToken, binding ModelBinding, msgs []provider.Message) error {
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
	binding.ModelGeneration = s.binding.ModelGeneration + 1
	old := s.publishBindingLocked(binding)
	s.invalidateLocked()
	s.turnID++
	s.Messages = provider.RepairToolPairing(msgs)
	s.mu.Unlock()
	if old.Dispatcher != nil && old.Dispatcher != binding.Dispatcher {
		old.Dispatcher.Close()
	}
	return nil
}

func (s *Session) publishLoadedMessages(token OperationToken, msgs []provider.Message, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tokenCurrentLocked(token) {
		return ErrStaleOperation
	}
	if len(s.catalog) > 0 {
		return fmt.Errorf("session binding factory is required for configured model catalogs")
	}
	s.turnID++
	s.invalidateLocked()
	s.Messages = provider.RepairToolPairing(msgs)
	previousModel := s.binding.Model
	s.restoreModelLocked(model)
	if s.binding.Model != previousModel {
		s.binding.ModelGeneration++
	}
	return nil
}
