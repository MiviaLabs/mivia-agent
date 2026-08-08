package chat

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type contextTurnConfig struct {
	manager   *contextmgr.ContextManager
	principal contextstate.Principal
	policy    contextstate.PolicySnapshot
	redaction contextstate.RedactionPolicy
	revision  contextstate.Revision
	worktree  contextstate.WorktreeInstance
}

type plainTurnSnapshot struct {
	myTurn      uint64
	messages    []provider.Message
	binding     ModelBinding
	token       OperationToken
	context     contextTurnConfig
	budget      int
	temperature *float64
	maxTokens   *int
	tools       *tools.Registry
}

type agentTurnSnapshot struct {
	myTurn            uint64
	messages          []provider.Message
	binding           ModelBinding
	token             OperationToken
	context           contextTurnConfig
	contextBudget     int
	temperature       *float64
	maxTokens         *int
	maxSteps          int
	maxToolResult     int
	batchResultBudget int
	onEvent           func(agent.Event)
	toolRegistry      *tools.Registry
	toolTimeout       time.Duration
	remainderSpool    *remainder.Spool
	sessionID         string
	eventBus          *events.Bus
	identityFactory   func(uint64) *events.Identity
	identity          *events.Identity
	// pendingAdmission reports whether a stage awaited publication when the
	// turn began. It lets sendAgent engage the start-of-turn publication
	// without adding a lock acquisition to every turn.
	pendingAdmission bool
	// Calibration is the rolling EWMA correction ratio carried across turns.
	Calibration contextmgr.Calibration
}

func (s *Session) Store() SessionStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionStore
}

func (s *Session) SetSessionStore(store SessionStore, mgr *SaveManager) {
	s.mu.Lock()
	if s.contextEnabledLocked() && store != nil {
		s.mu.Unlock()
		return
	}
	s.sessionStore, s.saveManager = store, mgr
	if fstore, ok := store.(*FileSessionStore); ok {
		s.SessionDir = fstore.Dir()
	}
	s.mu.Unlock()
	if mgr != nil {
		mgr.SetCurrentFence(s.currentSaveToken)
	}
}

func (s *Session) SetContextManager(manager *contextmgr.ContextManager, principal contextstate.Principal, policies ...contextstate.PolicySnapshot) error {
	if manager != nil {
		if err := principal.Validate(); err != nil {
			return err
		}
		if !principal.IsBound() {
			return fmt.Errorf("%w: owner capability is not bound", contextstate.ErrPrincipalMismatch)
		}
	}
	s.mu.Lock()
	if manager != nil && principal.SessionID != s.SessionID {
		s.mu.Unlock()
		return fmt.Errorf("%w: context principal session differs", contextstate.ErrPrincipalMismatch)
	}
	store := s.contextStore
	if manager == nil || !manager.Enabled {
		s.contextManager, s.contextPrincipal = nil, contextstate.Principal{}
		s.contextPolicy, s.contextStore = contextstate.PolicySnapshot{}, nil
		s.contextHead = contextstate.Revision{}
	} else {
		copyManager := *manager
		s.contextManager, s.contextPrincipal = &copyManager, principal
		if len(policies) > 0 {
			s.contextPolicy = policies[0]
		}
		s.sessionStore, s.saveManager, s.SessionDir = nil, nil, ""
	}
	worktree := s.contextWorktree
	sessionDir := s.contextSessionDir
	s.mu.Unlock()
	if manager != nil && manager.Enabled && store != nil {
		snapshot, err := ensureAndLoadContextStore(store, principal, s.captureBindingRevision(), worktree, sessionDir)
		if err != nil {
			return err
		}
		s.mu.Lock()
		if s.contextStore == store && s.contextManager != nil && s.contextPrincipal == principal {
			s.contextHead = snapshot.Revision
		}
		s.mu.Unlock()
	}
	return nil
}

func (s *Session) SetContextRedactionPolicy(policy contextstate.RedactionPolicy) {
	s.mu.Lock()
	s.contextRedaction = policy
	s.mu.Unlock()
}

