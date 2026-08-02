// Package reasoning is the provider-neutral vocabulary for model reasoning
// control: how hard a model should think, and which wire dialect expresses
// that to its provider.
//
// It deliberately imports nothing outside the standard library. Both
// internal/config and internal/provider depend on it, and provider already
// imports config, so any dependency in the other direction would be a cycle.
package reasoning

import "fmt"

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
)

var levels = map[Level]struct{}{
	Off: {}, Minimal: {}, Low: {}, Medium: {}, High: {}, XHigh: {}, Max: {},
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
		return "", fmt.Errorf("unknown reasoning level %q (want off, minimal, low, medium, high, xhigh, or max)", s)
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
	// DialectThinking sends a thinking object gating the mode on or off.
	DialectThinking Dialect = "thinking"
	// DialectThinkingEffort sends the thinking object plus reasoning_effort,
	// the shape GLM-5.2+ and DeepSeek v4-pro accept for graded depth.
	DialectThinkingEffort Dialect = "thinking_effort"
	// DialectNone declares that this model has no reasoning surface. It is
	// distinct from unset: it is a deliberate statement, not a missing key.
	DialectNone Dialect = "none"
)

var dialects = map[Dialect]struct{}{
	DialectOpenAI: {}, DialectOpenRouter: {}, DialectThinking: {},
	DialectThinkingEffort: {}, DialectNone: {},
}

// ParseDialect validates a configured dialect. The empty string is accepted
// and means "use the provider's vetted default, if it has one".
func ParseDialect(s string) (Dialect, error) {
	if s == "" {
		return "", nil
	}
	dialect := Dialect(s)
	if _, ok := dialects[dialect]; !ok {
		return "", fmt.Errorf("unknown reasoning dialect %q (want openai, openrouter, thinking, thinking_effort, or none)", s)
	}
	return dialect, nil
}

// defaultDialects holds only providers whose wire shape this repo has verified
// against current official documentation AND whose reasoning mode needs no
// request-history support we do not implement.
//
// DeepSeek is deliberately absent: its thinking mode expects reasoning_content
// to be replayed on subsequent tool-call turns, and provider.Message does not
// preserve that field, so a defaulted dialect would break multi-step tool
// turns. An operator may still opt in by naming reasoning_dialect explicitly.
var defaultDialects = map[string]Dialect{
	"zai":        DialectThinking,
	"openrouter": DialectOpenAI,
}

// DefaultDialect returns the vetted wire dialect for a built-in provider.
// ok=false means the provider has no default and an active level there must
// name its reasoning_dialect explicitly. Matching is exact, so an unexpected
// spelling fails closed rather than guessing a wire shape.
func DefaultDialect(provider string) (Dialect, bool) {
	dialect, ok := defaultDialects[provider]
	return dialect, ok
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
