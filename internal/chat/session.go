// Package chat implements multi-turn sessions (plain chat and agent).
package chat

import (
	"context"
	"io"

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
}

// DefaultMaxContextTokens is the default token budget for context pruning.
const DefaultMaxContextTokens = 96000

// NewSession builds a session from resolved config and completer.
func NewSession(res *config.Resolved, c provider.Completer) *Session {
	s := &Session{
		Completer:        c,
		Model:            res.Model,
		SystemPrompt:     res.SystemPrompt,
		Temperature:      res.Temperature,
		MaxTokens:        res.MaxTokens,
		MaxSteps:         30,
		MaxContextTokens: DefaultMaxContextTokens,
	}
	s.resetSystem()
	return s
}

func (s *Session) resetSystem() {
	s.Messages = nil
	if s.SystemPrompt != "" {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: s.SystemPrompt,
		})
	}
}

// Clear drops conversation history but keeps the system prompt.
func (s *Session) Clear() {
	s.resetSystem()
}

// UserTurns counts user messages in history.
func (s *Session) UserTurns() int {
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
	s.Messages = append(s.Messages, provider.Message{
		Role:    provider.RoleUser,
		Content: userText,
	})
	req := provider.Request{
		Model:       s.Model,
		Messages:    s.Messages,
		Temperature: s.Temperature,
		MaxTokens:   s.MaxTokens,
		Stream:      true,
	}
	reply, err := s.Completer.ChatStream(ctx, req, w)
	if err != nil {
		s.popLastUser()
		return "", err
	}
	s.Messages = append(s.Messages, provider.Message{
		Role:    provider.RoleAssistant,
		Content: reply,
	})
	return reply, nil
}

func (s *Session) sendAgent(ctx context.Context, userText string, w io.Writer) (string, error) {
	ctxBudget := s.MaxContextTokens
	if ctxBudget <= 0 {
		ctxBudget = DefaultMaxContextTokens
	}
	loop := &agent.Loop{
		Completer: s.Completer,
		Tools:     s.Tools,
		Messages:  append([]provider.Message(nil), s.Messages...),
	}
	reply, err := loop.Run(ctx, userText, agent.Options{
		Model:            s.Model,
		Temperature:      s.Temperature,
		MaxTokens:        s.MaxTokens,
		MaxSteps:         s.MaxSteps,
		MaxContextTokens: ctxBudget,
		FinalWriter:      w,
		OnEvent:          s.OnAgentEvent,
	})
	// Persist full history including tools.
	s.Messages = loop.Messages
	if err != nil {
		return reply, err
	}
	return reply, nil
}

func (s *Session) popLastUser() {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == provider.RoleUser {
			s.Messages = s.Messages[:i]
			return
		}
	}
}
