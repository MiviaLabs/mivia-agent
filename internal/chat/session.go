// Package chat implements multi-turn sessions (plain chat and agent).
package chat

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Session holds conversation history and a completer.
type Session struct {
	Completer          provider.Completer
	model              string
	allowedModels      []string
	rejectedSavedModel *string
	SystemPrompt       string
	Temperature        *float64
	MaxTokens          *int
	Messages           []provider.Message
	Tools              *tools.Registry
	// UseTools enables the agent loop when Tools is set.
	UseTools bool
	// Dispatcher is the runtime dispatcher for tool, skill, and subagent execution.
	// When set, it is passed to the agent loop for tool execution. If nil,
	// the agent loop creates a default tool-only dispatcher.
	Dispatcher *runtime.Dispatcher
	// SessionID is an unguessable principal stable for this session's lifetime.
	SessionID string
	MaxSteps  int
	// MaxToolResultChars caps each tool result stored in agent-loop history,
	// in bytes. 0 means uncapped (per-tool budgets are the bound). Set from
	// [tools] max_tool_result_bytes by NewSession.
	MaxToolResultChars int
	// MaxContextTokens sets the approximate token limit for pruning.
	// 0 means use default (75% of typical model context window).
	MaxContextTokens int
	// OnAgentEvent optional tool/step tracing.
	OnAgentEvent func(agent.Event)
	// EventBus optional extensible event delivery (TUI UIAdapter, etc.).
	// When set, the agent loop dual-publishes agent events onto this bus.
	EventBus *events.Bus
	// ToolTimeout is the default per-tool budget for tools that do not
	// declare Capability.Timeout. Zero means agent.DefaultToolTimeout (60s).
	// Long tools (run_command, dispatch_tasks, delegate) still extend via
	// Capability.Timeout regardless of this value.
	ToolTimeout time.Duration
	// SessionDir is the directory where sessions are persisted
	// (e.g., <workspace>/.mivia/sessions/). When set, enables
	// save/load/list/delete operations and auto-save on exit.
	SessionDir string
	// mu protects concurrent mutations to Messages, model, and turnID.
	// All exported methods that read or write these fields must
	// hold mu (Lock for writes, RLock for reads). Save/Load use the
	// lock-and-copy pattern so I/O happens without the lock while
	// the snapshot is safe. TUI code must use MessagesCopy() instead
	// of reading Messages directly to avoid data races.
	mu sync.RWMutex
	// turnID is incremented at the start of each SendUser turn.
	// Writeback of Messages only applies when the turn is still
	// current, so a cancelled/stale turn cannot overwrite a newer one
	// (force-send / overlapping SendUser).
	turnID uint64
	// sessionStore is the persistence backend for save/load/list/delete.
	// When nil, persistence operations return errors (graceful degradation).
	sessionStore SessionStore
	// saveManager orchestrates auto-save strategies (per-turn, exit, prune).
	// When nil, SaveAfterTurn and SaveLast are no-ops.
	saveManager *SaveManager
	// turnSaveName is the rolling per-turn snapshot directory used by the
	// unwired fallback path, mirroring SaveManager.turnSaveName. Guarded by mu.
	turnSaveName string
}

// DefaultMaxContextTokens is the default token budget for context pruning.
// DeepSeek models support up to 1M tokens; this conservative default
// allows comfortable headroom while preventing runaway context.
const (
	DefaultMaxContextTokens = 1000000
	DefaultRequestTimeout   = 300 * time.Second

	// DefaultMaxSteps bounds one interactive turn's agent loop. Leaving this
	// at 0 (unlimited) meant a model stuck emitting tool calls burned tokens
	// until the user hit Ctrl-C. 100 matches the nested-subagent step budget
	// and is far above any legitimate interactive turn; /steps raises or
	// removes it per session.
	DefaultMaxSteps = 100
)

// resolvedMaxSteps honours a configured [chat] max_steps, including an explicit
// 0 (unlimited). Only an unset key falls back to the default, which is why the
// config field is a pointer.
func resolvedMaxSteps(res *config.Resolved) int {
	if res.MaxSteps != nil {
		return *res.MaxSteps
	}
	return DefaultMaxSteps
}

// NewSession builds a session from resolved config and completer.
func NewSession(res *config.Resolved, c provider.Completer) *Session {
	ctxBudget := res.MaxContextTokens
	if ctxBudget <= 0 {
		ctxBudget = DefaultMaxContextTokens
	}
	s := &Session{
		Completer:        c,
		model:            res.Model,
		allowedModels:    slices.Clone(res.Models),
		SystemPrompt:     res.SystemPrompt,
		Temperature:      res.Temperature,
		MaxTokens:        res.MaxTokens,
		MaxSteps:         resolvedMaxSteps(res), // /steps overrides (0 = unlimited)
		MaxContextTokens: ctxBudget,
		// 0 = uncapped; config.Load already normalized negatives and enforced
		// the 1024-byte floor for positive values.
		MaxToolResultChars: res.Tools.MaxToolResultBytes,
		SessionID:          runtime.NewSessionID(),
	}
	s.resetSystem()
	return s
}