func (s *Session) SetContextStore(store contextstate.Store) error {
	s.mu.Lock()
	s.contextStore = store
	principal := s.contextPrincipal
	enabled := s.contextEnabledLocked()
	s.mu.Unlock()
	if store == nil || !enabled || !principal.IsBound() {
		return nil
	}
	s.mu.RLock()
	worktree := s.contextWorktree
	sessionDir := s.contextSessionDir
	s.mu.RUnlock()
	snapshot, err := ensureAndLoadContextStore(store, principal, s.captureBindingRevision(), worktree, sessionDir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.contextStore == store && s.contextPrincipal == principal {
		s.contextHead = snapshot.Revision
	}
	s.mu.Unlock()
	return nil
}

func (s *Session) loadContextSnapshot(operationName string) error {
	s.mu.RLock()
	store := s.contextStore
	principal := s.contextPrincipal
	token := s.captureOperationTokenLocked("context-load:" + operationName)
	binding := captureBindingRevision(s.binding)
	instance := s.contextWorktree
	s.mu.RUnlock()
	if store == nil || !principal.IsBound() {
		return fmt.Errorf("context store is not configured")
	}
	snapshot, err := loadBoundContextStore(context.Background(), store, principal, principal.SessionID, instance)
	if err != nil {
		return err
	}
	if snapshot.Binding != binding {
		return fmt.Errorf("%w: durable context binding differs", contextstate.ErrStaleBinding)
	}
	messages := []provider.Message{}
	if len(snapshot.Active.ActiveContext) > 0 {
		if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &messages); err != nil {
			return fmt.Errorf("decode active context: %w", err)
		}
		if err := provider.ValidateToolPairing(messages); err != nil {
			return fmt.Errorf("active context message shape: %w", err)
		}
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeTurns > 0 || !s.tokenCurrentLocked(token) {
		return ErrStaleOperation
	}
	s.Messages = cloneContextMessages(messages)
	s.contextHead = snapshot.Revision
	s.invalidateLocked()
	return nil
}

// resyncContextHead re-syncs s.contextHead and s.Messages from the durable
// store after a successful commit whose post-adoption fence check failed.
// The caller MUST hold contextPublishMu and MUST NOT hold mu.  This is the
// INV-AG-35 recovery path: without it, a drifted contextHead permanently
// wedges the session.
func (s *Session) resyncContextHead() error {
	s.mu.RLock()
	store := s.contextStore
	principal := s.contextPrincipal
	binding := captureBindingRevision(s.binding)
	instance := s.contextWorktree
	s.mu.RUnlock()
	if store == nil || !principal.IsBound() {
		return fmt.Errorf("context store not configured")
	}
	snapshot, err := loadBoundContextStore(context.Background(), store, principal, principal.SessionID, instance)
	if err != nil {
		return err
	}
	if snapshot.Binding != binding {
		return fmt.Errorf("%w: durable context binding differs", contextstate.ErrStaleBinding)
	}
	messages := []provider.Message{}
	if len(snapshot.Active.ActiveContext) > 0 {
		if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &messages); err != nil {
			return fmt.Errorf("decode active context: %w", err)
		}
	}
	s.mu.Lock()
	if s.contextStore == store && s.contextPrincipal == principal {
		s.Messages = cloneContextMessages(messages)
		s.contextHead = snapshot.Revision
		s.invalidateLocked()
	}
	s.mu.Unlock()
	return nil
}

func ensureAndLoadContextStore(store contextstate.Store, principal contextstate.Principal, binding contextstate.BindingRevision, instance contextstate.WorktreeInstance, retainedDir string) (contextstate.Snapshot, error) {
	dir, worktree := currentDirContext()
	if !instance.IsZero() {
		dir = retainedDir
		worktree = instance.Worktree
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding, Dir: dir, Worktree: worktree, WorktreeInstance: instance}); err != nil {
		return contextstate.Snapshot{}, err
	}
	return loadBoundContextStore(context.Background(), store, principal, principal.SessionID, instance)
}

func loadBoundContextStore(ctx context.Context, store contextstate.Store, principal contextstate.Principal, sessionID string, instance contextstate.WorktreeInstance) (contextstate.Snapshot, error) {
	if !instance.IsZero() {
		if scoped, ok := store.(contextstate.WorktreeStore); ok {
			return scoped.LoadWorktree(ctx, principal, sessionID, instance)
		}
		return contextstate.Snapshot{}, contextstate.ErrWorktreeDeleted
	}
	return store.Load(ctx, principal, sessionID)
}

func (s *Session) contextEnabledLocked() bool {
	return s.contextManager != nil && s.contextManager.Enabled &&
		s.contextManager.PreparationManager != nil && s.contextManager.CheckpointPublisher != nil &&
		s.contextPrincipal.IsBound() && s.contextPrincipal.SessionID == s.SessionID
}

