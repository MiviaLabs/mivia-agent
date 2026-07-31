# 28 — Per-model context windows

**Status:** Implementation-ready (challenged and revised 2026-07-31).
**Depends on:** archived plan 13 (per-provider allowlists).
**Blast radius:** HIGH — breaking TOML schema, config resolution, interactive
session state, both request paths, persisted-session restore, CLI/TUI UX, docs
and tracked examples.

## Goal

Replace each provider's string model allowlist with required model objects that
declare the model's total context capacity. The active model determines the
session's usable prompt budget, including after `/model` and session restore.

## Locked decisions

1. `models` is a TOML array of objects, not strings. The scalar-array shape is
   deliberately rejected as a breaking schema change; silently ignoring it
   would disable a managed provider's allowlist.

   ```toml
   [providers.deepseek]
   models = [
     { name = "deepseek-v4-flash", context_window_tokens = 128000 },
     { name = "deepseek-v4-pro", context_window_tokens = 1000000 },
   ]
   default_model = "deepseek-v4-pro"
   ```

   Omitted or explicitly empty `models` remains the established **unrestricted**
   policy; it has no per-model profile. A non-empty list is managed. The existing
   128-entry maximum remains in force.

2. Every declared object requires `name` and `context_window_tokens`. Names use
   the existing canonical normalization; duplicate normalized names are load
   errors (never silently deduplicated). Windows must be integers in
   `[1024, 10_000_000]`; missing, zero, negative, noninteger, overflow, and
   oversized values fail load with the declaration index and no unsafe value
   echo. Model objects accept only `name` and `context_window_tokens`; reject
   any other key using model-object-local strict decoding/raw-key validation.
   This is intentionally narrower than global TOML strictness.

3. `context_window_tokens` declares physical total capacity: prompt plus
   completion. Managed providers must set positive `[chat].max_tokens`; that is the output
   reserve. Every selectable model must satisfy
   `context_window_tokens > max_tokens`. This catches a later `/model` switch
   that would otherwise create an impossible request. The 10,000,000 ceiling is
   an implementation safety limit for token arithmetic and local request
   budgeting, not a claim about provider capacity.

   Structural TOML decoding rejects a scalar `models` value in any provider.
   Semantic profile and reserve validation is deliberately performed for the
   selected provider only, matching current resolution behavior; an inactive
   provider block is not required to be deployable. Tests must pin both cases.

4. `[chat].max_context_tokens` is replaced by optional
   `[chat].max_prompt_tokens`: an operator cap on prompt history, not provider
   capacity. The old key is rejected explicitly during TOML decode so a typo
   cannot silently change a safety budget. `max_prompt_tokens` is a pointer:
   absent means no operator cap; present values must be positive and within the
   same 10,000,000 upper bound. With no cap, usable prompt capacity is
   `context_window_tokens - max_tokens`; with a cap it is the smaller value.
   The decoder must retain a `*int toml:"max_context_tokens"` sentinel solely to
   reject that legacy key after normal decode. Do not enable global unknown-key
   rejection: unrelated unknown keys and the deliberately ignored legacy
   `providers.*.model` behavior stay compatible.

5. `/budget N` is a per-session requested prompt cap. `N=0` clears that manual
   cap. A positive value greater than the current model's usable prompt capacity
   is rejected (not silently clamped). The effective prompt budget is:

   ```text
   min(model.context_window_tokens - chat.max_tokens,
       chat.max_prompt_tokens if set,
       session /budget cap if set)
   ```

   For unrestricted providers, the profile is explicitly the local fallback:
   `chat.max_prompt_tokens` when set, otherwise `DefaultMaxContextTokens`
   (currently 1,000,000). `max_tokens` remains optional for unrestricted
   providers. `/budget` accepts a positive value only up to that fallback
   capacity, and follows the same `0` clear rule. It never inherits the last
   managed model's context window.

6. The selected model profile and effective prompt budget are immutable config
   policy copied into `chat.Session` and changed under its mutex as one state
   transition. `/model`, resume, and `/budget` return enough outcome data for
   REPL/TUI notices. A smaller-model switch takes effect before the next request;
   switching back recomputes rather than retaining a previous clamp. Define one
   mutex-owned immutable `ModelSelection` snapshot containing the model, profile
   (or unrestricted fallback), output reserve, requested cap, and effective
   cap. `SelectModel` and `SetBudget` return an outcome snapshot; no REPL/TUI
   caller may mutate a public budget field. Resolved and Session profile slices
   and maps are deep-copied before exposure.

7. Saved sessions continue to persist only the model name. Loading applies the
   current profile and recomputes the effective budget; a removed model follows
   plan 13's current-model fallback and warning. No historical window is trusted.

8. Context enforcement applies to plain and agent interactive sends. Refactor
   history preparation into one session-level path so plain chat cannot bypass
   a selected small model. It must prune an immutable message snapshot, then
   locally reject before a provider call if the estimated prompt still exceeds
   the effective cap (for example, an oversized system prompt or newest user
   turn that pruning preserves). A successful plain turn commits the prepared
   pruned snapshot plus reply under the existing turn-generation guard; failed
   or stale turns must not mutate history. This is an **approximate local
   guard**: the token estimator does not include provider serialization or tool
   schema overhead, so status/docs must not promise provider-side acceptance.
   The agent loop repeats the same prune-and-reject preflight after every tool
   step, before every subsequent provider request; session preparation alone is
   not sufficient for tool-expanded history.

   Subagents are in scope. Thread the frozen dispatcher profile's effective
   prompt cap and configured output reserve through `internal/cli/dispatcher.go`
   and `internal/cli/chat_repl.go` into both `internal/subagents/multi_step.go`
   and `internal/subagents/oneshot.go`. Multi-step sets `agent.Options`
   `MaxContextTokens` and `MaxTokens`; one-shot sets `provider.Request.MaxTokens`
   and runs equivalent preflight. Dispatchers intentionally freeze the startup
   resolved profile, including when a later session restore chooses a different
   root model; interactive `/model` never retargets already-created dispatchers.

