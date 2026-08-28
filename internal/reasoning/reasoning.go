// Package reasoning is the provider-neutral vocabulary for model reasoning
// control: how hard a model should think, and which wire dialect expresses
// that to its provider.
//
// It deliberately imports nothing outside the standard library. Both
// internal/config and internal/provider depend on it, and provider already
// imports config, so any dependency in the other direction would be a cycle.
package reasoning

import (
	"fmt"
	"strconv"
	"strings"
)

// Level is the provider-neutral reasoning dial for one model. The empty Level
// means unset: no reasoning field is sent at all, which is the required shape
// for a non-reasoning model. Off is different - it is an explicit instruction
// to disable thinking, and each dialect has a documented way to say that.
type Level string

// The closed set of levels. A model that does not accept one of these gets a
// 400 from its provider naming the values it does accept; embedding a
// per-model matrix here would rot on every model release.
const (
	Off     Level = "off"
	Minimal Level = "minimal"
	Low     Level = "low"
	Medium  Level = "medium"
	High    Level = "high"
	XHigh   Level = "xhigh"
	Max     Level = "max"
	// Auto delegates depth selection to the provider. Some endpoints (e.g.
	// proxied Qwen3.8-flash) reject every graded tier and accept only
	// "auto"; a model entry restricted to [Auto] states exactly that.
	Auto Level = "auto"
)

var levels = map[Level]struct{}{
	Off: {}, Minimal: {}, Low: {}, Medium: {}, High: {}, XHigh: {}, Max: {}, Auto: {},
}

// Active reports whether this level instructs the provider at all. Only the
// empty level is inactive; Off is an active instruction to disable thinking.
func (l Level) Active() bool { return l != "" }

// ParseLevel validates a configured level. The empty string is accepted and
// means unset. Matching is exact: every other closed TOML object in this repo
// is spelling-strict, and one forgiving key would be a surprising exception.
func ParseLevel(s string) (Level, error) {
	if s == "" {
		return "", nil
	}
	level := Level(s)
	if _, ok := levels[level]; !ok {
		return "", fmt.Errorf("unknown reasoning level %q (want off, minimal, low, medium, high, xhigh, max, or auto)", s)
	}
	return level, nil
}

// Dialect names the wire shape a provider expects for reasoning control. The
// same Level reaches different providers as different JSON.
type Dialect string

const (
	// DialectOpenAI sends a top-level reasoning_effort string.
	DialectOpenAI Dialect = "openai"
	// DialectOpenRouter sends OpenRouter's canonical nested reasoning object.
	DialectOpenRouter Dialect = "openrouter"
	// DialectOpenRouterOnOff sends OpenRouter's canonical reasoning object
	// carrying ONLY enabled true/false - for models with no reasoning_effort
	// surface (e.g. poolside/laguna-s-2.1).
	DialectOpenRouterOnOff Dialect = "openrouter_onoff"
	// DialectThinking sends a thinking object gating the mode on or off.
	DialectThinking Dialect = "thinking"
	// DialectThinkingEffort sends the thinking object plus reasoning_effort,
	// the shape GLM-5.2+ and DeepSeek v4-pro accept for graded depth.
	DialectThinkingEffort Dialect = "thinking_effort"
	// DialectThinkingPreserved sends a thinking object with clear_thinking:false
	// when enabled (z.ai Preserved Thinking). Model-entry opt-in; the factory
	// default stays DialectThinking so standard-PaaS users stay byte-identical.
	// Graded depth is carried the same way as thinking_effort (reasoning_effort
	// alongside the thinking object) so GLM-5.2 multi-level sets remain valid.
	DialectThinkingPreserved Dialect = "thinking_preserved"
	// DialectNone declares that this model has no reasoning surface. It is
	// distinct from unset: it is a deliberate statement, not a missing key.
	DialectNone Dialect = "none"
	// DialectAnthropicAdaptive sends Anthropic's native adaptive-thinking
	// shape: a top-level thinking object (type "adaptive", or "disabled" for
	// Off) plus output_config.effort carrying the graded level. Distinct from
	// DialectThinking (whose thinking object only ever toggles on/off) and
	// from DialectThinkingEffort (whose effort rides a top-level
	// reasoning_effort field): Anthropic's effort is nested under
	// output_config, and its "on" thinking type is "adaptive", not
	// "enabled". See internal/provider/reasoning.go's reasoningBodyFields,
	// which this dialect's wire shape is encoded in alongside every other
	// dialect - there is one encoder, not a second one to keep in step.
	DialectAnthropicAdaptive Dialect = "anthropic_adaptive"
)

