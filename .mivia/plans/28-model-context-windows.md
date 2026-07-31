# 28 — Per-model context windows

**Status:** Implementation-ready (challenged, revised, and amended 2026-07-31).
**Depends on:** archived plan 13 (per-provider allowlists).
**Blast radius:** HIGH — breaking TOML schema, config resolution, interactive
session state, both request paths, persisted-session restore, CLI/TUI UX, docs
and tracked examples.

## Goal

Replace each provider's string model allowlist with required model objects that
declare the model's total context capacity. The active provider-qualified model
determines the session's usable prompt budget, including after `/model`,
provider switching, and session restore. This plan is the configuration and
session foundation for `.mivia/plans/29-model-selection-dialog.md`.

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

   `models` is required and must be non-empty for every selectable provider.
   Omitted or explicitly empty `models` is invalid for the active provider and
   creates no selectable catalog for an inactive provider. There is no
   unrestricted-provider mode in the model-selection contract. The existing
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
   Every configured provider's declared model objects are normalized and
   profile-validated so a later provider switch cannot publish an invalid
   profile. Credential availability is resolved separately: an inactive
   provider with a missing key may be shown disabled, but never selectable.

4. `[chat].max_context_tokens` is replaced by optional
   `[chat].max_prompt_tokens`: an operator cap on prompt history, not provider
   capacity. The old key is rejected explicitly during TOML decode so a typo
   cannot silently change a safety budget. `max_prompt_tokens` is a pointer:
   absent means no operator cap; present values must be positive and within the
   same 10,000,000 upper bound. Usable prompt capacity is always
   `context_window_tokens - max_tokens`, further reduced by the operator cap
   when present.
   The decoder must retain a `*int toml:"max_context_tokens"` sentinel solely to
   reject that legacy key after normal decode. Do not enable global unknown-key
   rejection, but reject the legacy `providers.*.model` setting so it cannot
   silently manufacture or override a declared model.

5. `/budget N` is a per-session requested prompt cap. `N=0` clears that manual
   cap. A positive value greater than the current model's usable prompt capacity
   is rejected (not silently clamped). The effective prompt budget is:

   ```text
   min(model.context_window_tokens - chat.max_tokens,
       chat.max_prompt_tokens if set,
       session /budget cap if set)
   ```

   There is no unrestricted fallback profile. `/budget` accepts a positive value
   only up to the selected declared model's effective prompt capacity, and
   follows the same `0` clear rule. It never inherits another model's context
   window.

6. The selected provider/model profile and effective prompt budget are immutable
   config policy copied into `chat.Session` and changed under its mutex as one
   binding transition. `/model`, provider switching, resume, and `/budget`
   return enough outcome data for REPL/TUI notices. A smaller-model switch takes
   effect before the next request; switching back recomputes rather than
   retaining a previous clamp. Define one mutex-owned immutable selection
   snapshot containing provider, model, completer, dispatcher generation,
   profile, output reserve, requested cap, and effective cap. A transition is
   prepared outside the lock and published only when complete. Resolved and
   Session profile slices and maps are deep-copied before exposure.

7. Saved sessions persist provider and model together. Loading prepares the exact
   provider-qualified declared pair and its profile before replacing history; a
   removed provider/model, missing credential, or failed backend construction
   leaves both history and the current binding unchanged. There is no
   current-model fallback and no historical window is trusted.

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

   Subagents are in scope. Thread the selected binding's effective prompt cap
   and configured output reserve through `internal/cli/dispatcher.go`
   and `internal/cli/chat_repl.go` into both `internal/subagents/multi_step.go`
   and `internal/subagents/oneshot.go`. Multi-step sets `agent.Options`
   `MaxContextTokens` and `MaxTokens`; one-shot sets `provider.Request.MaxTokens`
   and runs equivalent preflight. A dispatcher generation is provider/model
   qualified. Provider/model switching is rejected while a turn or
   session-owned orchestration is active; once idle, CLI builds a complete new
   generation before atomically publishing it, so new work uses the new binding
   and old work is never retargeted.

