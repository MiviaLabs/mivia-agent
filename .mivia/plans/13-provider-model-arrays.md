# 13 — Per-provider model arrays (`models`)

**Status:** Implementation-ready (research complete, design validated).
**Date:** 2026-07-30.
**Depends on:** nothing (purely additive to `internal/config` + display surfaces).
**Blocks:** future `/model` autocomplete (plan `composer-autocomplete.md`).
**Blast radius:** SMALL — additive config field + display. No request path changes.

---

## 0. TL;DR / verdict

Add an optional `models = [...]` array to each `[providers.<name>]` block. The
existing `model` field stays as the **default** model for the provider. The array
is **declarative metadata**: surfaced in `config show` / `doctor` and available
for future `/model` completion. It does **not** gate `--model` or `/model`, and
it does **not** add runtime switching (that already exists).

```toml
[providers.deepseek]
model = "deepseek-v4-flash"                      # default
models = ["deepseek-v4-flash", "deepseek-v4-pro"] # available set
api_key_env = "DEEPSEEK_API_KEY"
base_url = "https://api.deepseek.com/v1"
```

---

## Document map

| §  | Content |
|----|---------|
| 1  | Why this is small (ground truth from research) |
| 2  | Locked design decisions (challenged + validated) |
| 3  | What does NOT change (non-goals) |
| 4  | Schema change |
| 5  | Resolution logic |
| 6  | Validation |
| 7  | Display surfaces (`config show`, `doctor`) |
| 8  | Example config + docs |
| 9  | Tests |
| 10 | Phases, gates, checklist |

---

## 1. Ground truth (from 3 parallel research agents)

These facts are verified against the codebase and are load-bearing for the
design.

### 1.1 The model is a single string, selected at startup

- `ProviderConfig.Model string` (`internal/config/types.go:100`) — TOML field.
- `Resolved.Model string` (`internal/config/types.go:167`) — runtime config.
- `resolveProvider` (`internal/config/load.go:120-149`) collapses everything to
  one `pc.Model` with this priority (lowest → highest):
  1. provider descriptor `DefaultModel` (only if TOML `model` is empty) — `load.go:139`
  2. TOML `[providers.<name>].model` — overrides descriptor default
  3. `--model` CLI flag (`LoadOptions.ModelOverride`) — overrides all — `load.go:146`
- `Validate()` (`load.go:204-243`) only checks `r.Model == ""`.

### 1.2 Runtime model switching ALREADY EXISTS

This is the key insight: a `models` array adds **zero runtime capability**.

- REPL `/model <name>`: `sess.Model = fields[1]` (`internal/cli/chat_slash_handlers.go:28`)
- TUI `/model <name>`: `m.session.Model = fields[1]` (`internal/cli/tui_slash_handlers.go:44`)
- Each turn re-reads `s.Model` fresh under the session lock and builds
  `provider.Request{Model: model, ...}` (`internal/chat/session.go:225, 278`).
- Both `/model` and `--model` are **unvalidated** — they accept any string
  (the intentional escape hatch for openrouter and ad-hoc testing).

### 1.3 The provider client never holds the model

- `provider.New` sets `Options.Model` (`internal/provider/provider.go:182`) but
  **every factory discards it** (`deepseek.go`, `openrouter.go`, `zai.go`).
- The `OpenAICompat` client has **no Model field**. The model is purely a
  per-`provider.Request` property serialized into the JSON body
  (`internal/provider/openai_compat.go:120, 372`).
- **Consequence:** switching models needs no client reconstruction. A `models`
  array has no place to plug into the request path — and shouldn't.

### 1.4 Consumers of `Resolved.Model` (the 6 sites)

| file:line | Use |
|---|---|
| `internal/chat/session.go:116` | `NewSession`: `Model: res.Model` |
| `internal/cli/chat_command.go:80` | `attachSessionDispatcher(sess, wsRoot, res.Model, ...)` |
| `internal/cli/chat_command.go:95` | `chat.NewSaveManager(store, res.Model, comp.Name())` |
| `internal/cli/config_cmd.go:37` | `fmt.Printf("model=%s\n", res.Model)` |
| `internal/cli/doctor.go:34` | `fmt.Printf("  model:      %s\n", res.Model)` |
| tests | `interactive_session_test.go:89, 216` |

> None of these need to change for the array. They all read the **default /
> active** model (`Model`), which remains a single string.

---

## 2. Locked design decisions

Each was challenged by an adversarial review and resolved.

### D1. Field name: `models` (plural)

`models` pairs cleanly with the singular `model`, matching mivia's established
singular/plural convention. No `_only` / `_extra` suffix is needed because there
is **no built-in model list** to extend or replace (the binary carries none —
`providerregistry.Descriptor` has only `DefaultModel`, singular).

Rejected alternatives:
- `available_models` — implies an exhaustive set (false for openrouter).
- `model_aliases` (map) — a *different*, more complex feature (short-name
  indirection). Reserve for a separate plan; do not conflate.
- `extra_models` — implies a baseline to extend; there is none.

### D2. `model` is authoritative; `models` is advisory metadata