var dialects = map[Dialect]struct{}{
	DialectOpenAI: {}, DialectOpenRouter: {}, DialectOpenRouterOnOff: {},
	DialectThinking: {}, DialectThinkingEffort: {}, DialectThinkingPreserved: {},
	DialectNone: {}, DialectAnthropicAdaptive: {},
}

// ParseDialect validates a configured dialect. The empty string is accepted
// and means "use the provider's vetted default, if it has one".
func ParseDialect(s string) (Dialect, error) {
	if s == "" {
		return "", nil
	}
	dialect := Dialect(s)
	if _, ok := dialects[dialect]; !ok {
		return "", fmt.Errorf("unknown reasoning dialect %q (want openai, openrouter, openrouter_onoff, thinking, thinking_effort, thinking_preserved, anthropic_adaptive, or none)", s)
	}
	return dialect, nil
}

// CanGrade reports whether this dialect can put DEPTH on the wire, as opposed
// to only switching thinking on or off. DialectThinking cannot: its body is a
// thinking object with one of two types, so every non-Off level it carries
// produces byte-identical JSON. DialectOpenRouterOnOff cannot either: its wire
// body carries only enabled true/false. Config uses this to refuse a model that offers
// graded levels its dialect would flatten, which would leave /effort reporting
// a change the request never made.
//
// It lives beside the Dialect type rather than beside the request encoder in
// internal/provider because internal/config must consult it, and config cannot
// import provider without a cycle. Keep it in step with
// provider.reasoningBodyFields.
func (d Dialect) CanGrade() bool {
	switch d {
	case DialectOpenAI, DialectOpenRouter, DialectThinkingEffort, DialectThinkingPreserved, DialectAnthropicAdaptive:
		return true
	case DialectOpenRouterOnOff:
		return false
	default:
		return false
	}
}

// defaultDialects holds only providers whose wire shape this repo has verified
// against current official documentation. DeepSeek uses thinking_effort (the
// thinking object plus top-level reasoning_effort) and requires
// reasoning_content replay, which the provider client implements via
// RequiresReasoningReplay. LLM Gateway's OpenAI-compatible surface accepts the
// top-level reasoning_effort shorthand and never downgrades effort tiers
// (https://docs.llmgateway.io/features/reasoning).
var defaultDialects = map[string]Dialect{
	"zai":         DialectThinking,
	"openrouter":  DialectOpenAI,
	"deepseek":    DialectThinkingEffort,
	"llmgateway":  DialectOpenAI,
	"llmproxycli": DialectOpenAI,
	"anthropic":   DialectAnthropicAdaptive,
}

// DefaultDialect returns the vetted wire dialect for a built-in provider.
// ok=false means the provider has no default and an active level there must
// name its reasoning_dialect explicitly. Matching is exact, so an unexpected
// spelling fails closed rather than guessing a wire shape.
func DefaultDialect(provider string) (Dialect, bool) {
	dialect, ok := defaultDialects[provider]
	return dialect, ok
}

// anthropicNativeCapableProviders lists provider names whose client can
// actually speak Anthropic's native wire format when a model entry sets
// reasoning_dialect = "anthropic_adaptive": the anthropic provider itself,
// and llmproxycli, whose factory (internal/provider/llmproxycli.go) builds a
// per-model dispatcher for any model that opts in, reusing llmproxycli's own
// base_url/api_key_env rather than requiring a separate [providers.anthropic]
// block. This is a capability allow-list, not a provider default:
// DialectAnthropicAdaptive is never llmproxycli's own default dialect
// (defaultDialects keeps that as DialectOpenAI) - only an explicit
// reasoning_dialect on one model entry opts that one model in.
//
// Every dialect other than DialectAnthropicAdaptive validates purely through
// ParseDialect/CanGrade; this one is gated separately because its wire shape
// is not just a different field on the same request body (like every other
// dialect here) but a structurally different request entirely - system
// prompt at the top level, content blocks instead of a flat message string,
// no OpenAI-compatible envelope at all. A provider whose client has no
// adapter for that shape cannot deliver it no matter what the config says,
// the same "looks applied, does nothing" failure
// checkReasoningIsDeliverable's own doc comment already guards against for
// every other dialect - except here, absent this gate, the failure mode was
// worse: not "sends nothing" but "sends a malformed request."
var anthropicNativeCapableProviders = map[string]struct{}{
	"anthropic":   {},
	"llmproxycli": {},
}