9. `provider.Request` and provider clients do not receive a context-window
   field. They receive the selected model and configured `MaxTokens`; context
   capacity is local history/pruning policy.

## API and file plan

### Config wave

- `internal/config/types.go`: replace `ProviderConfig.Models []string` with
  `[]ModelConfig{Name, ContextWindowTokens}`; add resolved immutable model
  profiles and lookup APIs (`ModelSpec`, `AllowsModel`, `ModelChoices`). Replace
  `ChatConfig.MaxContextTokens` with `MaxPromptTokens` and add a decode-only
  legacy guard for `max_context_tokens`. Give `ModelConfig` a narrow strict
  decoder (or validate raw object keys) so only its two declared keys are legal.
- `internal/config/load.go`: normalize/validate object arrays, validate every
  selected-provider model against `chat.max_tokens`, preserve ordered
  first-default behavior, and resolve the active profile plus effective prompt
  cap. Retain the narrow legacy-key sentinel; do not broaden unknown-key rules.
- `internal/config/load_test.go`, new policy tests: object decode, scalar-array
  rejection, empty/unrestricted semantics, required fields and unknown/misspelled
  object-key rejection, all numeric bounds, duplicate canonical names, default/override
  membership, output-reserve edges, selected versus inactive provider validation,
  unrestricted fallback, legacy-key rejection for valid/zero/wrong-type values,
  and preservation of unrelated unknown keys / `providers.*.model` compatibility.

### Session/request wave

- `internal/chat/session.go`: replace string-only `allowedModels` with copied
  model specs; make model selection, requested budget, profile lookup, and
  effective budget one mutex-owned transition. Expose snapshots rather than
  public mutable budget fields.
- Extract a shared history-preparation/pruning-and-preflight path for `sendPlain`
  and `sendAgent`. Pass the same effective prompt budget to agent options and
  apply it before plain provider requests, preserving success/failure/stale-turn
  persistence semantics.
- `internal/agent/loop.go`: after `pruneHistory` and before **every** provider
  request (including post-tool steps), reject locally with a safe error when the
  estimated message tokens still exceed `Options.MaxContextTokens`. Do not send
  an over-budget irreducible system/current/tool turn; preserve valid tool-call
  pairing when returning the error.
- `internal/chat/persistence.go`, `save_manager.go`, and model-policy tests:
  restore model names then recompute against current profiles; save no derived
  window. Test model window tightening, removed models, and save/load races.
- `internal/cli/dispatcher.go`, `internal/cli/chat_repl.go`, and
  `internal/subagents/{multi_step,oneshot}.go`: thread the initial resolved
  profile's effective prompt budget and configured output reserve into nested
  requests/options; table-test their fixed-startup-profile semantics.

### CLI and docs wave

- `internal/cli/*slash*`, REPL, TUI and status: `/model` shows each name and
  usable prompt budget; `/budget` validates against the selected profile;
  switching/resume messages state a changed effective budget.
- `config show` and `doctor`: render each managed model as
  `name:context_window_tokens`, plus active usable prompt budget. Table-test
  managed and unrestricted output.
- Update `.mivia/mivia.toml`, `.mivia/mivia.toml.example`, and owned
  `docs/product/config.md` atomically. OpenRouter remains unrestricted; every
  managed example uses object syntax and declares `[chat].max_tokens`.

## TDD and verification

Wave 1 RED tests cover config parsing/validation before config implementation.
Wave 2 RED tests cover model/budget transitions, plain and agent requests,
resume, post-prune plain-history commits, oversized irreducible system/user/tool
turn rejection (including a direct post-tool agent-loop request), and both
subagent paths before session implementation. Wave 3 RED tests
cover REPL/TUI formatting and notices before UI/docs changes.

Required gates:

```text
go test ./internal/config/... -count=1
go test ./internal/chat/... ./internal/subagents/... -count=1
go test ./internal/cli/... -count=1
go test -race ./internal/chat/... ./internal/subagents/... ./internal/cli/...
go vet ./internal/config ./internal/chat ./internal/subagents ./internal/cli
make docs-check
make verify
```

The security test matrix includes malformed TOML, controls/invalid UTF-8,
duplicate normalized names, 128-entry boundary, integer bounds, legacy schema
rejection, unrestricted `/model` and `/budget`, malformed/negative/over-cap
budget parity in REPL and TUI, and a model/budget/select/send/save/load race.
The implementation
must add an invariant row only if the resulting atomic model-plus-budget state
cannot be pinned by an existing exact test; allocate the next free ID at landing.

## Rollback criterion

Reject this plan and return to challenge if one session state cannot atomically
represent selected model, requested budget, and effective budget, or if plain
chat/subagents cannot be made to honor the same physical capacity without
duplicating pruning policy.
