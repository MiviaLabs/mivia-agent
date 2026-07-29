// Package provider implements LLM chat adapters for mivia.
package provider

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
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
	// CreatedAt is local wall time when the message entered session history.
	// Persisted in session JSONL; stripped before provider API requests.
	// Zero means unknown (legacy sessions).
	CreatedAt time.Time `json:"created_at,omitempty"`
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
	// StreamWriter receives content deltas when ChatTurn streams (Stream=true).
	// Tool-call argument fragments are not written here — only assistant text.
	StreamWriter io.Writer
	Tools        []ToolSpec
	ToolChoice   string // "auto", "none", or empty
	Timeout      time.Duration
}

// WebSearchResult is provider-supplied search context attached to a completion.
// Fields are intentionally transport-level so adapters can preserve provider
// responses without interpreting or rendering them.
type WebSearchResult struct {
	Title       string `json:"title"`
	Content     string `json:"content"`
	Link        string `json:"link"`
	Media       string `json:"media"`
	Icon        string `json:"icon"`
	Refer       string `json:"refer"`
	PublishDate string `json:"publish_date"`
}

// Response is a non-stream completion result.
type Response struct {
	Content          string
	ReasoningContent string
	ToolCalls        []ToolCall
	FinishReason     string
	WebSearch        []WebSearchResult
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

type providerFactory func(Options) (Completer, error)

type factoryRegistry struct{ factories map[string]providerFactory }

func newFactoryRegistry() *factoryRegistry {
	return &factoryRegistry{factories: map[string]providerFactory{}}
}

func (r *factoryRegistry) register(name string, factory providerFactory) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, ok := providerregistry.Lookup(name); !ok {
		return fmt.Errorf("provider factory %q has no descriptor", name)
	}
	if factory == nil {
		return fmt.Errorf("provider factory %q is nil", name)
	}
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("provider factory %q already registered", name)
	}
	r.factories[name] = factory
	return nil
}

func (r *factoryRegistry) lookup(name string) (providerFactory, bool) {
	factory, ok := r.factories[strings.ToLower(strings.TrimSpace(name))]
	return factory, ok
}

func (r *factoryRegistry) names() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var (
	builtinFactories *factoryRegistry
	builtinsOnce     sync.Once
	builtinsErr      error
)

func registerBuiltins() error {
	builtinsOnce.Do(func() {
		registry := newFactoryRegistry()
		if err := registry.register("deepseek", NewDeepSeek); err != nil {
			builtinsErr = err
			return
		}
		if err := registry.register("openrouter", NewOpenRouter); err != nil {
			builtinsErr = err
			return
		}
		builtinFactories = registry
	})
	return builtinsErr
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
	if err := registerBuiltins(); err != nil {
		return nil, err
	}
	factory, ok := builtinFactories.lookup(res.ProviderName)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (available: %s)", res.ProviderName, strings.Join(builtinFactories.names(), ", "))
	}
	return factory(opts)
}
