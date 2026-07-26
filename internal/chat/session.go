// Package chat implements multi-turn sessions (plain chat and agent).
package chat

import (
	"context"
	"io"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Session holds conversation history and a completer.
type Session struct {
	Completer    provider.Completer
	Model        string
	SystemPrompt string
	Temperature  *float64
	MaxTokens    *int
	Messages     []provider.Message
	Tools        *tools.Registry
	// UseTools enables the agent loop when Tools is set.
	UseTools bool
	MaxSteps int
	// MaxContextTokens sets the approximate token limit for pruning.
	// 0 means use default (75% of typical model context window).
	MaxContextTokens int
	// OnAgentEvent optional tool/step tracing.
	OnAgentEvent func(agent.Event)
	// SessionDir is the directory where sessions are persisted
	// (e.g., <workspace>/.mivia/sessions/). When set, enables
	// save/load/list/delete operations and auto-save on exit.
	SessionDir string
	// mu protects concurrent mutations to Messages and Model.
	// All exported methods that read or write these fields must
	// hold mu. Save/Load use the lock-and-copy pattern so I/O
	// happens without the lock while the snapshot is safe.
	mu sync.Mutex
}

// DefaultMaxContextTokens is the default token budget for context pruning.
// DeepSeek models support up to 1M tokens; this conservative default
// allows comfortable headroom while preventing runaway context.
const (
	DefaultMaxContextTokens   = 1000000
	DefaultMaxToolResultChars = 4000
)

// NewSession builds a session from resolved config and completer.
func NewSession(res *config.Resolved, c provider.Completer) *Session {
	ctxBudget := res.MaxContextTokens
	if ctxBudget <= 0 {
		ctxBudget = DefaultMaxContextTokens
	}
	s := &Session{
		Completer:        c,
		Model:            res.Model,
		SystemPrompt:     res.SystemPrompt,
		Temperature:      res.Temperature,
		MaxTokens:        res.MaxTokens,
		MaxSteps:         0, // 0 = unlimited; set via /steps or config if desired
		MaxContextTokens: ctxBudget,
	}
	s.resetSystem()
	return s
}

func (s *Session) resetSystem() {
	s.mu.Lock()
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

// UserTurns counts user messages in history.
func (s *Session) UserTurns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if s.UseTools && s.Tools != nil {
		return s.sendAgent(ctx, userText, w)
	}
	return s.sendPlain(ctx, userText, w)
}

func (s *Session) sendPlain(ctx context.Context, userText string, w io.Writer) (string, error) {
	// Lock, copy messages + user text, unlock — API call is lock-free.
	s.mu.Lock()
	userMsg := provider.Message{Role: provider.RoleUser, Content: userText}
	msgs := make([]provider.Message, len(s.Messages)+1)
	copy(msgs, s.Messages)
	msgs[len(s.Messages)] = userMsg
	model := s.Model
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

	// Lock again to append the assistant reply.
	s.mu.Lock()
	s.Messages = append(s.Messages, userMsg)
	s.Messages = append(s.Messages, provider.Message{
		Role:    provider.RoleAssistant,
		Content: reply,
	})
	s.mu.Unlock()

	return reply, nil
}

func (s *Session) sendAgent(ctx context.Context, userText string, w io.Writer) (string, error) {
	s.mu.Lock()
	ctxBudget := s.MaxContextTokens
	if ctxBudget <= 0 {
		ctxBudget = DefaultMaxContextTokens
	}
	// Deep-copy messages so the agent loop can run lock-free.
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	model := s.Model
	temp := s.Temperature
	maxTok := s.MaxTokens
	maxSteps := s.MaxSteps
	onEvent := s.OnAgentEvent
	s.mu.Unlock()

	loop := &agent.Loop{
		Completer: s.Completer,
		Tools:     s.Tools,
		Messages:  msgs,
	}
	reply, err := loop.Run(ctx, userText, agent.Options{
		Model:            model,
		Temperature:      temp,
		MaxTokens:        maxTok,
		MaxSteps:         maxSteps,
		MaxContextTokens: ctxBudget,
		FinalWriter:      w,
		OnEvent:          onEvent,
	})

	// Lock to persist full history including tools.
	s.mu.Lock()
	s.Messages = loop.Messages
	s.mu.Unlock()

	if err != nil {
		return reply, err
	}
	return reply, nil
}

func (s *Session) popLastUser() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == provider.RoleUser {
			s.Messages = s.Messages[:i]
			return
		}
	}
}
