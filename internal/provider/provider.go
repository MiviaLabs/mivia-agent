// Package provider implements LLM chat adapters for mivia.
package provider

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Role message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is a chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a chat completion request.
type Request struct {
	Model       string
	Messages    []Message
	Temperature *float64
	MaxTokens   *int
	Stream      bool
}

// Completer talks to an LLM provider.
type Completer interface {
	// Name returns the provider id (deepseek, openrouter).
	Name() string
	// ChatStream streams assistant text to w and returns the full assistant content.
	ChatStream(ctx context.Context, req Request, w io.Writer) (string, error)
	// Chat completes without streaming.
	Chat(ctx context.Context, req Request) (string, error)
}

// Options for constructing a completer from resolved config.
type Options struct {
	Name        string
	BaseURL     string
	APIKey      string
	Model       string
	HTTPReferer string
	XTitle      string
}

// New builds a Completer from resolved config.
func New(res *config.Resolved) (Completer, error) {
	if res == nil {
		return nil, fmt.Errorf("nil config")
	}
	if !res.APIKeySet || strings.TrimSpace(res.APIKey) == "" {
		return nil, fmt.Errorf("missing API key for provider %q (set %s in environment or env file)", res.ProviderName, res.APIKeyEnv)
	}
	opts := Options{
		Name:        res.ProviderName,
		BaseURL:     res.BaseURL,
		APIKey:      res.APIKey,
		Model:       res.Model,
		HTTPReferer: res.HTTPReferer,
		XTitle:      res.XTitle,
	}
	switch res.ProviderName {
	case config.DeepSeekName:
		return NewDeepSeek(opts)
	case config.OpenRouterName:
		return NewOpenRouter(opts)
	default:
		return nil, fmt.Errorf("unknown provider %q", res.ProviderName)
	}
}
