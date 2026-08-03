# Phase 03 - Model configuration

Files:

- Modify `internal/config/types.go` (`ModelSpec`, its closed `UnmarshalTOML`, and the provider-scoped post-decode check).
- Modify/extend the focused config tests.

## Surface

```go
type ModelSpec struct {
	Name                string
	ContextWindowTokens int
	MaxOutputTokens     int
	// Reasoning is this model's reasoning dial. Empty sends no field.
	Reasoning reasoning.Level `toml:"reasoning,omitempty"`
	// ReasoningDialect is this model's wire dialect. Empty uses the
	// provider's vetted default, where one exists.
	ReasoningDialect reasoning.Dialect `toml:"reasoning_dialect,omitempty"`
}
```

`UnmarshalTOML` gains `reasoning` and `reasoning_dialect` cases. Both must be
TOML strings, both are validated through `internal/reasoning`, and any other
key still hard-errors.

## Provider-scoped validation

`ModelSpec` cannot see its provider during decode, so the check runs where the
provider groups are resolved: a model with an active `reasoning` level, no
explicit `reasoning_dialect`, and no `reasoning.DefaultDialect(provider)` is a
configuration error naming both the model and the key to set. This is what
stops `reasoning = "high"` from silently doing nothing on DeepSeek.

`reasoning_dialect` without `reasoning` is accepted: it declares capability for
a model that is currently dialled off.

## Tests first

- A model object with `reasoning = "high"` on zai decodes and validates.
- `reasoning_dialect = "thinking_effort"` decodes.
- An invalid level and an invalid dialect are both rejected.
- An unknown key is still rejected.
- `reasoning` on deepseek without `reasoning_dialect` fails to load, and the
  error names the model and the key.
- The same deepseek model with an explicit `reasoning_dialect` loads.
- A model with neither key round-trips to the zero values.

Gate: `go test ./internal/config/...`.