// CurrentModel returns the selected model under the session lock.
func (s *Session) CurrentModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// SelectModel changes the selected model when it is safe and permitted by the
// session's immutable provider policy.
func (s *Session) SelectModel(name string) bool {
	name, err := config.NormalizeModelName(name)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.allowedModels) > 0 && !slices.Contains(s.allowedModels, name) {
		return false
	}
	s.model = name
	return true
}

// ModelRestoreNotice returns a snapshot of a rejected saved model and the
// current selected model. A non-nil rejected value can be empty.
func (s *Session) ModelRestoreNotice() (saved, current string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rejectedSavedModel == nil {
		return "", "", false
	}
	return *s.rejectedSavedModel, s.model, true
}

func (s *Session) restoreModelLocked(saved string) {
	s.rejectedSavedModel = nil
	normalized, err := config.NormalizeModelName(saved)
	if err == nil && (len(s.allowedModels) == 0 || slices.Contains(s.allowedModels, normalized)) {
		s.model = normalized
		return
	}
	saved = strings.TrimSpace(saved)
	s.rejectedSavedModel = &saved
}

func (s *Session) resetSystem() {
	s.mu.Lock()
	// Replacing history wholesale invalidates any turn already in flight: bump
	// the generation so its writeback fails the myTurn == s.turnID check.
	// Without this, /clear is silently undone by the running turn and the
	// purged conversation is restored — then persisted by SaveAfterTurn.
	s.turnID++
	s.Messages = nil
	if s.SystemPrompt != "" {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: s.SystemPrompt,
		})
	}
	s.mu.Unlock()
}

// Clear drops conversation history but keeps the system prompt.
func (s *Session) Clear() {
	s.resetSystem()
}

// Store returns the wired persistence backend, or nil if none is attached.
// Slash commands that need to rebuild the SaveManager for a fresh session
// (/new) reach the store through this getter rather than capturing it in the
// CLI layer; the store is a property of the session that wired it.
func (s *Session) Store() SessionStore { return s.sessionStore }

// SetSessionStore wires a persistence backend and save manager onto the session.
// After calling this, Save/Load/ListSessions/DeleteSession delegate to the store,
// and SaveAfterTurn/SaveLast delegate to the manager.
// Pass nil to detach (all operations become no-ops or errors).
func (s *Session) SetSessionStore(store SessionStore, mgr *SaveManager) {
	s.sessionStore = store
	s.saveManager = mgr
	if store != nil {
		// SessionDir is kept for backward compatibility (autosave status file).
		if fstore, ok := store.(*FileSessionStore); ok {
			s.SessionDir = fstore.Dir()
		}
	}
}

// MessagesCount returns the number of messages under the read lock.
// Safe for concurrent use with agent goroutines.
func (s *Session) MessagesCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Messages)
}

// MessagesCopy returns a deep copy of all conversation messages under the read lock.
// TUI code must call this instead of reading s.Messages directly to avoid data races.
func (s *Session) MessagesCopy() []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]provider.Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

// UserTurns counts user messages in history.
func (s *Session) UserTurns() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser {
			n++
		}
	}
	return n
}

// SendUser handles one user turn (plain stream or agent loop).
func (s *Session) SendUser(ctx context.Context, userText string, w io.Writer) (string, error) {
	return s.sendUser(ctx, userText, w, nil)
}

// SendUserWithEvent handles one turn with a turn-local event callback.
func (s *Session) SendUserWithEvent(ctx context.Context, userText string, w io.Writer, onEvent func(agent.Event)) (string, error) {
	return s.sendUser(ctx, userText, w, onEvent)
}

func (s *Session) sendUser(ctx context.Context, userText string, w io.Writer, onEvent func(agent.Event)) (string, error) {
	if s.UseTools && s.Tools != nil {
		return s.sendAgent(ctx, userText, w, onEvent)
	}
	return s.sendPlain(ctx, userText, w)
}

func (s *Session) sendPlain(ctx context.Context, userText string, w io.Writer) (string, error) {
	// Lock, bump turn, copy messages + user text, unlock — API call is lock-free.
	s.mu.Lock()
	s.turnID++
	myTurn := s.turnID
	userMsg := provider.Message{Role: provider.RoleUser, Content: userText, CreatedAt: time.Now()}
	msgs := make([]provider.Message, len(s.Messages)+1)
	copy(msgs, s.Messages)
	msgs[len(s.Messages)] = userMsg
	model := s.model
	temp := s.Temperature
	maxTok := s.MaxTokens
	s.mu.Unlock()

	req := provider.Request{
		Model:       model,
		Messages:    msgs,
		Temperature: temp,
		MaxTokens:   maxTok,
		Stream:      true,
	}
	reply, err := s.Completer.ChatStream(ctx, req, w)
	if err != nil {
		// On error, we need to revert the user message addition.
		// Since msgs was a local copy, just return the error.
		// The session's Messages are unchanged.
		return "", err
	}

	// Only the latest turn may write history (stale/cancelled turn must not win).
	s.mu.Lock()
	if myTurn == s.turnID {
		s.Messages = append(s.Messages, userMsg)
		// Skip an empty reply: a contentless assistant message is rejected by
		// the API on every later turn, which would poison the session.
		if strings.TrimSpace(reply) != "" {
			s.Messages = append(s.Messages, provider.Message{
				Role:      provider.RoleAssistant,
				Content:   reply,
				CreatedAt: time.Now(),
			})
		}
	}
	s.mu.Unlock()

	// Auto-save after every successful plain turn too.
	s.SaveAfterTurn()

	return reply, nil
}

