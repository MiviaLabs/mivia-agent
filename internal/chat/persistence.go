// Package chat implements multi-turn sessions with disk persistence.
package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// Session persistence constants.
const (
	// AutoSaveName is the reserved name prefix for auto-save on exit.
	AutoSaveName = "__last__"

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
	if !s.ContextEnabled() {
		return fmt.Errorf("context session catalog is not configured")
	}
	name = sanitizeSessionName(name)
	s.mu.Lock()
	s.captureBindingLocked()
	msgs := cloneContextMessages(s.Messages)
	selection := s.binding
	s.mu.Unlock()
	return s.saveContextSession(name, msgs, selection)
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
	if !s.ContextEnabled() {
		return fmt.Errorf("context session catalog is not configured")
	}
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
	warn := s.checkWarnUnknownModelLocked(binding.Model, binding.FallbackProfile)
	s.mu.Unlock()
	if warn && WarnUnknownContextWindow != nil {
		WarnUnknownContextWindow(binding.Model)
	}
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