func (s *Session) captureContextLocked() contextTurnConfig {
	if !s.contextEnabledLocked() {
		return contextTurnConfig{}
	}
	manager := *s.contextManager
	return contextTurnConfig{
		manager: &manager, principal: s.contextPrincipal,
		policy: s.contextPolicy, redaction: s.contextRedaction,
		revision: s.contextHead,
		worktree: s.contextWorktree,
	}
}

// SetContextWorktreeBinding retains the physical worktree identity for every
// later context mutation. Call it before installing a context store.
func (s *Session) SetContextWorktreeBinding(instance contextstate.WorktreeInstance) error {
	dir, _ := currentDirContext()
	return s.SetContextWorktreeBindingAt(instance, dir, dir)
}

// SetContextWorktreeBindingAt retains the exact managed worktree paths.
func (s *Session) SetContextWorktreeBindingAt(instance contextstate.WorktreeInstance, root, dir string) error {
	if err := instance.Validate(); err != nil {
		return err
	}
	if instance.IsZero() || !filepath.IsAbs(root) || !filepath.IsAbs(dir) {
		return fmt.Errorf("context worktree binding requires absolute paths")
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
		return fmt.Errorf("context session directory is outside the worktree root")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contextStore != nil || s.contextManager != nil {
		return fmt.Errorf("context worktree binding must be set before context setup")
	}
	s.contextWorktree = instance
	s.contextWorktreeRoot = filepath.Clean(root)
	s.contextSessionDir = filepath.Clean(dir)
	return nil
}

func (s *Session) beginPlainTurn(userText string) (plainTurnSnapshot, func(), error) {
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return plainTurnSnapshot{}, nil, fmt.Errorf("session surface switching is in progress")
	}
	if s.loading {
		s.mu.Unlock()
		return plainTurnSnapshot{}, nil, fmt.Errorf("session loading is in progress")
	}
	s.activeTurns++
	s.turnID++
	myTurn := s.turnID
	messages := make([]provider.Message, len(s.Messages)+1)
	copy(messages, s.Messages)
	messages[len(s.Messages)] = provider.Message{Role: provider.RoleUser, Content: userText, CreatedAt: time.Now()}
	binding := s.captureBindingLocked()
	token := s.captureOperationTokenLocked(fmt.Sprintf("turn:%d", myTurn))
	budget := binding.PromptBudgetTokens
	if budget <= 0 {
		budget = s.MaxContextTokens
	}
	snapshot := plainTurnSnapshot{
		myTurn: myTurn, messages: messages, binding: binding, token: token,
		context: s.captureContextLocked(), budget: budget,
		temperature: s.Temperature, maxTokens: config.EffectiveOutputTokens(binding.Profile, s.MaxTokens), tools: s.Tools,
	}
	s.mu.Unlock()
	done := func() {
		s.mu.Lock()
		s.activeTurns--
		s.mu.Unlock()
	}
	return snapshot, done, nil
}

func (s *Session) beginAgentTurn(userText string, eventOverride func(agent.Event)) (agentTurnSnapshot, func(), error) {
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return agentTurnSnapshot{}, nil, fmt.Errorf("session surface switching is in progress")
	}
	if s.loading {
		s.mu.Unlock()
		return agentTurnSnapshot{}, nil, fmt.Errorf("session loading is in progress")
	}
	s.activeTurns++
	s.turnID++
	myTurn := s.turnID
	binding := s.captureBindingLocked()
	token := s.captureOperationTokenLocked(fmt.Sprintf("turn:%d", myTurn))
	budget := binding.PromptBudgetTokens
	if budget <= 0 {
		budget = s.MaxContextTokens
	}
	messages := cloneContextMessages(s.Messages)
	onEvent := s.OnAgentEvent
	if eventOverride != nil {
		onEvent = eventOverride
	}
	snapshot := agentTurnSnapshot{
		myTurn: myTurn, messages: messages, binding: binding, token: token,
		context: s.captureContextLocked(), contextBudget: budget,
		temperature: s.Temperature, maxTokens: config.EffectiveOutputTokens(binding.Profile, s.MaxTokens),
		maxSteps: s.MaxSteps, maxToolResult: s.MaxToolResultChars,
		batchResultBudget: s.BatchResultBudgetBytes, onEvent: onEvent,
		toolRegistry: s.Tools, toolTimeout: s.ToolTimeout, sessionID: s.SessionID,
		// Captured under the lock: the host republishes the spool after a
		// surface publication, concurrently with turns starting.
		remainderSpool: s.RemainderSpool,
		eventBus:       s.EventBus, identityFactory: s.eventIdentity,
		pendingAdmission: s.pendingAdmission != nil,
		Calibration:      s.Calibration,
	}
	s.mu.Unlock()
	if snapshot.identityFactory != nil {
		snapshot.identity = snapshot.identityFactory(binding.ModelGeneration)
	}
	done := func() {
		s.mu.Lock()
		s.activeTurns--
		s.mu.Unlock()
	}
	return snapshot, done, nil
}