func (s *Session) sendAgent(ctx context.Context, userText string, w io.Writer, eventOverride func(agent.Event)) (string, error) {
	s.mu.Lock()
	s.turnID++
	myTurn := s.turnID
	ctxBudget := s.MaxContextTokens
	if ctxBudget <= 0 {
		ctxBudget = DefaultMaxContextTokens
	}
	// Deep-copy messages so the agent loop can run lock-free.
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	model := s.model
	temp := s.Temperature
	maxTok := s.MaxTokens
	maxSteps := s.MaxSteps
	maxToolResult := s.MaxToolResultChars
	onEvent := s.OnAgentEvent
	if eventOverride != nil {
		onEvent = eventOverride
	}
	s.mu.Unlock()

	loop := &agent.Loop{
		Completer: s.Completer,
		Tools:     s.Tools,
		Messages:  msgs,
	}
	toolTimeout := s.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = agent.DefaultToolTimeout
	}
	opts := agent.Options{
		Model:              model,
		Temperature:        temp,
		MaxTokens:          maxTok,
		MaxSteps:           maxSteps,
		MaxContextTokens:   ctxBudget,
		MaxToolResultChars: maxToolResult,
		RequestTimeout:     DefaultRequestTimeout,
		// Default for tools that do not declare Capability.Timeout.
		// Long tools (run_command, dispatch_tasks, delegate) advertise higher
		// budgets via Capability so they are not killed at this default.
		ToolTimeout: toolTimeout,
		ParentID:    "session",
		TurnID:      fmt.Sprintf("turn:%d", myTurn),
		SessionID:   s.SessionID,
		FinalWriter: w,
		OnEvent:     onEvent,
		EventBus:    s.EventBus,
		// A user is watching this turn: a completed turn with no answer must
		// surface as an error rather than a bare "done". Sub-agents deliberately
		// leave this off - see agent.Options.RequireFinalText.
		RequireFinalText: true,
	}
	if s.Dispatcher != nil {
		opts.Dispatcher = s.Dispatcher
	}
	reply, err := loop.Run(ctx, userText, opts)

	s.commitTurnHistory(loop.Messages, myTurn)
	return reply, err
}

// commitTurnHistory adopts a finished turn's history and persists it.
//
// Only when the turn is still current: a force-send / newer SendUser increments
// turnID, and a superseded turn must not overwrite the newer turn's Messages
// (last-writer-wins race) nor save the newer turn's state under its own name.
//
// Errored and cancelled turns are committed too. The user message is already in
// history and an interrupted turn keeps the text it streamed, so skipping the
// save left the transcript on disk missing a question the user asked and an
// answer they had already read on screen. Best-effort: never fails the reply.
func (s *Session) commitTurnHistory(msgs []provider.Message, myTurn uint64) {
	s.mu.Lock()
	current := myTurn == s.turnID
	if current {
		s.Messages = msgs
	}
	s.mu.Unlock()
	if current {
		s.SaveAfterTurn()
	}
}

// SaveAfterTurn saves the session as an auto-save without pruning.
// Designed to be called after each assistant turn completes so progress
// is never lost even if the process crashes between SaveLast calls.
//
// Unlike SaveLast, this does NOT prune old auto-saves — that only happens
// on graceful exit (SaveLast). This means we keep per-turn snapshots
// without worrying about the prune budget mid-session.
//
// If SessionDir is not set, this is a no-op.
// If there's no meaningful history (only system prompt), this is a no-op.
func (s *Session) SaveAfterTurn() {
	if s.SessionDir == "" {
		return
	}

	s.mu.RLock()
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	model := s.model
	hasContent := len(msgs) > 1
	s.mu.RUnlock()
	if !hasContent {
		return
	}

	// If a SaveManager is wired, delegate to it (handles naming + storage).
	if s.saveManager != nil {
		if err := s.saveManager.SaveAfterTurnWithModel(msgs, model); err != nil {
			fmt.Fprintf(os.Stderr, "\n⚠ turn auto-save failed: %v\n", err)
		}
		return
	}

	// Fallback: direct save via SessionDir (backward compat for unwired
	// sessions). Reuse one rolling directory rather than minting one per turn,
	// which grew the session tree without bound (see SaveManager.turnSaveName).
	s.mu.Lock()
	if s.turnSaveName == "" {
		s.turnSaveName = uniqAutoSaveName(s.SessionDir, turnSaveMarker)
	}
	name := s.turnSaveName
	s.mu.Unlock()
	if err := s.Save(name); err != nil {
		fmt.Fprintf(os.Stderr, "\n⚠ turn auto-save failed: %v\n", err)
	}
}
