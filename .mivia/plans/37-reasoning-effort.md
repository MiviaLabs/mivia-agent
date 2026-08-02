# 37 - Reasoning control across providers (multi-dialect)

**Status:** BLOCKED - Step 0 re-audit (2026-08-02) invalidated the provider-wide
wire and sampling assumptions. Do not implement this plan as written.
**Date:** 2026-08-02 (revised 2026-08-03 after multi-provider research)
**Depends on:** `internal/provider/openai_compat.go` (`chatRequestBody`, `CompatOptions`).
**Would block:** plans 34 (xAI), 38 (OpenAI), and 31 (Kimi) if a replacement
design is approved.
**Amends:** nothing.
**Blast radius:** HIGH - changes the shared request body, model binding, nested
agent paths, and (for DeepSeek) durable multi-turn conversation state.
The original risk hypothesis was that reasoning models universally reject sampling
parameters (`temperature`/`top_p`). The re-audit disproved that hypothesis; any
replacement must preserve existing requests when reasoning is unset and apply
sampling policy only where the selected model's documented capability requires it.

## Re-audit disposition (2026-08-02)

This plan is blocked rather than partially implemented. Current official provider
documentation and a repository path audit disproved its central premise that one
provider-level dialect can safely suppress sampling for every configured model.

- **Provider/model capability is required.** DeepSeek documents that sampling
  settings are accepted and ignored in thinking mode; Z.AI's active-thinking
  example sends `temperature`; and OpenRouter exposes supported reasoning and
  sampling parameters per model. A provider-wide `SuppressSampling` rule would
  change valid requests and cannot meet this plan's stated 400-avoidance goal.
- **DeepSeek needs history support before agent-loop enablement.** Its thinking
  mode requires `reasoning_content` to be replayed on subsequent tool-call
  turns. `provider.Message`/`apiMessage` do not currently preserve that field,
  so assigning a DeepSeek reasoning dialect would make a multi-step tool turn
  fail or lose required state.
- **The proposed wire mappings are incomplete.** In particular, internal `off`
  maps to no field in the proposed OpenAI dialect, rather than the documented
  `reasoning_effort: "none"`; Z.AI GLM-5.2 needs effort control but the proposed
  constructor omits it; and OpenRouter's canonical Chat Completions form is
  `reasoning: {"effort": ...}` (the top-level field is a shorthand).
- **The propagation inventory is incomplete.** Plain chat requests are built in
  `internal/chat/context_integration.go`, not `session.go`; the stream fallback
  reconstructs a request; and one-shot, multi-step, skill, and routed-agent
  handlers create requests outside the session loop.

Re-entry requires a replacement Step 0 design that (1) chooses an explicit
per-model capability source and dialect, including disable semantics; (2) states
sampling policy per capability rather than per provider; (3) either adds tested
reasoning-history replay for DeepSeek tool turns or excludes DeepSeek from the
initial slice; (4) inventories every request constructor, including fallback and
nested-agent paths; and (5) defines deterministic precedence over `ExtraBody`
for reasoning and sampling keys. The replacement must use current official
provider docs and re-run the ADLC challenge before code is written.

---

## 0. Why this plan replaced the "single reasoning_effort field" version

The first draft assumed `reasoning_effort` (the OpenAI parameter name) was a
universal wire field. Research across six providers proved it is not. There are
**four incompatible wire dialects**, and a single string field cannot express any of
the non-OpenAI shapes. Sending OpenAI's `reasoning_effort` to z.ai or Kimi K2.x does
nothing (silently ignored) or errors. This plan defines a **provider-aware
abstraction** that maps one internal concept to the correct wire shape per provider.

## 1. The four dialects (research)

All surveyed providers are OpenAI-compatible at the `/chat/completions` level, but
they each added reasoning control with **different field names and value sets**:

| Dialect | Wire field(s) | Value shape | Providers |
|---|---|---|---|
| **openai** | `reasoning_effort` | string: `none`/`minimal`/`low`/`medium`/`high`/`xhigh`/`max` | OpenAI (GPT-5.x), xAI (grok-4.5: `low`/`medium`/`high`), DeepInfra-hosted DeepSeek, OpenRouter (Chat Completions) |
| **openrouter** | `reasoning.effort` | nested object: `{"effort": "high"}`, optional `max_tokens` | OpenRouter (Responses API / normalized), Anthropic-via-OR uses `reasoning.max_tokens` |
| **thinking-object** | `thinking.type` | `{"type": "enabled"}` / `{"type": "disabled"}`, optional `keep` | **z.ai GLM** (GLM-4.5/4.6/4.7/5.x), **DeepSeek** (v4-pro via `extra_body`), Kimi K2.5/K2.6 |
| **none** | - | reasoning always on, no parameter | Kimi K2.7-code (always thinks), Kimi K3 (`reasoning_effort` only - no `thinking`) |

