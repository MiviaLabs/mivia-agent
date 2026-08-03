package chat

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// The plain (non-agent) chat turn: its snapshot, its admission, and the two
// send paths. Split from context_integration.go, which reached the file-size
// gate's ceiling; the agent turn and the context-store wiring stay there.

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
