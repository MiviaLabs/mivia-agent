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
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
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
	// ReasoningContent is the model's chain-of-thought for this turn, preserved
	// verbatim on the assistant message so providers whose thinking mode requires
	// replay (DeepSeek v4, z.ai preserved thinking) can get it back on subsequent
	// tool-call turns. Empty for non-reasoning models and for non-assistant roles.
	// Persisted in session history; only ever re-emitted on the wire by providers
	// that declare the replay capability (CompatOptions.RequiresReasoningReplay).
	// Counted by the token estimators so prompt budgets see it.
	ReasoningContent string `json:"reasoning_content,omitempty"`
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
	// Tool-call argument fragments are not written here - only assistant text.
	StreamWriter io.Writer
	Tools        []ToolSpec
	ToolChoice   string // "auto", "none", or empty
	Timeout      time.Duration
	// DisableProviderReplay prevents transport and protocol fallbacks from
	// issuing a second provider request for this logical attempt.
	DisableProviderReplay bool
	// ReasoningLevel is the selected model's reasoning dial. Empty sends no
	// reasoning field at all, which is the required shape for a non-reasoning
	// model, and leaves the request body byte-identical to a pre-reasoning one.
	ReasoningLevel reasoning.Level
	// ReasoningDialect overrides the client's default wire dialect for this
	// request. Empty falls back to the client default; when neither resolves,
	// nothing is sent rather than a guessed wire shape.
	ReasoningDialect reasoning.Dialect
	// SDKReasoningEffort is the SDK-shaped reasoning effort for one request,
	// produced by sdkadapter.LevelToReasoningEffort(ReasoningLevel). Empty
	// string means "no SDK surface" (the user picked a level the SDK cannot
	// carry on the wire, or the request is unset). Currently unused by any
	// consumer in the tree; reserved for B.2 #8's SDK-backed inner loop.
	SDKReasoningEffort sdkshape.ReasoningEffort `json:"sdk_reasoning_effort,omitempty"`
	// SessionID is the caller's session/run identifier, threaded through
	// unchanged from whatever principal issued this turn (chat session,
	// delegated subagent, workflow step). Empty means unknown - only a
	// client that opts into session-keyed routing (CompatOptions.
	// SendSessionUserKey) ever reads it, and only then does it reach the
	// wire, hashed rather than sent verbatim.
	SessionID string
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
	// CacheUsage is provider-reported prompt-cache accounting for this turn.
	// Its zero value (Reported=false) means the provider reported nothing
	// recognized, not that the cache was missed.
	CacheUsage CacheUsage
	// TokenUsage is provider-reported input/output token counts for this turn.
	TokenUsage TokenUsage
}

// Completer talks to an LLM provider.
type Completer interface {
	Name() string
	ChatStream(ctx context.Context, req Request, w io.Writer) (string, error)
	Chat(ctx context.Context, req Request) (string, error)
	// ChatTurn is a non-stream turn that may return tool_calls.
	ChatTurn(ctx context.Context, req Request) (*Response, error)
}

// ContextAccountingAware is an optional Completer capability: a client that
// knows how its provider bills context (see ContextAccountingProfile). It is
// a separate interface, not a Completer method, so the many test fakes that
// implement Completer without it keep compiling; ContextAccountingFor below
// treats a Completer that does not implement it exactly like the conservative
// zero-value profile.
type ContextAccountingAware interface {
	ContextAccounting() ContextAccountingProfile
}

// ContextAccountingFor returns c's declared context-billing profile, or the
// conservative zero-value profile (bill everything) when c is nil or does
// not implement ContextAccountingAware.
func ContextAccountingFor(c Completer) ContextAccountingProfile {
	aware, ok := c.(ContextAccountingAware)
	if !ok {
		return ContextAccountingProfile{}
	}
	return aware.ContextAccounting()
}

// ReasoningPolicy is a client's reasoning-replay wire contract, mirroring the
// CompatOptions bits an OpenAI-compatible client was constructed with.
type ReasoningPolicy struct {
	// RequiresReplay reports whether this client's dialect requires assistant
	// reasoning_content to be echoed back verbatim on later tool-call turns.
	RequiresReplay bool
	// RejectReasoningLess reports whether this client's provider 400s on a
	// tools-carrying request that includes a reasoning-less tool-call turn
	// (see RepairReasoningLessToolExchanges).
	RejectReasoningLess bool
}

// ReasoningPolicyAware is an optional Completer capability: a client that
// knows its own reasoning-replay wire contract. Separate from Completer for
// the same reason as ContextAccountingAware - test fakes implementing
// Completer alone keep compiling.
type ReasoningPolicyAware interface {
	ReasoningPolicy() ReasoningPolicy
}

// ReasoningPolicyFor returns c's declared reasoning policy, or the zero value
// (no replay, no reject) when c is nil or does not implement
// ReasoningPolicyAware.
func ReasoningPolicyFor(c Completer) ReasoningPolicy {
	aware, ok := c.(ReasoningPolicyAware)
	if !ok {
		return ReasoningPolicy{}
	}
	return aware.ReasoningPolicy()
}