### Per-provider detail

**OpenAI / xAI** - top-level `reasoning_effort` string on Chat Completions. Values
differ by model (OpenAI supports `none`→`max`; xAI grok-4.5 supports `low`/`medium`/
`high` and cannot disable). Documented in plans 38/34.

**z.ai GLM** - `thinking: {"type": "enabled" | "disabled"}`, plus
`reasoning_effort` (string) on GLM-5.2+ *only*. Critical gotcha from research: z.ai's
API **ignores** `thinking: {"type": "disabled"}` in some server paths (pi issue
#2025) - `enable_thinking: false` (the Qwen form) is what actually disables it. v1
sends the documented `thinking` object and treats "did it actually disable" as a
known gap, documented, not silently worked around.

**DeepSeek** - `reasoning_effort` (string, `high`/`max`) **plus**
`thinking: {"type": "enabled"}` via `extra_body`. Both fields are sent together on
v4-pro; the `thinking` object gates the mode, `reasoning_effort` controls depth.

**Kimi (Moonshot)** - **model-dependent**:
- `kimi-k3`: top-level `reasoning_effort` (`low`/`high`/`max`), no `thinking` field
- `kimi-k2.6`: `thinking: {"type": "enabled"|"disabled"}`, no `reasoning_effort`
- `kimi-k2.7-code`: always thinks; only `{"type":"enabled","keep":"all"}` accepted
- `kimi-k2.5`: `thinking: {"type": "enabled"|"disabled"}`, simpler

**OpenRouter** - accepts **both** forms. Chat Completions: top-level
`reasoning_effort` (translated per-model). Responses: nested `reasoning.effort`.
OpenRouter normalizes across providers, so `reasoning_effort: "high"` reaches grok,
o-series, and Gemini correctly.

## 2. The internal abstraction

A single internal concept - **a reasoning level** - that each provider maps to its
wire dialect. The model chooses the dial (off / level); the provider chooses the wire
shape.

### 2a. The level type

The shared level type must not be declared in `internal/provider` and imported by
`internal/config`: `provider` already imports `config`, so that direction would
create an import cycle. Put the provider-neutral type in a dependency-light
package (recommended: `internal/reasoning`) and have both `config` and `provider`
depend on it. Provider may expose a type alias for its request-facing API if that
improves call-site ergonomics, but the config package must not import provider.

```go
// ReasoningLevel is the provider-neutral reasoning dial. Empty/zero = do not
// send any reasoning field (the default for non-reasoning models). The mapping
// to a wire shape is provider-specific (see ReasoningDialect).
type ReasoningLevel string

const (
	ReasoningOff      ReasoningLevel = "off"      // disable thinking where possible
	ReasoningMinimal  ReasoningLevel = "minimal"  // fastest, fewest reasoning tokens
	ReasoningLow      ReasoningLevel = "low"
	ReasoningMedium   ReasoningLevel = "medium"
	ReasoningHigh     ReasoningLevel = "high"
	ReasoningXHigh    ReasoningLevel = "xhigh"
	ReasoningMax      ReasoningLevel = "max"
)
```

This is a closed, finite set - not an arbitrary string - so the config loader and
the `/reasoning` command can validate it once. A model that doesn't support a level
gets a 400 from the provider naming the valid set; we do not embed a per-model matrix
(that drifts on every release).

### 2b. The dialect interface

```go
// ReasoningDialect maps a provider-neutral ReasoningLevel to the wire fields
// a specific provider expects in the Chat Completions request body.
// Returns nil body fields when reasoning should not be sent (level empty,
// or the model cannot honor it).
type ReasoningDialect interface {
	// BodyFields returns the extra body keys to merge into the request, or
	// nil if no reasoning field should be sent for this level.
	BodyFields(level ReasoningLevel) map[string]any
	// SuppressSampling reports whether temperature/top_p must be omitted
	// when this level is active (reasoning models generally forbid them).
	SuppressSampling(level ReasoningLevel) bool
}
```

### 2c. The built-in dialects

```go
// openaiDialect: top-level reasoning_effort string. OpenAI, xAI, DeepInfra.
type openaiDialect struct{}
func (openaiDialect) BodyFields(l ReasoningLevel) map[string]any {
	if l == "" || l == ReasoningOff { return nil }
	return map[string]any{"reasoning_effort": string(l)}
}
func (openaiDialect) SuppressSampling(l ReasoningLevel) bool { return l != "" && l != ReasoningOff }

// thinkingDialect: thinking.type object + optional reasoning_effort (GLM-5.2+).
// z.ai, DeepSeek, Kimi K2.x.
type thinkingDialect struct{ effortToo bool } // effortToo: also send reasoning_effort (DeepSeek v4-pro, GLM-5.2)
func (d thinkingDialect) BodyFields(l ReasoningLevel) map[string]any {
	if l == "" { return nil }
	fields := map[string]any{}
	if l == ReasoningOff {
		fields["thinking"] = map[string]string{"type": "disabled"}
		return fields
	}
	fields["thinking"] = map[string]string{"type": "enabled"}
	if d.effortToo {
		fields["reasoning_effort"] = string(l)
	}
	return fields
}
func (thinkingDialect) SuppressSampling(l ReasoningLevel) bool { return l != "" && l != ReasoningOff }
```

A `nil` dialect (the default) means "this provider has no reasoning surface" -
`BodyFields` returns nil, nothing is sent. This is the safe default for any provider
that hasn't declared a dialect, and it is the exact behaviour of every existing
provider today.

## 3. Wiring into CompatOptions and the request body

### 3a. CompatOptions gains a dialect

`internal/provider/openai_compat.go`:

```go
type CompatOptions struct {
	// ... existing fields ...
	// Reasoning maps a ReasoningLevel to provider-specific wire fields.
	// Nil means no reasoning surface (the pre-reasoning default).
	Reasoning ReasoningDialect
}
```

Each provider constructor picks its dialect:
- `NewDeepSeek` → `thinkingDialect{effortToo: true}` (v4-pro)
- `NewZAI` → `thinkingDialect{effortToo: false}` (GLM; effortToo=true for GLM-5.2+ - but the factory can't know the model, so start false and document that GLM-5.2 users set reasoning_effort separately if needed)
- `NewOpenRouter` → `openaiDialect{}` (OpenRouter normalizes `reasoning_effort`)
- `NewXAI` (plan 34) → `openaiDialect{}`
- `NewOpenAI` (plan 38) → `openaiDialect{}`

### 3b. Request body construction

In `newRequest`, after the existing payload is built, the dialect merges its fields:

```go
if c.reasoning != nil && req.ReasoningLevel != "" {
	fields := c.reasoning.BodyFields(req.ReasoningLevel)
	for k, v := range fields {
		body[k] = v
	}
	if c.reasoning.SuppressSampling(req.ReasoningLevel) {
		delete(body, "temperature") // reasoning models forbid sampling params
	}
}
```

The `delete(body, "temperature")` is the correctness fix - without it, every
reasoning request against a reasoning model returns 400 because our config defaults
`temperature = 0`. The suppression is conditional on the dialect and level, so
non-reasoning requests are byte-identical to today.

### 3c. Request struct

`provider.Request` gains the level:

```go
type Request struct {
	// ... existing fields ...
	// ReasoningLevel controls reasoning depth for reasoning-capable models.
	// Empty = do not send any reasoning field (non-reasoning models reject it).
	ReasoningLevel ReasoningLevel
}
```

### 3d. Propagation - per-model, not global

**Reasoning level is a property of the model spec, not a global chat setting.**
Different models have different reasoning capabilities: GLM-5.2 accepts `max`,
GLM-4.5 only does on/off, grok-4.5 cannot disable reasoning, a non-reasoning model
must not send the field at all. A global `[chat].reasoning` would be wrong for every
model it didn't match. Putting it on `ModelSpec` means it travels with the binding
and switches automatically when the user runs `/model` or picks from the model dialog.

`internal/config/types.go` - `ModelSpec` gains the field, using the shared
provider-neutral type from `internal/reasoning`:

```go
type ModelSpec struct {
	Name                string `toml:"name"`
	ContextWindowTokens int    `toml:"context_window_tokens"`
	// Reasoning is the reasoning level for this specific model. Empty = do not
	// send any reasoning field (required for non-reasoning models). The provider's
	// dialect maps it to the correct wire shape.
	Reasoning reasoning.Level `toml:"reasoning,omitempty"`
}
```

`UnmarshalTOML` (the closed parser at `types.go:141`) gains a `reasoning` case
alongside `name` and `context_window_tokens`, validating the value against the
finite set in §2a. Unknown keys still hard-error.

`internal/chat/binding.go` already selects the active `ModelSpec` into
`ModelBinding.Profile` (`binding.go:22`, `binding.go:63-68`) and model switch
rebuilds the binding with the new profile. The field then needs explicit
propagation at both request paths; it does not reach requests automatically:

```
ModelSpec.Reasoning  →  binding.Profile  →  agent.Options.ReasoningLevel  →  Request
                                      ↘  direct chat provider.Request.ReasoningLevel
```

The agent loop reads `binding.Profile.Reasoning` when building `Options`, the same
way it reads `Profile.ContextWindowTokens` for the prompt budget
(`binding.go:229`). No per-session override state; `/model` is the switch.

## 4. Config surface - per-model

On each model entry, not in `[chat]`:

```toml
[providers.zai]
models = [
  { name = "glm-5.2", context_window_tokens = 1000000, reasoning = "max" },
  { name = "glm-5-turbo", context_window_tokens = 200000 },  # no reasoning field = none sent
]

[providers.deepseek]
models = [
  { name = "deepseek-v4-pro", context_window_tokens = 1000000, reasoning = "high" },
  { name = "deepseek-v4-flash", context_window_tokens = 1000000 },  # fast, no reasoning
]
```

A model without `reasoning` sends no field (the safe default for non-reasoning
models). Switching to a model with `reasoning = "high"` activates it for that model
only; switching back to one without it turns it off. **No `/reasoning` command, no
session-global state** - the model is the source of truth, matching how
`context_window_tokens` already works.

### 4a. Why not a `[chat].reasoning` global

Rejected. Models have incompatible value sets and capabilities:
- grok-4.5 supports only `low`/`medium`/`high` and cannot disable
- GLM-4.5 is on/off only; GLM-5.2 adds `max`/`xhigh`
- DeepSeek-v4-flash is non-reasoning and would 400 on any reasoning field
- A non-reasoning model (gpt-4o, claude-3.5) rejects the field entirely

A global value wrong for every model it doesn't match is worse than per-model.
Per-model also means the catalog (model picker, `/model`) shows which models have
reasoning configured - the user sees the capability at selection time.

## 5. What this does NOT do

- **No per-model value validation.** The valid set differs by model and drifts on
  every release. The provider API returns a clear 400 naming the valid set. Validating
  client-side would embed a matrix that rots.
- **No Responses API.** Both OpenAI and xAI have a Responses API using nested
  `reasoning.effort`. We use Chat Completions. OpenRouter is the exception - it
  accepts `reasoning_effort` on Chat Completions and normalizes it.
- **No `verbosity` parameter.** GPT-5.x supports `verbosity`. Out of scope; the
  `ExtraBody` escape hatch can carry it later.
- **No reasoning-content extraction.** Several providers return
  `reasoning_content` in the response (DeepSeek, GLM, Kimi). Capturing and surfacing
  it is a separate, additive change (`Response.ReasoningContent` already exists on
  the struct). This plan is about the *request* side only.
- **No z.ai disable workaround.** z.ai ignores `thinking: {"type":"disabled"}` on
  some server paths (research, pi #2025). v1 sends the documented field. Documenting
  the gap is honest; silently sending `enable_thinking: false` (Qwen's form) would be
  an undocumented workaround that can break without notice.

## 6. Existing-provider impact (the backwards-compat proof)

| Provider | Dialect | Today (no reasoning) | After (reasoning unset) | After (reasoning set) |
|---|---|---|---|---|
| deepseek | thinking{effortToo} | no reasoning field | **identical** | `thinking.type` + `reasoning_effort`, temp suppressed |
| z.ai | thinking | no reasoning field | **identical** | `thinking.type`, temp suppressed |
| openrouter | openai | no reasoning field | **identical** | `reasoning_effort`, temp suppressed |
| (future) xai | openai | n/a | n/a | `reasoning_effort`, temp suppressed |
| (future) openai | openai | n/a | n/a | `reasoning_effort`, temp suppressed |

The "identical" column is the invariant: when `ReasoningLevel` is empty, the request
body is byte-for-byte the same as today. Existing request-building tests and both
request call paths must gain explicit coverage; the new field remains additive for
callers that leave it empty.

## 7. Verification

- `go test ./internal/provider/...` - `reasoning_dialect_test.go`:
  - openaiDialect: `BodyFields(high)` → `{"reasoning_effort":"high"}`; off/empty → nil
  - thinkingDialect: `BodyFields(high)` → `{"thinking":{"type":"enabled"}}`; off → `{"thinking":{"type":"disabled"}}`
  - thinkingDialect{effortToo}: adds `reasoning_effort` alongside
  - nil dialect: returns nil regardless of level
  - `SuppressSampling` true for all non-empty/non-off levels
- `reasoning_request_test.go`: full request body assertions:
  - reasoning unset → byte-identical to today (temperature present)
  - reasoning high on openaiDialect → `reasoning_effort` present, `temperature` absent
  - reasoning high on thinkingDialect → `thinking` object present, `temperature` absent
  - the suppression does not mutate the caller's `Request.Temperature`
- `go test ./internal/config/...` - `ModelSpec.UnmarshalTOML` accepts `reasoning`,
  validates against the finite set, rejects unknown keys, rejects an invalid level
- `go test ./internal/chat/...` - switching to a model with `reasoning = "high"`
  sets `Options.ReasoningLevel`; switching to a model without it clears it
- `go test ./internal/agent/...` - `Options.ReasoningLevel` propagates to `Request`
- `go test -race ./...`, `go build ./...`, `go vet ./...`
- Manual: confirm a reasoning model accepts the request without 400; confirm a
  non-reasoning model (e.g. deepseek-v4-flash without reasoning) is unaffected

## 8. Invariant

A new row (next free `INV-AG-32`): *When `ReasoningLevel` is non-empty and the
provider's dialect suppresses sampling, `temperature` is not sent, because
reasoning-capable models reject sampling parameters. When `ReasoningLevel` is empty,
the request body is byte-identical to the pre-reasoning shape - no field is added, no
field is removed, regardless of dialect. A provider with a nil dialect sends no
reasoning field at any level.*

## 9. Rollback

Each dialect is a pure function; removing one reverts that provider to nil-dialect
(no reasoning field). The `ReasoningLevel` on `Request` is additive. The
temperature-suppression is gated on the dialect+level, so removing the dialect
restores today's behaviour exactly.

## 10. Sequencing

1. `internal/reasoning/reasoning.go` (new) - provider-neutral `Level`, finite
   values, validation, and tests. This package is below both config and provider
   to avoid an import cycle.
2. `internal/provider/reasoning.go` (new) - `ReasoningDialect`,
   `openaiDialect`, `thinkingDialect`, and tests; use or alias the shared level.
3. `internal/provider/provider.go` - add the shared/aliased level to `Request`
4. `internal/provider/openai_compat.go` - add `Reasoning ReasoningDialect` to
   `CompatOptions`; merge dialect fields + suppress temperature in `newRequest`
5. `internal/provider/{deepseek,zai,openrouter}.go` - set the dialect on each
   existing provider
6. `internal/config/types.go` - add `Reasoning reasoning.Level` to `ModelSpec`;
   extend the closed `UnmarshalTOML` to accept and validate the `reasoning` key
7. `internal/chat/session.go` - propagate `binding.Profile.Reasoning` into both
   direct-chat `provider.Request` construction sites
8. `internal/chat/session.go` / `internal/chat/binding.go` - propagate the active
   profile into `agent.Options` for agent turns
9. `internal/agent/loop.go` - copy `Options.ReasoningLevel` into each
   `provider.Request`
10. `.mivia/mivia.toml.example` - document `reasoning` on model entries
11. Invariant `INV-AG-32` (allocate the lowest free ID at landing time; the
    current draft number is not a permanent reservation)

The direct-chat request path is required for parity: `internal/chat/session.go`
currently constructs requests at both the plain-turn and agent-turn boundaries.
The agent loop is not the sole request builder.

Land this before plans 34/38 declare their dialects. The existing providers
(deepseek/z.ai/openrouter) gain reasoning support in step 5 - that is a real user
benefit today, not just groundwork for future providers.

## 11. Superseded validation disposition

The earlier local review was superseded by the 2026-08-02 multi-agent re-audit.
Its corrections remain useful, but it no longer establishes implementation
readiness:

- **Confirmed and fixed:** placing the shared level in `internal/provider` would
  make `internal/config` import provider and create a cycle.
- **Confirmed and fixed:** the plain chat path was omitted from propagation; both
  direct request construction sites are now explicit work items.
- **Confirmed and fixed:** the example configuration path is
  `.mivia/mivia.toml.example`.
- **Accepted residual risk:** provider capability/value matrices are intentionally
  not embedded; live provider compatibility remains a manual verification item.
- **Process limitation:** the prescribed parallel ADLC challenge tool was not
  available in this session; this validation was performed locally. The plan
  should receive the normal multi-agent Step 0 challenge before implementation.