The array is **never a validation gate**. Resolution:

- default model = `model` field, else descriptor `DefaultModel` (unchanged).
- `models` array = stored verbatim, surfaced for display + future completion.

**Why not `models[0]` as default?** Positional coupling is fragile (reordering
the array silently changes the default). `model` stays the explicit default knob.

### D3. Both `model` and `models` set → no contradiction error

If `model="A"` and `models=["B","C"]` (A not in array), this is **not an error**.
The array is advisory; `model` is the default. `config show` prints both.
Rationale: the array does not *constrain*, so there is nothing to contradict.
Rejecting this would break the escape-hatch philosophy for zero benefit.

### D4. `--model` and `/model` stay unrestricted overrides

`--model foo` and `/model foo` continue to accept **any string**, ignoring
`models` entirely. Membership validation would regress an existing capability
(openrouter's effectively-infinite catalog, ad-hoc testing). The array is for
*discovery*, not *enforcement*.

### D5. No runtime switching mechanism (already exists)

Do **not** wire `models` into any request-building path. Runtime `/model`
switching already works. If subagent-model-following is wanted later
(currently `attachSessionDispatcher` freezes `res.Model` for subagents — a
latent staleness gap), that is a **separate** plan targeting the dispatcher.

### D6. No validation against `providerregistry`

The registry carries only `DefaultModel`. openrouter's value is arbitrary
`org/model` slugs. Any validation would either hardcode a stale allowlist or
require a load-time network call (config load is deliberately offline). **Do not
validate.** Correctness for deepseek/zai is not worth breaking openrouter.

---

## 3. What does NOT change (non-goals)

- `Resolved.Model` stays a single string (the active/default model).
- The request path (`provider.Request.Model` → JSON body) is untouched.
- No client reconstruction on model switch.
- No `models[0]` positional default.
- No deprecation of `model`.
- No validation that `--model` / `/model` targets are in the array.
- No subagent model-following (separate plan).
- No migration tooling (there is no `config init`; old configs work as-is).

---

## 4. Schema change

### 4.1 `internal/config/types.go`

Add `Models []string` to `ProviderConfig` (TOML shape) and `Resolved` (runtime):

```go
// ProviderConfig holds non-secret provider settings.
type ProviderConfig struct {
	Model       string   `toml:"model"`
	Models      []string `toml:"models,omitempty"`   // NEW: declared available models (advisory)
	BaseURL     string   `toml:"base_url"`
	APIKeyEnv   string   `toml:"api_key_env"`
	HTTPReferer string   `toml:"http_referer"`
	XTitle      string   `toml:"x_title"`
}
```

```go
// In Resolved, after Model:
type Resolved struct {
	// ... existing fields ...
	Model    string   // default / active model (unchanged)
	Models   []string // NEW: advisory list of declared models for the active provider
	// ... rest ...
}
```

> `Resolved.Models` reflects **only the active provider's** array (consistent
> with how `Resolved.Model`/`BaseURL`/`APIKeyEnv` are all active-provider-only).

---

## 5. Resolution logic

### 5.1 `resolveProvider` (`internal/config/load.go`)

The array requires **no defaulting and no override logic** — it is stored
verbatim. Only carry it through so `Load` can copy it into `Resolved`.

`resolveProvider` already returns `ProviderConfig`, so `pc.Models` is already
populated from TOML decode. No change to `resolveProvider` is required for the
array itself.

### 5.2 `Load` assignment (`internal/config/load.go`, the `res := &Resolved{...}` block)

Add one line:

```go
res := &Resolved{
	// ...
	Model:    pc.Model,
	Models:   pc.Models,   // NEW
	// ...
}
```

That is the entire resolution change.

---

## 6. Validation

### 6.1 Reject empty entries in `models` (`Resolved.Validate()`, `load.go`)

Mirror the style of the existing `MaxToolResultBytes` floor check:

```go
for i, m := range r.Models {
	if strings.TrimSpace(m) == "" {
		return fmt.Errorf("[providers] models[%d] is empty", i)
	}
}
```

### 6.2 Optional: deduplicate (recommend YES, low cost)

Deduplicate the array at resolution time so display and completion are clean:

```go
// In resolveProvider or Load, after copying pc.Models:
pc.Models = dedupModels(pc.Models)
```

```go
func dedupModels(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, m := range in {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}
```

> Do **not** sort — preserve declared order (useful for display + completion
> ranking). Only collapse exact duplicates.

### 6.3 Do NOT validate

- membership of `model` in `models` (D2/D3 — advisory, no constraint).
- against `providerregistry` (D6 — breaks openrouter).

---

## 7. Display surfaces

### 7.1 `mivia config show` — YES, print the array

`internal/cli/config_cmd.go`, after the `model=` line:

```go
fmt.Printf("model=%s\n", res.Model)
if len(res.Models) > 0 {
	fmt.Printf("models=[%s]\n", strings.Join(res.Models, ","))
}
```

Print only when non-empty (avoids noise on old configs). This is in-scope for
"resolved non-secret settings."

### 7.2 `mivia doctor` — NO (keep focused)