func (s *Session) sendPlainLegacy(ctx context.Context, persistedText string, w io.Writer, snapshot plainTurnSnapshot) (string, error) {
	prepared := provider.PruneMessagesKeepTurns(snapshot.messages, snapshot.budget)
	if snapshot.budget > 0 && provider.MessagesTokens(prepared) > snapshot.budget {
		return "", fmt.Errorf("%w (%d > %d tokens)", agent.ErrPromptBudgetExceeded, provider.MessagesTokens(prepared), snapshot.budget)
	}
	// The tee captures the already-streamed bytes: on an interrupted turn
	// ChatStream returns "" as the reply, so the writer is the only record of
	// the partial answer the user already read on screen. A nil caller writer
	// (tests, headless callers) keeps the capture-only surface.
	var captured strings.Builder
	streamWriter := io.Writer(&captured)
	if w != nil {
		streamWriter = io.MultiWriter(w, &captured)
	}
	reply, err := snapshot.binding.Completer.ChatStream(ctx, provider.Request{
		Model: snapshot.binding.Model, Messages: prepared, Temperature: snapshot.temperature,
		MaxTokens: snapshot.maxTokens, Stream: true,
		ReasoningLevel: snapshot.binding.Profile.Reasoning, ReasoningDialect: snapshot.binding.Profile.ReasoningDialect,
	}, streamWriter)
	if err != nil {
		// An interrupted turn (Ctrl+C / force-send / deadline) must not lose
		// the user's message or the answer already streamed: adopt both and
		// persist, then hand the partial back instead of the error. Only a
		// still-current turn may persist (stale-turn fence). Non-interrupted
		// errors keep today's drop-everything behavior.
		if partial, ok := s.adoptInterruptedPlainTurn(ctx, err, snapshot, prepared, persistedText, captured.String()); ok {
			return partial, nil
		}
		return "", err
	}
	if !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		return reply, nil
	}
	s.mu.Lock()
	replaceNewestUserText(prepared, snapshot.messages[len(snapshot.messages)-1].Content, persistedText)
	s.Messages = prepared
	if strings.TrimSpace(reply) != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: reply, CreatedAt: time.Now()})
	}
	s.mu.Unlock()
	_ = s.saveAfterTurn(snapshot.token)
	return reply, nil
}

func (s *Session) sendPlainContext(ctx context.Context, persistedText string, w io.Writer, snapshot plainTurnSnapshot) (string, error) {
	input := prepareInputForContext(snapshot.messages, snapshot.budget, snapshot.maxTokens, snapshot.binding, snapshot.context.principal, snapshot.context.policy, snapshot.context.worktree)
	input.Revision = snapshot.context.revision
	if snapshot.tools != nil {
		input.Tools = snapshot.tools.OpenAITools()
	}
	preparation, err := snapshot.context.manager.Prepare(ctx, input)
	if err != nil {
		return "", err
	}
	prepared := preparation.Messages
	// The tee captures the already-streamed bytes: on an interrupted turn
	// ChatStream returns "" as the reply, so the writer is the only record of
	// the partial answer the user already read on screen. A nil caller writer
	// (tests, headless callers) keeps the capture-only surface.
	var captured strings.Builder
	streamWriter := io.Writer(&captured)
	if w != nil {
		streamWriter = io.MultiWriter(w, &captured)
	}
	reply, err := snapshot.binding.Completer.ChatStream(ctx, provider.Request{
		Model: snapshot.binding.Model, Messages: prepared, Temperature: snapshot.temperature,
		MaxTokens: snapshot.maxTokens, Stream: true,
		ReasoningLevel: snapshot.binding.Profile.Reasoning, ReasoningDialect: snapshot.binding.Profile.ReasoningDialect,
	}, streamWriter)
	if err != nil {
		// An interrupted turn (Ctrl+C / force-send / deadline) must not lose
		// the user's message or the answer already streamed: the interrupted
		// branch commits the partial turn durably with OutcomeCancelled; all
		// other errors keep today's discard-and-drop behavior.
		return s.commitInterruptedPlainContext(ctx, err, snapshot, prepared, persistedText, captured.String(), preparation)
	}
	return s.commitPlainContextTurn(ctx, reply, snapshot, prepared, persistedText, preparation)
}
