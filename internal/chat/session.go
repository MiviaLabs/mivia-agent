// Package chat implements multi-turn sessions (plain chat and agent).
package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
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
	// RemainderSpool stores truncated tool-result bodies for read_output.
	// Set from the session dispatcher registration so notices and reads share
	// one grant domain. Nil omits refs from truncation notices.
	RemainderSpool *remainder.Spool
	// MaxContextTokens sets the approximate token limit for pruning.
	// 0 means use default (75% of typical model context window).
	MaxContextTokens int
	// Calibration is the rolling EWMA correction ratio carried across turns.
	// Read into every agent turn snapshot; the zero value is safe (no
	// correction).
	Calibration contextmgr.Calibration
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
	// binding is the mutex-owned provider/model/backend generation. Public
	// Completer and Dispatcher remain compatibility mirrors for older callers;
	// turn code captures binding instead of reading those fields after unlock.
	binding ModelBinding
	// eventIdentity builds a validated snapshot for each turn's event stream.
	eventIdentity          func(uint64) *events.Identity
	activeTurns            int
	switching              bool
	agentSurfaceGeneration uint64
	catalog                []config.ProviderModelGroup
	bindingFactory         func(providerName, model string) (ModelBinding, error)
	switchGuard            func() error
	// admittedTools, pendingAdmission and the admission counters are the
	// deferred-tool-loading state for the CURRENT agent binding (plan
	// tools/05). ResetAdmissions clears them on an /agent switch.
	admittedTools         []string
	pendingAdmission      *AdmissionStage
	admissionPublications int
	admissionAttempts     int
	admissionDeferrals    int
	admissionNotes        []string
	admissionAgent        string
	admissionDigest       string
	surfaceWidener        SurfaceWidener
	operatorPromptCap     int
	requestedPromptCap    int
	// turnID is incremented at the start of each SendUser turn.
	// Writeback of Messages only applies when the turn is still
	// current, so a cancelled/stale turn cannot overwrite a newer one
	// (force-send / overlapping SendUser).
	turnID uint64
	// operationEpoch and contextRevision fence work that outlives the session
	// lock. Clear, load, model/surface changes advance the session domain;
	// successful autosaves advance the durable domain.
	operationEpoch  uint64
	contextRevision contextstate.Revision
	// sessionStore is the persistence backend for save/load/list/delete.
	// When nil, persistence operations return errors (graceful degradation).
	sessionStore SessionStore
	// saveManager orchestrates auto-save strategies (per-turn, exit, prune).
	// When nil, SaveAfterTurn and SaveLast are no-ops.
	saveManager *SaveManager
	// turnSaveName is the rolling per-turn snapshot directory used by the
	// unwired fallback path, mirroring SaveManager.turnSaveName. Guarded by mu.
	turnSaveName string
	// contextManager is optional and deliberately separate from legacy
	// SessionStore. When enabled, durable turns use the checkpoint publisher
	// and never fall back to raw JSONL autosave.
	contextManager       *contextmgr.ContextManager
	contextPrincipal     contextstate.Principal
	contextPolicy        contextstate.PolicySnapshot
	contextRedaction     contextstate.RedactionPolicy
	contextStore         contextstate.Store
	contextHead          contextstate.Revision
	loadedContextSession bool
	// contextPublishMu serializes context publication with clear and turn
	// snapshot capture. Provider calls remain lock-free; only the durable
	// compare-and-swap and its in-memory adoption are serialized.
	contextPublishMu sync.Mutex
}

// TurnOptions supplies an invocation-local capability surface. It never
// mutates the session-owned registry or binding, which keeps scoped tools from
// leaking into ordinary or concurrent turns. Cleanup runs after history has
// been scrubbed and committed.
type TurnOptions struct {
	Tools      *tools.Registry
	Dispatcher *runtime.Dispatcher
	Cleanup    func()
}