`doctor` is a readiness check ("can I chat right now?"), answered by the single
active model. Do not expand it into a model catalog. Leave the existing
deepseek-specific note (`doctor.go:44-46`) as-is.

> **Follow-up (out of scope):** the hardcoded `if res.ProviderName == "deepseek"`
> branch in `doctor` could be generalized to read from `models`. File as a
> separate refactor; do not bundle it here.

---

## 8. Example config + docs

### 8.1 `.mivia/mivia.toml` and `.mivia/mivia.toml.example`

Add a commented `models` example to one provider block (deepseek is the natural
demonstration since it has the flash/pro split):

```toml
[providers.deepseek]
model = "deepseek-v4-flash"                        # default
models = ["deepseek-v4-flash", "deepseek-v4-pro"]  # available (advisory)
api_key_env = "DEEPSEEK_API_KEY"
base_url = "https://api.deepseek.com/v1"
```

### 8.2 `docs/product/config.md`

Add a short subsection under "Set up a provider":

> ### Model arrays
>
> Each provider accepts an optional `models` array listing models you switch
> between. `model` is the default; `models` is advisory metadata surfaced in
> `mivia config show` and available for `/model` completion. It does **not**
> restrict `--model` or `/model`, which accept any string.

### 8.3 `TestExampleConfigIncludesZAI` (`internal/config/load_test.go`)

If the example gains a `models` array, add an assertion mirroring the existing
`pc.Model` check.

---

## 9. Tests

Mirror existing patterns (`TestLoadTOMLAndEnv`, `TestModelOverride`). All new
tests go in `internal/config/load_test.go`.

| Test | Asserts |
|---|---|
| `TestLoadModelsArrayFromTOML` | TOML with `model` + `models` → `res.Model` == default, `res.Models` == array (order preserved). |
| `TestLoadModelsArrayNoDefault` | `models` set, `model` unset → `res.Model` == descriptor default (NOT `models[0]`), `res.Models` == array. |
| `TestLoadModelsArrayEmpty` | `models = []` → `res.Models` is nil/empty, no error (old-config compatibility). |
| `TestLoadModelsArrayDedup` | `models = ["A","B","A"]` → `res.Models == ["A","B"]` (order preserved, dups collapsed). |
| `TestLoadModelsArrayRejectsEmptyEntry` | `models = ["A",""]` → load error naming the index. |
| `TestModelOverrideStillWins` | `model` + `models` set, `--model X` → `res.Model == X`, `res.Models` unchanged (flag ignores array). |
| `TestOldConfigSingleModelUnchanged` | Config with only `model` (no `models`) → identical to today; `res.Models` is nil. |
| `TestExampleConfigModelsField` | (if example updated) `file.Providers["deepseek"].Models` has expected entries. |

---

## 10. Phases, gates, checklist

### Phase 1 — Schema + resolution (no behavior change visible)

- [ ] Add `Models []string` to `ProviderConfig` (`types.go`).
- [ ] Add `Models []string` to `Resolved` (`types.go`).
- [ ] Copy `pc.Models` → `res.Models` in `Load` (`load.go`).
- [ ] Add `dedupModels` helper.
- [ ] Add empty-entry validation to `Validate`.
- [ ] Tests: §9 rows 1–6.
- **Gate:** `go test ./internal/config/...` green; existing tests unchanged.

### Phase 2 — Display

- [ ] `config show` prints `models=[...]` when non-empty (`config_cmd.go`).
- [ ] Tests for display (or manual verify via `mivia config show`).
- **Gate:** `go test ./internal/cli/...` green; `mivia config show` shows array.

### Phase 3 — Example + docs

- [ ] Update `.mivia/mivia.toml` + `.mivia/mivia.toml.example`.
- [ ] Update `docs/product/config.md`.
- [ ] Add/adjust `TestExampleConfigIncludesZAI` if example changed.
- **Gate:** `go test ./...` green; `mivia doctor` unaffected.

### Checklist

- [ ] `Model` field and its priority order **unchanged** (descriptor → TOML → `--model`).
- [ ] `Resolved.Model` remains a single string.
- [ ] No change to `provider.Request`, session send path, or client factories.
- [ ] `--model` and `/model` still accept any string (no membership gate).
- [ ] Old configs (no `models`) load identically.
- [ ] `config show` additive only; `doctor` unchanged.
- [ ] No load-time network calls; no registry validation.

---

## Appendix A — Resolved sharp edges (acknowledged, not fixed here)

1. **Subagent model staleness:** `attachSessionDispatcher` freezes `res.Model`
   for subagent/skill handlers; `/model` switches do not propagate. This is
   pre-existing and orthogonal. Separate plan if subagent model-following is
   wanted.
2. **`doctor` deepseek hardcode:** `if res.ProviderName == "deepseek"` prints a
   hardcoded `DeepSeekProModel` note. Could generalize to `models`, but that is
   a refactor, not part of this feature.
3. **No `/model` completion yet:** the array enables future Tab-completion in the
   composer (see plan `composer-autocomplete.md`), but wiring that is a separate
   UI task.
