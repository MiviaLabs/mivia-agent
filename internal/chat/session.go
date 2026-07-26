// Package chat implements a simple in-memory multi-turn chat session.
package chat

import (
	"context"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// Session holds conversation history and a completer.
type Session struct {
	Completer    provider.Completer
	Model        string
	SystemPrompt string
	Temperature  *float64
	MaxTokens    *int
	Messages     []provider.Message
}

// NewSession builds a session from resolved config and completer.
func NewSession(res *config.Resolved, c provider.Completer) *Session {
	s := &Session{
		Completer:    c,
		Model:        res.Model,
		SystemPrompt: res.SystemPrompt,
		Temperature:  res.Temperature,
		MaxTokens:    res.MaxTokens,
	}
	if s.SystemPrompt != "" {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: s.SystemPrompt,
		})
	}
	return s
}

// SendUser appends a user message, streams the assistant reply to w, and stores it.
func (s *Session) SendUser(ctx context.Context, userText string, w io.Writer) (string, error) {
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
		// Drop the user turn on hard failure? Keep it so retries can continue with history.
		return "", err
	}
	s.Messages = append(s.Messages, provider.Message{
		Role:    provider.RoleAssistant,
		Content: reply,
	})
	return reply, nil
}