func (s *Session) resetSystem() error {
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	contextStore := s.contextStore
	contextPrincipal := s.contextPrincipal
	contextExpected := s.contextHead
	contextBinding := captureBindingRevision(s.binding)
	contextEnabled := s.contextEnabledLocked() && contextStore != nil
	s.mu.Unlock()
	// Advance the durable head BEFORE mutating in-memory state, so that a
	// failure leaves the conversation intact and the user can retry.  This is
	// the INV-AG-35 guarantee: a refused commit must never destroy state
	// the user already has, and /clear is a commit from the user's
	// perspective.
	if contextEnabled {
		if err := s.advanceContextHead(contextStore, contextPrincipal, contextExpected, contextBinding, contextBinding, "clear", true); err != nil {
			return err
		}
	}
	s.mu.Lock()
	// Replacing history wholesale invalidates any turn already in flight: bump
	// the generation so its writeback fails the myTurn == s.turnID check.
	// Without this, /clear is silently undone by the running turn and the
	// purged conversation is restored - then persisted by SaveAfterTurn.
	s.invalidateLocked()
	s.turnID++
	s.Messages = nil
	if s.SystemPrompt != "" {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: s.SystemPrompt,
		})
	}
	if contextEnabled {
		s.contextHead = contextstate.Revision{Session: contextExpected.Session + 1, Durable: contextExpected.Durable + 1, Source: contextExpected.Source}
	}
	s.mu.Unlock()
	return nil
}

// Clear drops conversation history but keeps the system prompt.
func (s *Session) Clear() error {
	return s.resetSystem()
}

// MessagesCount returns the number of messages under the read lock.
// Safe for concurrent use with agent goroutines.
func (s *Session) MessagesCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Messages)
}

// LoadedContextSession reports whether the most recent Load adopted a durable
// context session (as opposed to a named chat_sessions snapshot).  Callers use
// this to surface the fork-on-load semantics to the user.
func (s *Session) LoadedContextSession() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadedContextSession
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
	return s.sendUser(ctx, userText, userText, w, nil)
}

// SendUserWithEvent handles one turn with a turn-local event callback.
func (s *Session) SendUserWithEvent(ctx context.Context, userText string, w io.Writer, onEvent func(agent.Event)) (string, error) {
	return s.sendUser(ctx, userText, userText, w, onEvent)
}

// SendUserWithEventAndPersistedText sends userText to the provider but keeps
// persistedText in conversation history. It is for UI-only expansions such as
// slash skills, whose private instruction bodies must not enter snapshots.
func (s *Session) SendUserWithEventAndPersistedText(ctx context.Context, userText, persistedText string, w io.Writer, onEvent func(agent.Event)) (string, error) {
	return s.sendUser(ctx, userText, persistedText, w, onEvent)
}

// SendUserWithTurnOptions is the scoped-capability variant used by activated
// skills. Passing nil retains the ordinary session behavior.
func (s *Session) SendUserWithTurnOptions(ctx context.Context, userText, persistedText string, w io.Writer, onEvent func(agent.Event), turn *TurnOptions) (string, error) {
	return s.sendUserWithTurn(ctx, userText, persistedText, w, onEvent, turn)
}

func (s *Session) sendUser(ctx context.Context, userText, persistedText string, w io.Writer, onEvent func(agent.Event)) (string, error) {
	return s.sendUserWithTurn(ctx, userText, persistedText, w, onEvent, nil)
}

func (s *Session) sendUserWithTurn(ctx context.Context, userText, persistedText string, w io.Writer, onEvent func(agent.Event), turn *TurnOptions) (string, error) {
	if s.AgentTurnEnabled() {
		return s.sendAgent(ctx, userText, persistedText, w, onEvent, turn)
	}
	if turn != nil && turn.Cleanup != nil {
		defer turn.Cleanup()
	}
	return s.sendPlain(ctx, userText, persistedText, w)
}

func (s *Session) sendPlain(ctx context.Context, userText, persistedText string, w io.Writer) (string, error) {
	snapshot, done, err := s.beginPlainTurn(userText)
	if err != nil {
		return "", err
	}
	defer done()
	if snapshot.context.manager != nil {
		return s.sendPlainContext(ctx, persistedText, w, snapshot)
	}
	return s.sendPlainLegacy(ctx, persistedText, w, snapshot)
}