// Options for constructing a completer from resolved config.
type Options struct {
	Name        string
	BaseURL     string
	APIKey      string
	Model       string
	HTTPReferer string
	XTitle      string
	// CacheUsageEnabled gates capture of provider-reported prompt-cache usage
	// accounting. It never changes what is sent to the provider.
	CacheUsageEnabled bool
	// CacheMarkersEnabled requests explicit cache_control markers on the
	// stable prefix for providers whose upstream honors them (OpenRouter
	// forwards them to Anthropic-family models; models without explicit
	// caching ignore the marker). Factories for providers that only cache
	// implicitly (deepseek, zai, ollama) ignore this option so their request
	// bodies stay byte-identical.
	CacheMarkersEnabled bool
	// ContextWindowTokens is the configured model's declared context capacity
	// (config.ModelSpec.ContextWindowTokens for the resolved model name), or 0
	// if the model is unrecognized. Only consumed by providers whose server
	// does not infer context length from the model name on its own (ollama's
	// num_ctx); other factories ignore it.
	ContextWindowTokens int
	// ReasoningDialect is the resolved model's effective wire dialect
	// (config.ModelSpec.ReasoningDialect for the resolved model name, falling
	// back to the provider's own vetted default when the model entry sets
	// none - see reasoningDialectFor). Only a factory whose provider serves a
	// caller-chosen, heterogeneous model set needs this: a single-vendor
	// factory (deepseek, zai) already knows its own dialect and ignores this
	// field. llmgateway reads it to decide, per model, whether the upstream
	// speaks a DeepSeek-style thinking dialect that requires the matching
	// reasoning-replay and reasoning-less-tool-turn-reject wire contract.
	ReasoningDialect reasoning.Dialect
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

// builtinEntry pairs a provider name with the factory that builds it.
type builtinEntry struct {
	name    string
	factory providerFactory
}

var builtins = []builtinEntry{
	{"deepseek", NewDeepSeek},
	{"openrouter", NewOpenRouter},
	{"zai", NewZAI},
	{"ollama", NewOllama},
	{"llmgateway", NewLLMGateway},
	{"minimax", NewMiniMax},
}

// registerAll registers each entry in order, stopping at the first error.
func registerAll(registry *factoryRegistry, entries []builtinEntry) error {
	for _, entry := range entries {
		if err := registry.register(entry.name, entry.factory); err != nil {
			return err
		}
	}
	return nil
}

func registerBuiltins() error {
	builtinsOnce.Do(func() {
		registry := newFactoryRegistry()
		builtinsErr = registerAll(registry, builtins)
		if builtinsErr == nil {
			builtinFactories = registry
		}
	})
	return builtinsErr
}

// New builds a Completer from resolved config.
func New(res *config.Resolved) (Completer, error) {
	if res == nil {
		return nil, fmt.Errorf("nil config")
	}
	return NewForProvider(res, res.ProviderName)
}

// NewForProvider builds the configured backend for one provider without
// mutating the active session. Runtime records are resolved by config.Load;
// the compatibility projection supports hand-built Resolved values in tests.
func NewForProvider(res *config.Resolved, providerName string) (Completer, error) {
	if res == nil {
		return nil, fmt.Errorf("nil config")
	}
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	if err := registerBuiltins(); err != nil {
		return nil, err
	}
	runtime, ok := res.ProviderRuntimes[providerName]
	if !ok && providerName == strings.ToLower(strings.TrimSpace(res.ProviderName)) {
		runtime = config.ProviderRuntime{
			ProviderName: res.ProviderName, BaseURL: res.BaseURL, APIKey: res.APIKey,
			APIKeyEnv: res.APIKeyEnv, APIKeySet: res.APIKeySet,
			HTTPReferer: res.HTTPReferer, XTitle: res.XTitle, Models: res.ModelProfiles,
		}
		ok = true
	}
	if !ok {
		if _, supported := builtinFactories.lookup(providerName); !supported {
			return nil, fmt.Errorf("unsupported provider %q (available: %s)", providerName, strings.Join(builtinFactories.names(), ", "))
		}
		return nil, fmt.Errorf("provider %q is not configured", providerName)
	}
	if !runtime.APIKeySet || strings.TrimSpace(runtime.APIKey) == "" {
		if !(providerName == "ollama" && config.IsOllamaLoopback(runtime.BaseURL)) {
			return nil, fmt.Errorf("missing API key for provider %q", providerName)
		}
	}
	contextWindowTokens := contextWindowTokensFor(runtime.Models, res.Model)
	opts := Options{
		Name:                runtime.ProviderName,
		BaseURL:             runtime.BaseURL,
		APIKey:              runtime.APIKey,
		Model:               res.Model,
		HTTPReferer:         runtime.HTTPReferer,
		XTitle:              runtime.XTitle,
		CacheUsageEnabled:   res.PromptCache != "off",
		CacheMarkersEnabled: res.PromptCache != "off",
		ContextWindowTokens: contextWindowTokens,
		ReasoningDialect:    reasoningDialectFor(runtime.Models, res.Model, providerName),
	}
	factory, ok := builtinFactories.lookup(providerName)
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q (available: %s)", providerName, strings.Join(builtinFactories.names(), ", "))
	}
	return factory(opts)
}

// reasoningDialectFor returns the resolved model's effective reasoning
// dialect: the model entry's own override when it declares one, otherwise
// the provider's vetted default (mirrors reasoning.Resolve, which
// internal/config cannot call directly without an import cycle). Returns ""
// when the model is unrecognized and the provider has no default.
func reasoningDialectFor(models []config.ModelSpec, name, providerName string) reasoning.Dialect {
	for _, model := range models {
		if model.Name == name {
			return reasoning.Resolve(providerName, reasoning.Setting{Dialect: model.ReasoningDialect}).Dialect
		}
	}
	return reasoning.Resolve(providerName, reasoning.Setting{}).Dialect
}

// contextWindowTokensFor returns the declared context capacity of the model
// named in the catalog. It returns 0 when the name is absent. The match is
// exact, and the first match wins. Catalogs are small, and NewForProvider
// runs once per client construction, so a linear scan is the right shape.
func contextWindowTokensFor(models []config.ModelSpec, name string) int {
	for _, model := range models {
		if model.Name == name {
			return model.ContextWindowTokens
		}
	}
	return 0
}
