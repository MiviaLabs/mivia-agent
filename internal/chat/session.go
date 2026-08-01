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
	operatorPromptCap      int
	requestedPromptCap     int
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
	contextManager   *contextmgr.ContextManager
	contextPrincipal contextstate.Principal
	contextPolicy    contextstate.PolicySnapshot
	contextRedaction contextstate.RedactionPolicy
	contextStore     contextstate.Store
	contextHead      contextstate.Revision
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

func (s *Session) resetSystem() {
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	contextStore := s.contextStore
	contextPrincipal := s.contextPrincipal
	contextExpected := s.contextHead
	contextBinding := captureBindingRevision(s.binding)
	contextEnabled := s.contextEnabledLocked() && contextStore != nil
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
	s.mu.Unlock()
	if contextEnabled {
		if err := s.advanceContextHead(contextStore, contextPrincipal, contextExpected, contextBinding, contextBinding, "clear", true); err == nil {
			s.mu.Lock()
			if s.contextStore == contextStore && s.contextHead == contextExpected {
				s.contextHead = contextstate.Revision{Session: contextExpected.Session + 1, Durable: contextExpected.Durable + 1, Source: contextExpected.Source}
			}
			s.mu.Unlock()
		}
	}
}

// Clear drops conversation history but keeps the system prompt.
func (s *Session) Clear() {
	s.resetSystem()
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
	}
	if snapshot.toolTimeout <= 0 {
		snapshot.toolTimeout = agent.DefaultToolTimeout
	}
	opts := agent.Options{
		Model: snapshot.binding.Model, Temperature: snapshot.temperature, MaxTokens: snapshot.maxTokens,
		MaxSteps: snapshot.maxSteps, MaxContextTokens: snapshot.contextBudget,
		MaxToolResultChars: snapshot.maxToolResult,
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

func (s *Session) finishAgentTurn(ctx context.Context, loop *agent.Loop, registry *tools.Registry, userText, persistedText string, token OperationToken, turn *TurnOptions, contextCfg contextTurnConfig, turnErr error) error {
	agent.ScrubEphemeralToolMessages(loop.Messages, registry)
	replaceNewestUserText(loop.Messages, userText, persistedText)
	if contextCfg.manager != nil {
		interrupted := isInterruptedTurn(ctx, turnErr)
		if turnErr != nil {
			if !interrupted {
				if loop.HasPreparation {
					contextCfg.manager.PreparationManager.Discard(loop.LastPreparation)
				}
				if turn != nil && turn.Cleanup != nil {
					turn.Cleanup()
				}
				return nil
			}
		}
		if !loop.HasPreparation {
			if turn != nil && turn.Cleanup != nil {
				turn.Cleanup()
			}
			return fmt.Errorf("%w: agent completed without a preparation", contextstate.ErrCheckpointConflict)
		}
		s.contextPublishMu.Lock()
		defer s.contextPublishMu.Unlock()
		s.mu.RLock()
		current := s.tokenCurrentLocked(token)
		s.mu.RUnlock()
		if !current {
			contextCfg.manager.PreparationManager.Discard(loop.LastPreparation)
			if turn != nil && turn.Cleanup != nil {
				turn.Cleanup()
			}
			return ErrStaleOperation
		}
		ordered := contextTurnMessages(loop.Messages, userText)
		preparation := loop.LastPreparation
		commitCtx := ctx
		if interrupted {
			// The provider context is canceled by force-send, but the durable
			// history publication must still complete before the next turn starts.
			commitCtx = context.Background()
		}
		result, err := buildContextTurnResult(commitCtx, contextCfg, &preparation, loop.Messages, ordered, token.TurnID)
		if err == nil && interrupted {
			result.Outcome = contextmgr.OutcomeCancelled
		}
		if err == nil {
			err = contextCfg.manager.Commit(commitCtx, preparation, result)
		}
		contextCfg.manager.PreparationManager.Discard(preparation)
		if err == nil {
			s.mu.Lock()
			if !s.tokenCurrentLocked(token) {
				s.mu.Unlock()
				if turn != nil && turn.Cleanup != nil {
					turn.Cleanup()
				}
				return ErrStaleOperation
			}
			s.Messages = cloneContextMessages(loop.Messages)
			s.contextHead = nextContextRevision(preparation, result)
			s.mu.Unlock()
			s.emitContextCompaction(preparation, token.TurnID)
		}
		if turn != nil && turn.Cleanup != nil {
			turn.Cleanup()
		}
		return err
	}
	persistErr := s.commitPreparedTurn(loop.Messages, token, turnErr)
	if turn != nil && turn.Cleanup != nil {
		turn.Cleanup()
	}
	return persistErr
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

// commitPreparedTurn adopts a finished turn's history and persists it only
// while the captured operation fence remains current. Errored and cancelled
// turns still preserve the visible partial history.
func (s *Session) commitPreparedTurn(msgs []provider.Message, token OperationToken, turnErr error) error {
	s.mu.Lock()
	if !s.tokenCurrentLocked(token) {
		s.mu.Unlock()
		return ErrStaleOperation
	}
	if !errors.Is(turnErr, agent.ErrPromptBudgetExceeded) {
		s.Messages = msgs
	}
	s.mu.Unlock()
	return s.saveAfterTurn(token)
}
