# Phase 01 - Shared reasoning vocabulary

Files:

- Create `internal/reasoning/reasoning.go`.
- Create `internal/reasoning/reasoning_test.go`.

The package is dependency-light: it imports only the standard library, so both
`internal/config` and `internal/provider` can depend on it without a cycle
(`provider` already imports `config`).

## Surface

```go
type Level string

const (
	Off     Level = "off"
	Minimal Level = "minimal"
	Low     Level = "low"
	Medium  Level = "medium"
	High    Level = "high"
	XHigh   Level = "xhigh"
	Max     Level = "max"
)

// ParseLevel accepts the empty string as "unset".
func ParseLevel(s string) (Level, error)
func (l Level) Active() bool // non-empty

type Dialect string

const (
	DialectOpenAI         Dialect = "openai"          // reasoning_effort: "<level>"
	DialectOpenRouter     Dialect = "openrouter"      // reasoning: {"effort": "<level>"}
	DialectThinking       Dialect = "thinking"        // thinking: {"type": "enabled"|"disabled"}
	DialectThinkingEffort Dialect = "thinking_effort" // thinking object + reasoning_effort
	DialectNone           Dialect = "none"            // provider has no reasoning surface
)

func ParseDialect(s string) (Dialect, error)

// DefaultDialect is the vetted wire dialect for a built-in provider. ok=false
// means the provider requires an explicit reasoning_dialect on the model entry.
func DefaultDialect(provider string) (Dialect, bool)
```

`DefaultDialect`: `zai` -> `DialectThinking`, `openrouter` -> `DialectOpenAI`.
Every other provider, including `deepseek`, returns `ok=false`.

## Tests first

- Every named level parses; empty parses to unset; an arbitrary string is
  rejected with a stable message.
- Every named dialect parses; empty parses to unset; garbage is rejected.
- `DefaultDialect` returns the documented pair for zai/openrouter and
  `ok=false` for deepseek and for an unknown provider name.
- `Active()` is false only for the empty level.

Gate: `go test ./internal/reasoning/...`.