func (s *Session) sendAgent(ctx context.Context, userText, persistedText string, w io.Writer, eventOverride func(agent.Event), turn *TurnOptions) (string, error) {
	snapshot, done, err := s.beginAgentTurn(userText, eventOverride)
	if err != nil {
		return "", err
	}
	defer done()
	toolRegistry, turnDispatcher := resolveTurnExecutionSurface(snapshot.toolRegistry, snapshot.binding.Dispatcher, turn)
	loop := &agent.Loop{
		Completer: snapshot.binding.Completer,
		Tools:     toolRegistry,
		Messages:  snapshot.messages,
		// Seeded from the session so the correction keeps accumulating instead
		// of restarting from zero samples every turn.
		Calibration: snapshot.Calibration,
	}
	if snapshot.toolTimeout <= 0 {
		snapshot.toolTimeout = agent.DefaultToolTimeout
	}
	opts := agent.Options{
		Model: snapshot.binding.Model, Temperature: snapshot.temperature, MaxTokens: snapshot.maxTokens,
		MaxSteps: snapshot.maxSteps, MaxContextTokens: snapshot.contextBudget,
		MaxToolResultChars: snapshot.maxToolResult,
		RemainderSpool:     s.RemainderSpool,
		RequestTimeout:     DefaultRequestTimeout,
		ToolTimeout:        snapshot.toolTimeout,
		ParentID:           "session",
		TurnID:             fmt.Sprintf("turn:%d", snapshot.myTurn), SessionID: snapshot.sessionID,
		FinalWriter: w,
		OnEvent:     snapshot.onEvent, EventBus: snapshot.eventBus, EventIdentity: snapshot.identity,
		RequireFinalText: true,
	}
	if snapshot.context.manager != nil {
		input := prepareInputForContext(snapshot.messages, snapshot.contextBudget, snapshot.maxTokens, snapshot.binding, snapshot.context.principal, snapshot.context.policy)
		input.Revision = snapshot.context.revision
		input.CurrentObjective = userText
		opts.PreparationManager = snapshot.context.manager.PreparationManager
		opts.PreparationInput = input
	}
	if turnDispatcher != nil {
		opts.Dispatcher = turnDispatcher
	}
	reply, err := loop.Run(ctx, userText, opts)

	if persistErr := s.finishAgentTurn(ctx, loop, toolRegistry, userText, persistedText, snapshot.token, turn, snapshot.context, err); persistErr != nil && !errors.Is(persistErr, ErrStaleOperation) {
		return reply, persistErr
	}
	return reply, err
}

// adoptCalibration copies a finished turn's rolling token calibration back
// into the session so the next turn starts from it.
//
// Deliberately not fenced by the turn's operation token, unlike history: an
// estimate-vs-actual observation stays true even when the turn errored or its
// fence went stale, and discarding it would leave the heuristic uncorrected
// exactly on the long turns that drift most. Concurrent turns each start from
// the same seed, so the one with the most samples is the most informed; the
// count only ever grows on top of what the turn was seeded with.
func (s *Session) adoptCalibration(turnCalibration contextmgr.Calibration) {
	if turnCalibration.Samples == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnCalibration.Samples >= s.Calibration.Samples {
		s.Calibration = turnCalibration
	}
}

// runTurnCleanup invokes the optional per-turn cleanup callback.
func (s *Session) runTurnCleanup(turn *TurnOptions) {
	if turn != nil && turn.Cleanup != nil {
		turn.Cleanup()
	}
}

func isInterruptedTurn(ctx context.Context, turnErr error) bool {
	if errors.Is(turnErr, context.Canceled) || errors.Is(turnErr, context.DeadlineExceeded) {
		return true
	}
	return ctx != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded))
}

func resolveTurnExecutionSurface(sessionTools *tools.Registry, sessionDispatcher *runtime.Dispatcher, turn *TurnOptions) (*tools.Registry, *runtime.Dispatcher) {
	if turn == nil {
		return sessionTools, sessionDispatcher
	}
	if turn.Tools != nil {
		sessionTools = turn.Tools
	}
	if turn.Dispatcher != nil {
		sessionDispatcher = turn.Dispatcher
	}
	return sessionTools, sessionDispatcher
}

func replaceNewestUserText(messages []provider.Message, userText, persistedText string) {
	if userText == persistedText {
		return
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleUser && messages[i].Content == userText {
			messages[i].Content = persistedText
			return
		}
	}
}