9. `provider.Request` and provider clients do not receive a context-window
   field. They receive the selected model and configured `MaxTokens`; context
   capacity is local history/pruning policy.

## API and file plan

### Config wave

- `internal/config/types.go`: replace `ProviderConfig.Models []string` with
  `[]ModelConfig{Name, ContextWindowTokens}`; add resolved immutable model
  profiles, provider-qualified `ProviderModelGroup` catalog rows, private
  provider runtime records, and lookup APIs (`ModelSpec`, `AllowsModel`,
  `ModelChoices`, `ModelCatalog`). Replace `ChatConfig.MaxContextTokens` with
  `MaxPromptTokens` and add decode-only legacy guards for `max_context_tokens`
  and `providers.*.model`. Give `ModelConfig` a narrow strict decoder (or
  validate raw object keys) so only its two declared keys are legal.
- `internal/config/load.go`: normalize/validate every configured provider's
  object array, require explicit finite catalogs, validate every model profile
  against `chat.max_tokens`, preserve ordered declared defaults, resolve all
  provider credentials/runtime records, and expose a stable secret-free catalog.
  Retain narrow legacy-key sentinels; do not broaden unknown-key rules.
- `internal/config/load_test.go`, new policy tests: object decode, scalar-array
  rejection, omitted/empty/no-fallback semantics, required fields and unknown/misspelled
  object-key rejection, all numeric bounds, duplicate canonical names, default/override
  membership, output-reserve edges, selected versus inactive provider validation,
  no-fallback behavior, legacy-key rejection for valid/zero/wrong-type values,
  and rejection of the legacy `providers.*.model` setting while preserving
  unrelated unknown-key behavior.

### Session/request wave

- `internal/chat/session.go`: replace string-only `allowedModels` with copied
  provider-qualified model specs; make provider/model binding, requested budget,
  profile lookup, and effective budget one mutex-owned transition. Add complete
  turn-binding snapshots and an idle guard. Expose snapshots rather than public
  mutable budget fields.
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
  persist and restore exact provider/model pairs, prepare the binding before
  replacing history, recompute against current profiles, save provider/model
  from one generation, and test removed pairs plus save/load races.
- `internal/provider/provider.go`, `internal/cli/dispatcher.go`,
  `internal/cli/chat_repl.go`, and `internal/subagents/{multi_step,oneshot}.go`:
  build provider-qualified backend/dispatcher generations, thread the selected
  profile's effective prompt budget and configured output reserve into nested
  requests/options, and table-test idle switching plus old-turn isolation.

### CLI and docs wave

- `internal/cli/*slash*`, REPL, TUI and status: `/model` uses the strict
  provider-qualified catalog and transition service; `/budget` validates against
  the selected profile; switching/resume messages state the provider, model,
  and changed effective budget.
- `config show` and `doctor`: render each explicitly configured provider/model
  as `provider/name:context_window_tokens`, plus active usable prompt budget.
  Table-test finite catalogs and missing/disabled providers; there is no
  unrestricted output.
- Update `.mivia/mivia.toml`, `.mivia/mivia.toml.example`, and owned
  `docs/product/config.md` atomically. Every configured provider example uses
  object syntax, declares models, and declares `[chat].max_tokens` where
  required.

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
duplicate normalized names, provider-qualified duplicate IDs, 128-entry
boundary, integer bounds, legacy schema rejection, omitted/empty catalog and
registry-default no-fallback cases, provider credential isolation, malformed/
negative/over-cap budget parity in REPL and TUI, and a
model/budget/provider-select/send/save/load race.
The implementation
must add an invariant row only if the resulting atomic model-plus-budget state
cannot be pinned by an existing exact test; allocate the next free ID at landing.

## Rollback criterion

Reject this plan and return to challenge if one session state cannot atomically
represent selected model, requested budget, and effective budget, or if plain
chat/subagents cannot be made to honor the same physical capacity without
duplicating pruning policy.
