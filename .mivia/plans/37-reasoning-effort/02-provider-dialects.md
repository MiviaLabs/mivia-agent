# Phase 02 - Provider dialects and request shaping

Files:

- Create `internal/provider/reasoning.go` and `internal/provider/reasoning_test.go`.
- Modify `internal/provider/provider.go` (`Request`).
- Modify `internal/provider/openai_compat.go` (`CompatOptions`, `newRequest`, `readStream` fallback).
- Modify `internal/provider/{deepseek,zai,openrouter}.go`.

## Dialect surface

```go
// reasoningDialect maps a level to the wire fields one provider expects.
// Nil result means: send nothing.
type reasoningDialect interface {
	bodyFields(reasoning.Level) map[string]any
}

func dialectFor(name reasoning.Dialect) reasoningDialect // nil for "" and "none"
```

Mappings (level `""` always yields nil in every dialect):

| Dialect | `off` | graded level `L` |
|---|---|---|
| `openai` | `{"reasoning_effort":"none"}` | `{"reasoning_effort":"L"}` |
| `openrouter` | `{"reasoning":{"enabled":false}}` | `{"reasoning":{"effort":"L"}}` |
| `thinking` | `{"thinking":{"type":"disabled"}}` | `{"thinking":{"type":"enabled"}}` |
| `thinking_effort` | `{"thinking":{"type":"disabled"}}` | `{"thinking":{"type":"enabled"},"reasoning_effort":"L"}` |

**There is no sampling suppression.** DeepSeek documents that sampling settings
are accepted and ignored in thinking mode, Z.AI's own active-thinking example
sends `temperature`, and OpenRouter advertises sampling support per model.
Removing `temperature` would change valid requests for no benefit.

## Request and options

```go
type Request struct {
	// ...
	// ReasoningLevel is the model-scoped reasoning dial. Empty sends nothing.
	ReasoningLevel reasoning.Level
	// ReasoningDialect overrides the client's default wire dialect. Empty
	// falls back to the client default; unresolvable means nothing is sent.
	ReasoningDialect reasoning.Dialect
}

type CompatOptions struct {
	// ...
	// Reasoning is the client's default wire dialect, used when a request
	// carries a level but no explicit dialect. Empty means none.
	Reasoning reasoning.Dialect
}
```

Constructors: `NewZAI` -> `reasoning.DialectThinking`,
`NewOpenRouter` -> `reasoning.DialectOpenAI`, `NewDeepSeek` -> unset.

## Body merge

In `newRequest`, after `ExtraBody` is merged:

```go
if fields := c.reasoningFields(req); fields != nil {
	for k, v := range fields { body[k] = v }
}
```

Reasoning is last, so an active model-scoped level deterministically wins over
a static `ExtraBody` key. Nothing is deleted, and `req` is never mutated.

The non-streaming fallback inside `readStream` rebuilds a `Request` from
scratch; it must copy both reasoning fields or a fallback silently downgrades
the model.

## Tests first

- Each dialect x {unset, off, every graded level} produces the table above.
- `dialectFor("")` and `dialectFor("none")` are nil and emit nothing.
- Request-level dialect overrides the client default; client default applies
  when the request omits one.
- Unset level: the serialized body is byte-identical to the pre-change body,
  including `temperature`.
- Active level: `temperature` is still present and unchanged.
- Active reasoning overrides a colliding `ExtraBody` key.
- Building a request does not mutate the caller's `Request`.
- The `readStream` fallback carries both reasoning fields.

Gate: `go test ./internal/provider/...` and `go test -race ./internal/provider/...`.