// CanCarryDialect reports whether provider's client can actually deliver
// dialect's wire shape. Every dialect but DialectAnthropicAdaptive always
// returns true here - see anthropicNativeCapableProviders for why that one
// is different. internal/config's checkReasoningIsDeliverable calls this
// after resolving the dialect, so a model entry naming a dialect its
// provider's client cannot speak is rejected at config-load time instead of
// reaching a provider client that would marshal something the wire never
// defined.
func CanCarryDialect(provider string, dialect Dialect) bool {
	if dialect != DialectAnthropicAdaptive {
		return true
	}
	_, ok := anthropicNativeCapableProviders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

// Setting is one model's resolved reasoning configuration, carried together so
// the many request paths thread one value instead of two parallel fields that
// can drift apart.
type Setting struct {
	Level   Level
	Dialect Dialect
}

// Active reports whether this setting instructs the provider. A Dialect alone
// declares a capability for a model that is currently dialled off and sends
// nothing on its own.
func (s Setting) Active() bool { return s.Level.Active() }

// Resolve returns the setting with the dialect the wire will actually carry:
// the configured one when the model named it, otherwise the provider's vetted
// default. An empty dialect on the way out means the provider has no default
// and this setting sends nothing, which callers must treat as such rather than
// guessing a wire shape.
//
// This is the only implementation of that sequencing, and it lives here for the
// reason Dialect.CanGrade does: internal/config validates it, internal/provider
// encodes it, and internal/chat reports it, but config cannot import provider
// without a cycle. Every copy of the rule is a chance for the request path and
// the surface describing it to disagree about what was sent.
func Resolve(provider string, s Setting) Setting {
	if s.Dialect != "" {
		return s
	}
	if dialect, ok := DefaultDialect(provider); ok {
		s.Dialect = dialect
	}
	return s
}

// FormatLevels renders a declared set for a UI line. It lives here because the
// session's refusal message and the CLI picker need the same rendering, and two
// private copies of a join is exactly how they end up disagreeing.
//
// Its input is always a catalog set, which load has already validated against
// the closed level vocabulary. Anything rendering values that have NOT cleared
// that gate must use FormatLevelsQuoted.
func FormatLevels(levels []Level) string {
	names := make([]string, 0, len(levels))
	for _, level := range levels {
		names = append(names, string(level))
	}
	return strings.Join(names, ", ")
}

// FormatLevelsQuoted is the same rendering with every element quoted, for
// config load errors. Those print straight to stderr and may carry a level
// exactly as the operator typed it, so an unescaped ANSI sequence in a TOML
// string would recolour or clear the reader's terminal.
func FormatLevelsQuoted(levels []Level) string {
	names := make([]string, 0, len(levels))
	for _, level := range levels {
		names = append(names, strconv.Quote(string(level)))
	}
	return strings.Join(names, ", ")
}

// OutputReserveFloor is the conservative per-effort-level output-token
// stand-in used whenever a computation must reserve room for a completion
// but has no explicit token count to use. It lives here, not in
// internal/provider or internal/config, because BOTH must read the exact
// same number for the same level:
//   - internal/provider's wire request layer (effectiveMaxTokens in
//     openai_compat_request.go) uses it as the max_tokens sent on the wire
//     when a request leaves MaxTokens unset, so an always-thinking model
//     (e.g. z.ai's GLM-5.3 family) does not burn a small provider-side
//     default entirely on reasoning tokens before producing any answer.
//   - internal/config's prompt-budget layer (EffectiveOutputTokens in
//     prompt_budget.go) must reserve AT LEAST this much context-window room
//     for the completion before packing history, or the wire request above
//     can ask for more completion tokens than the budget left room for,
//     risking a prompt_tokens+max_tokens over-context-window rejection.
//
// internal/provider cannot depend on internal/config for this (provider
// already imports config, so the reverse would cycle), which is exactly why
// this package - a leaf both already depend on - is the single source of
// truth instead of either duplicating the table or one delegating to the
// other. This heuristic is unverified against live traffic for every
// provider and should be tuned as real numbers come in.
func OutputReserveFloor(level Level) int {
	switch level {
	case XHigh, Max:
		return 65536
	case High:
		return 32768
	case Medium:
		return 16384
	case Low, Minimal:
		return 8192
	default:
		// Off, or no reasoning level configured: a plain non-thinking turn
		// needs headroom for the response only.
		return 4096
	}
}
