package chat

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type contextTurnConfig struct {
	manager   *contextmgr.ContextManager
	principal contextstate.Principal
	policy    contextstate.PolicySnapshot
	redaction contextstate.RedactionPolicy
	revision  contextstate.Revision
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
	sessionID         string
	eventBus          *events.Bus
	identityFactory   func(uint64) *events.Identity
	identity          *events.Identity
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
	s.mu.Unlock()
	if manager != nil && manager.Enabled && store != nil {
		snapshot, err := ensureAndLoadContextStore(store, principal, s.captureBindingRevision())
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
	snapshot, err := ensureAndLoadContextStore(store, principal, s.captureBindingRevision())
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
	s.mu.RUnlock()
	if store == nil || !principal.IsBound() {
		return fmt.Errorf("context store is not configured")
	}
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
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
	s.mu.RUnlock()
	if store == nil || !principal.IsBound() {
		return fmt.Errorf("context store not configured")
	}
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
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

func ensureAndLoadContextStore(store contextstate.Store, principal contextstate.Principal, binding contextstate.BindingRevision) (contextstate.Snapshot, error) {
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		return contextstate.Snapshot{}, err
	}
	return store.Load(context.Background(), principal, principal.SessionID)
}

func (s *Session) ContextStore() contextstate.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextStore
}

func (s *Session) ContextEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextEnabledLocked()
}

func (s *Session) ContextManager() *contextmgr.ContextManager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.contextManager == nil {
		return nil
	}
	copyManager := *s.contextManager
	return &copyManager
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
	}
}

func (s *Session) beginPlainTurn(userText string) (plainTurnSnapshot, func(), error) {
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return plainTurnSnapshot{}, nil, fmt.Errorf("session surface switching is in progress")
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
		eventBus: s.EventBus, identityFactory: s.eventIdentity,
		Calibration: s.Calibration,
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
	reply, err := snapshot.binding.Completer.ChatStream(ctx, provider.Request{
		Model: snapshot.binding.Model, Messages: prepared, Temperature: snapshot.temperature,
		MaxTokens: snapshot.maxTokens, Stream: true,
	}, w)
	if err != nil {
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
	input := prepareInputForContext(snapshot.messages, snapshot.budget, snapshot.maxTokens, snapshot.binding, snapshot.context.principal, snapshot.context.policy)
	input.Revision = snapshot.context.revision
	if snapshot.tools != nil {
		input.Tools = snapshot.tools.OpenAITools()
	}
	preparation, err := snapshot.context.manager.Prepare(ctx, input)
	if err != nil {
		return "", err
	}
	prepared := preparation.Messages
	reply, err := snapshot.binding.Completer.ChatStream(ctx, provider.Request{
		Model: snapshot.binding.Model, Messages: prepared, Temperature: snapshot.temperature,
		MaxTokens: snapshot.maxTokens, Stream: true,
	}, w)
	if err != nil {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return "", err
	}
	if !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return reply, nil
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	if !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return reply, nil
	}
	candidate := cloneContextMessages(prepared)
	userText := snapshot.messages[len(snapshot.messages)-1].Content
	replaceNewestUserText(candidate, userText, persistedText)
	if strings.TrimSpace(reply) != "" {
		candidate = append(candidate, provider.Message{Role: provider.RoleAssistant, Content: reply, CreatedAt: time.Now()})
	}
	ordered := contextTurnMessages(candidate, userText)
	result, err := buildContextTurnResult(ctx, snapshot.context, &preparation, candidate, ordered, snapshot.myTurn)
	if err == nil {
		err = snapshot.context.manager.Commit(ctx, preparation, result)
	}
	if err != nil {
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return "", err
	}
	s.mu.Lock()
	if !s.tokenCurrentLocked(snapshot.token) {
		s.mu.Unlock()
		snapshot.context.manager.PreparationManager.Discard(preparation)
		return reply, ErrStaleOperation
	}
	s.Messages = candidate
	s.contextHead = nextContextRevision(preparation, result)
	s.mu.Unlock()
	snapshot.context.manager.PreparationManager.Discard(preparation)
	s.emitContextCompaction(preparation, snapshot.myTurn)
	return reply, nil
}

func (s *Session) plainTurnCurrent(token OperationToken, turn uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turnID == turn && s.tokenCurrentLocked(token)
}

func contextTurnMessages(messages []provider.Message, userText string) []provider.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser && messages[index].Content == userText {
			return cloneContextMessages(messages[index:])
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser {
			return cloneContextMessages(messages[index:])
		}
	}
	return nil
}

func cloneContextMessages(messages []provider.Message) []provider.Message {
	output := make([]provider.Message, len(messages))
	copy(output, messages)
	for index := range output {
		output[index].ToolCalls = append([]provider.ToolCall(nil), messages[index].ToolCalls...)
	}
	return output
}

func prepareInputForContext(messages []provider.Message, budget int, maxTokens *int, binding ModelBinding, principal contextstate.Principal, policy contextstate.PolicySnapshot) contextmgr.PrepareInput {
	return contextmgr.PrepareInput{
		Messages: messages, Budget: budget, OutputReserve: outputReserve(maxTokens),
		CurrentObjective: latestUserMessage(messages), Principal: principal,
		Revision: contextstate.Revision{}, Binding: captureBindingRevision(binding), Policy: policy,
	}
}

func latestUserMessage(messages []provider.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser {
			return messages[index].Content
		}
	}
	return ""
}

func outputReserve(maxTokens *int) int {
	if maxTokens == nil || *maxTokens < 0 {
		return 0
	}
	return *maxTokens
}
