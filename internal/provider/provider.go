// Package provider implements LLM chat adapters for mivia.
package provider

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Role message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is a chat turn (supports tool calls and tool results).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is an OpenAI-compatible function call from the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolSpec is an OpenAI tools[] entry (already shaped as map from tools.Registry).
type ToolSpec = map[string]any

// Request is a chat completion request.
type Request struct {
	Model       string
	Messages    []Message
	Temperature *float64
	MaxTokens   *int
	Stream      bool
	Tools       []ToolSpec
	ToolChoice  string // "auto", "none", or empty
	Timeout     time.Duration
}

// Response is a non-stream completion result.
type Response struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
}

// Completer talks to an LLM provider.
type Completer interface {
	Name() string
	ChatStream(ctx context.Context, req Request, w io.Writer) (string, error)
	Chat(ctx context.Context, req Request) (string, error)
	// ChatTurn is a non-stream turn that may return tool_calls.
	ChatTurn(ctx context.Context, req Request) (*Response, error)
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
