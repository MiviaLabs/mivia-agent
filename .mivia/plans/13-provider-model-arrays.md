# 13 — Per-provider model allowlists (`models`)

**Status:** Implementation-ready (rev 4, 2026-07-31 — enforcing allowlist; `model` removed, no legacy surface).
**Date:** 2026-07-30 (rev 2026-07-31).
**Depends on:** nothing.
**Blocks:** nothing today, but this is the prerequisite for any `/model` picker or
Tab-completion, because it is the first thing in the codebase that knows what
models exist.
**Blast radius:** MEDIUM — breaking config-schema change (`model` → `default_model`),
breaking behavior change to `--model` and `/model`, a new load-time failure mode, plus
`internal/chat` session-resume changes. This is **not** an additive-only change;
earlier revisions that claimed "SMALL / purely additive" described a different
(advisory) design that was rejected.

**Breaking is acceptable:** mivia is unpublished with a single user, and §1.9 confirms
the only two `mivia.toml` files in existence are both tracked in this repo and updated
by Phase 5. No migration path or deprecation window is owed to anyone.

---

## 0. TL;DR / verdict

`models = [...]` in a `[providers.<name>]` block is the **allowlist** of models
that provider may use. When declared, `--model`, `/model`, and session resume are
all restricted to that set. `default_model` names the default; without it, the
first entry wins. The old `model` field is **removed**.

```toml
[providers.deepseek]
models = ["deepseek-v4-flash", "deepseek-v4-pro"]  # allowlist; [0] is the default
default_model = "deepseek-v4-pro"                  # optional; must be a member
api_key_env = "DEEPSEEK_API_KEY"
base_url = "https://api.deepseek.com/v1"

[providers.openrouter]
default_model = "deepseek/deepseek-v4-flash"       # no `models` → unrestricted
api_key_env = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"
```

**Declaring `models` is opt-in.** A provider block with no `models` is
unrestricted — `default_model` still sets its default, and `--model` / `/model`
accept any string. This is not a courtesy: §1.5 shows it is forced by the fact
that mivia runs with no config file at all, and §1.9 shows the live openrouter
block already depends on it.

**`default_model` fully replaces `model`** because it works in *both* modes —
that is what makes removing `model` viable rather than merely aggressive (D2).

### 0.1 Why this design, and what it costs

The value is real and specific. Today `/model deepseek-v4-pr` (typo) succeeds
silently: `sess.Model` is set, and the mistake surfaces as a provider 400 on the
**next** turn, after the user has typed a prompt. Enforcement rejects it at the
keystroke and prints the valid set.

The cost is equally specific: **this removes a capability.** Today `--model
anything` and `/model anything` work against any provider. That escape hatch is
what makes openrouter's effectively-infinite `org/model` catalog usable ad hoc.
Trading it for typo-safety and a knowable model set is the right call for a tool
whose curated providers are deepseek and zai — but it *is* a trade, and §2 D5
gives it a documented bypass rather than pretending it away.

> **Superseded:** revisions 1–2 designed `models` as advisory metadata that
> validated nothing, and honestly admitted the payoff was one line of
> `config show` output. That design is dead. Decisions D2/D3/D4 from those
> revisions are inverted below.

---

## Document map

| §  | Content |
|----|---------|
| 1  | Ground truth (verified against `2dca36b`) |
| 2  | Locked design decisions |
| 3  | Non-goals |
| 4  | Schema |
| 5  | Resolution + enforcement algorithm |
| 6  | The four enforcement points |
| 7  | Display surfaces |
| 8  | Migration + docs |
| 9  | Tests |
| 10 | Phases, gates, checklist |

---

## 1. Ground truth

Verified against `2dca36b` on 2026-07-31. These facts are load-bearing.

### 1.1 The model is a single string, selected at startup

- `ProviderConfig.Model string` (`internal/config/types.go:100`).
- `Resolved.Model string` (`internal/config/types.go:158`).
- `resolveProvider` (`internal/config/load.go:120-150`) collapses everything to
  one `pc.Model`, lowest → highest precedence:
  1. descriptor `DefaultModel`, only if TOML `model` is empty — `load.go:137-139`
  2. TOML `[providers.<name>].model`
  3. `--model` (`LoadOptions.ModelOverride`) — `load.go:146-148`
- `Validate()` (`load.go:266-289`) only checks `r.Model == ""` (`load.go:270`).

### 1.2 Runtime switching exists and is unvalidated

- REPL: `sess.Model = fields[1]` (`internal/cli/chat_slash_handlers.go:28`)
- TUI: `m.session.Model = fields[1]` (`internal/cli/tui_slash_handlers.go:44`)
- Each turn re-reads `s.Model` under the session lock into
  `provider.Request{Model: ...}` (`internal/chat/session.go:225, 278`).
- Neither surface validates. This is what §0.1 changes.

### 1.3 Both `/model` surfaces already hold the config — no `Session` change needed for the gate

This is why enforcement is cheap where it matters most:

- REPL: `handleSlashInfo(cmd, fields, sess, res *config.Resolved, ...)` already
  takes `res`.
- TUI: `tuiModel.config *config.Resolved` (`internal/cli/tui.go:53`, assigned at
  `:189`).

The `/model` gate is a local edit in each handler. (Session resume is the
exception — see §1.6.)

### 1.4 The provider client never holds the model

`provider.New` sets `Options.Model` (`internal/provider/provider.go:182`) but
every factory discards it (`deepseek.go:12`, `openrouter.go:20`, `zai.go:18` all
build `CompatOptions` with no Model). `OpenAICompat` has no Model field; the
model is a per-`Request` JSON property (`openai_compat.go:120, 372`). Switching
models needs no client reconstruction — enforcement is purely a config/UI
concern and touches no request path.

### 1.5 mivia runs with **no config file at all** — this forces the opt-in rule

`Load` supports `AllowMissingConfig: true` (used by `config show` and `doctor`,
`config_cmd.go:26-29`, `doctor.go:16-19`). With no file, `file.Providers` is nil,
`pc` is the zero `ProviderConfig`, and `pc.Model` falls back to
`descriptor.DefaultModel` (`load.go:136-139`).

**Consequence:** there is no universe in which `models` is the sole source of the
active model. The descriptor default must survive as the floor, so enforcement
can only apply *when `models` is declared*.

This is a statement about the **runtime**, not a compatibility argument: the
schema change is deliberately breaking (D2), and both config files get rewritten
in Phase 5. Unrestricted mode exists because `mivia doctor` in an empty directory
must still resolve a model — not to spare anyone a migration.

### 1.6 Session resume overwrites the model, bypassing any gate

`Session.Load` (`internal/chat/persistence.go:256`) assigns `s.Model = model` at
`:284` (store path) and `s.Model = meta.Model` at `:313` (direct-I/O fallback).
`persistence_cleanup.go:61` writes `Model: "unknown"` for crash-recovered
sessions — a value that is in no allowlist, ever.

Call sites — all four in `internal/cli`, all already reporting to their own
surface, so no `os.Stderr` write is needed (which would corrupt the TUI):

| site | surface |
|---|---|
| `chat_slash_handlers.go:113` | REPL `/load` → `term.WriteString` |
| `chat_repl_loop.go:66` | REPL auto-resume |
| `tui_slash_handlers.go:104` | TUI `/load` → `m.appendInfo` |
| `welcome.go:280` | TUI welcome picker → `m.appendInfo` |

### 1.7 Three existing hardcodes that `models` generalizes

Enforcement turns these from "someday" refactors into natural fallout:

1. `chat_slash_handlers.go:25` — bare `/model` prints the literal usage string
   `/model deepseek-v4-flash|deepseek-v4-pro|<name>`, with deepseek model names
   baked in regardless of the active provider.
2. `doctor.go:44-48` — `if res.ProviderName == "deepseek"` prints a hardcoded
   `config.DeepSeekProModel` hint.
3. `docs/product/config.md:28-31` — a hand-maintained table of per-provider
   models.

### 1.8 The change is decode-safe

- No test does struct equality on `Resolved` or `ProviderConfig`.
- Nothing calls `toml.Marshal`; `File` is decode-only (`load.go:152-172`).
- `internal/cli` has **no** tests for `config_cmd.go` or `doctor.go`, and no
  stdout-capture helper. This drives §7.4 and the Phase 4 gate.
- go-toml/v2 silently ignores unknown TOML keys, so a leftover `model = "x"` is a
  no-op. §1.9 bounds that to zero configs: both files in existence are rewritten
  by Phase 5, so there is nothing left to carry a stale key.

### 1.9 Every `mivia.toml` in existence is in this repo

Verified 2026-07-31. `DefaultConfigCandidates()` (`internal/config/paths.go:31-43`)
searches `$MIVIA_CONFIG`, then `<cwd>/.mivia/mivia.toml`, then
`~/.config/mivia/config.toml`. On this machine `~/.config/mivia/` **does not
exist**, and `git ls-files` shows exactly two config files, both tracked:
`.mivia/mivia.toml` and `.mivia/mivia.toml.example`. Phase 5 updates both.

Two facts from the live `.mivia/mivia.toml` that shaped the design:

1. **openrouter sets a custom default with no allowlist:**
   `model = "deepseek/deepseek-v4-flash"` (`:26`) — not the descriptor default.
   This is the direct evidence that the default-setter must work *without*
   `models`, and therefore that `default_model` (not `models[0]` alone) has to
   exist. Without it, expressing "unrestricted, but default to X" is impossible.
2. **A stale comment to fix in passing:** `:20` reads
   `model = "deepseek-v4-pro"        # fast. For hard reasoning: deepseek-v4-pro`
   — the value is `pro`, the comment calls it "fast", and then recommends `pro`.
   Phase 5 rewrites this block anyway.

---

## 2. Locked design decisions

### D1. `models` is an allowlist, enforced — but only when declared

- `len(models) == 0` → **unrestricted mode**: `--model` and `/model` accept any
  string. `default_model` still sets the default; absent that, the descriptor's.
- `len(models) > 0` → **managed mode**: `models` is the complete selectable set.

Forced by §1.5 (mivia runs with no config file) and by §1.9's live openrouter
block, which needs "unrestricted, but default to X".

### D2. `model` is removed outright; `default_model` replaces it in both modes

`default_model` is not a managed-mode-only field — it is simply the new name for
"this provider's default model", valid with or without `models`. That is what
makes deleting `model` viable: there is no expressible configuration that loses
meaning in the rename.

| | no `models` | `models` declared |
|---|---|---|
| no `default_model` | descriptor default, unrestricted | `models[0]`, restricted |
| `default_model = "X"` | `X`, unrestricted | `X`, restricted; **must** be in `models` |

`model` is deleted from `ProviderConfig` outright. **No compatibility shim, no
deprecation alias, no rename guard** — the key does not exist, and nothing in the
codebase acknowledges that it ever did. §1.9 is what makes that clean: both
config files in existence are rewritten by Phase 5, and there is no
`~/.config/mivia/config.toml` on the machine, so there is no config anywhere that
could still carry the old key.

A stray `model = "x"` is simply an unrecognized TOML key and is ignored, exactly
like any other typo'd key in this config format.

### D3. Default resolution: `default_model` → `models[0]` → descriptor

One ordered chain, both modes:

1. `default_model` if set (trimmed). In managed mode it **must** be a member of
   `models`, else load error.
2. else `models[0]` if `models` is declared.
3. else `descriptor.DefaultModel` (unrestricted mode only — see D4).

Revision 1's D2 rejected positional defaults as fragile. Still true, and that is
exactly why step 1 exists: array order is a convenience for zero-ceremony
configs, never the only way to pin a default.

### D4. The descriptor default never overrides an allowlist

Revision 1's D3 said a `model` outside `models` was "not an error." Under
enforcement that would let startup produce an active model violating its own
gate. In managed mode the descriptor's `DefaultModel` is **not consulted at
all** — `models[0]`/`default_model` is the floor. Unrestricted mode keeps the
descriptor fallback unchanged.

### D5. One escape hatch: `MIVIA_ALLOW_UNLISTED_MODEL=1`

Mirrors the existing precedent in `validateBaseURL` (`load.go:291-303`), which
gates `http://` behind `MIVIA_ALLOW_INSECURE_HTTP=1` — same shape, same `== "1"`
comparison, no new config field.

When set, `models` degrades to advisory: `--model`, `/model`, and resume all
accept anything, and the list still drives display and (future) completion.
Resolved **once at load** into `Resolved.AllowUnlistedModel`, not re-read per
call, so the policy cannot change mid-session (matching how
`MIVIA_REDACT_TOOL_ARGS` resolves at load, `load.go:193-198`).

Rejected: a per-provider `strict_models = false`. Two mechanisms for one policy,
and the openrouter case is already covered by simply not declaring `models`.

### D6. Still no validation against `providerregistry`

`Descriptor` carries only `DefaultModel`. Any built-in catalog would be a stale
allowlist or a load-time network call, and config load is deliberately offline.
The operator's `models` array is the only source of truth. Unchanged from rev 1.

### D7. Entries are trimmed and de-duplicated, order preserved

`strings.TrimSpace` each entry, reject empty-after-trim, collapse exact
duplicates, never sort. Order is meaningful: `models[0]` is the default (D3) and
the order is the display/completion order. Mirrors the existing `BaseURL`
normalization (`strings.TrimRight`, `load.go:76`).

### D8. Resume falls back and warns; it does not refuse

A saved session whose model is not selectable (config tightened since, or
`Model: "unknown"` from crash recovery) loads normally, keeps the **current**
model, and reports the substitution. Refusing to open a saved conversation
because of a config change is hostile, and grandfathering it would leave the gate
bypassable by resuming.

---

## 3. Non-goals

- No change to `provider.Request`, the send path, or client factories (§1.4).
- No client reconstruction on model switch.
- No `providerregistry` model catalog (D6).
- No per-provider strict flag (D5).
- No `/model` Tab-completion or picker UI — this plan makes it *possible*, and
  that remains separate work.
- No subagent model-following. `attachSessionDispatcher` still freezes
  `res.Model`; orthogonal, pre-existing (Appendix A).
- No `config init` / migration tooling. §8 documents the manual edit.
- `Resolved.Model` stays a single string.

---

## 4. Schema

### 4.1 `internal/config/types.go`

```go
// ProviderConfig holds non-secret provider settings.
type ProviderConfig struct {
	// Models is the allowlist of models this provider may use. Empty means
	// unrestricted. Non-empty restricts --model, /model and session resume.
	// Entry [0] is the default unless DefaultModel names another member.
	Models []string `toml:"models,omitempty"`
	// DefaultModel is this provider's default model, valid with or without
	// Models. When Models is declared it must be a member of it.
	DefaultModel string `toml:"default_model,omitempty"`
	// ResolvedModel is derived, never decoded: resolveProvider writes the
	// resolved default (or the --model override) here for Load to copy into
	// Resolved.Model.
	ResolvedModel string `toml:"-"`
	BaseURL       string `toml:"base_url"`
	APIKeyEnv     string `toml:"api_key_env"`
	HTTPReferer   string `toml:"http_referer"`
	XTitle        string `toml:"x_title"`
}
```

> There is no `Model` field. The resolved value is carried in `ResolvedModel`,
> named so it cannot be mistaken for a decoded TOML key, and `Load` becomes
> `Model: pc.ResolvedModel`. Nothing in the struct maps to `model`.

```go
// In Resolved, replacing the bare Model field:
type Resolved struct {
	// ... existing fields ...

	// Model is the active model: the resolved default, or the --model override.
	Model string
	// Models is the active provider's allowlist, trimmed and de-duplicated in
	// declaration order. Nil means unrestricted.
	Models []string
	// AllowUnlistedModel is resolved from MIVIA_ALLOW_UNLISTED_MODEL at load,
	// mirroring MIVIA_ALLOW_INSECURE_HTTP. When true, Models is advisory only.
	AllowUnlistedModel bool

	// ... rest ...
}
```

> `Resolved.Models` is the **active provider's** array only, consistent with
> `Model`/`BaseURL`/`APIKeyEnv`. See §5.4 for the validation-scope consequence.

### 4.2 The shared policy predicate

One place, used by all four enforcement points (§6):

```go
// AllowsModel reports whether name may be selected under the resolved policy.
// Unrestricted providers and the MIVIA_ALLOW_UNLISTED_MODEL escape hatch admit
// everything; otherwise membership in Models is required.
func (r *Resolved) AllowsModel(name string) bool {
	if len(r.Models) == 0 || r.AllowUnlistedModel {
		return true
	}
	return slices.Contains(r.Models, strings.TrimSpace(name))
}

// ModelChoices renders the selectable set for error messages and usage text.
// Empty string when unrestricted.
func (r *Resolved) ModelChoices() string {
	return strings.Join(r.Models, ", ")
}
```

`slices` is stdlib on `go 1.25.0`; this is its first use in `internal/`, which is
fine.

---

## 5. Resolution + enforcement algorithm

### 5.1 `normalizeModels` (`internal/config/load.go`)

Trim → reject empty **at the declared index** → dedup. One pass, fresh
allocation.

```go
func normalizeModels(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for i, m := range in {
		t := strings.TrimSpace(m)
		if t == "" {
			// Report the index the operator wrote, not a post-dedup index.
			return nil, fmt.Errorf("models[%d] is empty", i)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}
```

Two traps this avoids, both live defects in earlier revisions:

1. **No in-place aliasing.** An earlier draft used `out := in[:0]`. `pc` is a
   *copy* of `file.Providers[name]`, but the slice header shares its backing
   array with the map entry — deduping `["A","B","A"]` in place leaves
   `file.Providers[name].Models` as `["A","B","B"]`. Use `make`.
2. **No index skew.** Deduping before validating makes `["A","A",""]` report
   `models[1]` when the operator's empty entry is on the third line. Validate
   inside the same pass, against the original index.

### 5.2 `resolveProvider` — the mode split

Replaces the current `if pc.Model == "" { pc.Model = descriptor.DefaultModel }`
… `if opts.ModelOverride != "" { pc.Model = opts.ModelOverride }` block:

```go
pc := file.Providers[name]

models, err := normalizeModels(pc.Models)
if err != nil {
	return "", ProviderConfig{}, fmt.Errorf("[providers.%s]: %w", name, err)
}
pc.Models = models
pc.DefaultModel = strings.TrimSpace(pc.DefaultModel)

// ---- D3: default_model → models[0] → descriptor. ----
switch {
case pc.DefaultModel != "":
	if len(models) > 0 && !slices.Contains(models, pc.DefaultModel) {
		return "", ProviderConfig{}, fmt.Errorf(
			"[providers.%s]: default_model %q is not in models (%s)",
			name, pc.DefaultModel, strings.Join(models, ", "))
	}
	pc.ResolvedModel = pc.DefaultModel
case len(models) > 0:
	pc.ResolvedModel = models[0]
default:
	pc.ResolvedModel = descriptor.DefaultModel // D4: unrestricted mode only
}

// ---- D1/D5: --model is gated only in managed mode. ----
if opts.ModelOverride != "" {
	if len(models) > 0 &&
		os.Getenv("MIVIA_ALLOW_UNLISTED_MODEL") != "1" &&
		!slices.Contains(models, opts.ModelOverride) {
		return "", ProviderConfig{}, fmt.Errorf(
			"[providers.%s]: --model %q is not in models (%s); set MIVIA_ALLOW_UNLISTED_MODEL=1 to bypass",
			name, opts.ModelOverride, strings.Join(models, ", "))
	}
	pc.ResolvedModel = opts.ModelOverride
}
```

Note `pc.ResolvedModel` is a **derived** field on the returned `ProviderConfig`, not
a decoded one — it exists only to carry the resolved default into `Resolved`. It
has no TOML tag (D2) and is populated exclusively by the switch above.

### 5.3 `Load` assignment

```go
res := &Resolved{
	// ...
	Model:              pc.ResolvedModel,
	Models:             pc.Models,                                        // NEW
	AllowUnlistedModel: os.Getenv("MIVIA_ALLOW_UNLISTED_MODEL") == "1",   // NEW
	// ...
}
```

`Resolved.Validate()` needs **no** models clause — every failure is caught in
`resolveProvider`, where the provider name and declared index are in scope.

### 5.4 Scope caveat: active provider only

`resolveProvider` touches only `file.Providers[name]`. A malformed `models` under
`[providers.openrouter]` while `provider.name = "deepseek"` loads clean and only
errors once that provider is selected.

Accepted, not fixed: this is exactly how `model`, `base_url` and `api_key_env`
already behave. Documented so §5.2 is not misread as a whole-file guarantee.

---

## 6. The four enforcement points

| # | Point | Location | Behavior when rejected |
|---|---|---|---|
| 1 | `--model` | `resolveProvider` (§5.2) | Load error; process exits before any turn. |
| 2 | REPL `/model` | `chat_slash_handlers.go:23-29` | Print rejection + valid set; leave `sess.Model` untouched. |
| 3 | TUI `/model` | `tui_slash_handlers.go:41-48` | `m.appendInfo` rejection + valid set; leave `m.session.Model` untouched. |
| 4 | Session resume | `persistence.go:284, 313` | Keep current model, record substitution (D8). |

Points 2 and 3 need **no `chat.Session` change** — both handlers already hold
`*config.Resolved` (§1.3).

### 6.1 REPL `/model` (point 2)

Also fixes hardcode §1.7.1 — the usage string is generated, not literal:

```go
case "/model":
	if len(fields) < 2 {
		if choices := res.ModelChoices(); choices != "" {
			term.WriteString(fmt.Sprintf("\ncurrent model=%s\navailable: %s", sess.Model, choices))
		} else {
			term.WriteString(fmt.Sprintf("\ncurrent model=%s\nusage: /model <name>", sess.Model))
		}
		return true, false, nil
	}
	if !res.AllowsModel(fields[1]) {
		term.WriteString(fmt.Sprintf("\nmodel %q is not available for provider %s\navailable: %s",
			fields[1], res.ProviderName, res.ModelChoices()))
		return true, false, nil
	}
	sess.Model = fields[1]
```

Rejection is **not** an error return — it is a message, like the existing
invalid-step-limit path (`handleSlashLimits`).

### 6.2 TUI `/model` (point 3)

Same shape against `m.config`, via `m.appendInfo`. Bare `/model` lists the set.
`m.modelName = shortenModel(...)` only updates on an accepted switch.

### 6.3 Session resume (point 4)

This is the one place `internal/chat` changes. `Session` gains the policy —
mirroring how `NewSession` already copies `MaxSteps`, `MaxToolResultChars` and
`MaxContextTokens` off `res`:

```go
// In Session:
// AllowedModels restricts /model and session resume. Nil means unrestricted.
AllowedModels []string
// AllowUnlistedModel bypasses AllowedModels (MIVIA_ALLOW_UNLISTED_MODEL).
AllowUnlistedModel bool
// ModelSubstituted is set by Load when a saved session's model was not
// selectable and the active model was kept instead. Callers surface it on
// their own output channel. Cleared at the start of each Load.
ModelSubstituted string
```

`NewSession` sets the first two from `res.Models` / `res.AllowUnlistedModel`.
Both assignment sites in `persistence.go` become:

```go
if s.allowsModel(meta.Model) {
	s.Model = meta.Model
} else {
	s.ModelSubstituted = meta.Model // keep s.Model as-is
}
```

**Why keep the current model rather than "the config default":** at startup
`s.Model == res.Model` (the resolved default) anyway, and mid-session it is
whatever the user deliberately switched to. Both are better answers than
re-deriving a default, and neither needs a new `DefaultModel` field on `Session`.

Each of the four call sites (§1.6) then appends one line on its own surface:

```go
if sess.ModelSubstituted != "" {
	term.WriteString(fmt.Sprintf("\n(session was saved with model %q, which is not available; using %s)",
		sess.ModelSubstituted, sess.Model))
}
```

This covers `Model: "unknown"` from `persistence_cleanup.go:61` for free.

---

## 7. Display surfaces

### 7.1 `mivia config show`

Flat `key=value`, comma-separated — every other line in this output is bare
`key=value` with no delimiter syntax, and it is a de-facto machine-readable
surface, so no brackets.

```
provider=deepseek
model=deepseek-v4-flash
models=deepseek-v4-flash,deepseek-v4-pro
model_policy=restricted        # or: unrestricted / unrestricted (MIVIA_ALLOW_UNLISTED_MODEL)
```

- `models=` prints only when non-empty.
- `model_policy=` prints always — under enforcement, "can I select something
  else?" is a first-class question, and the env-var bypass must be visible or it
  becomes an invisible mode.

### 7.2 `mivia doctor`

Reversed from earlier revisions, which kept `doctor` out of scope. Under
enforcement "which models can I select?" is a readiness fact:

```
  provider:   deepseek
  model:      deepseek-v4-flash
  models:     deepseek-v4-flash, deepseek-v4-pro
```

And the hardcoded deepseek branch (`doctor.go:44-48`, §1.7.2) is **deleted** —
`models` is what it was approximating. Providers with no `models` print no
`models:` line and keep today's output otherwise.

> `config.DeepSeekProModel` (`defaults.go:60`) stays: it is still referenced by
> `load_test.go:58, 108, 112`. Only `doctor`'s use of it goes.

### 7.3 REPL `/model` usage string

Covered in §6.1 — generated from `models`, removing hardcode §1.7.1.

### 7.4 Testability: extract the formatting

`runConfigShow` and `runDoctor` write via `fmt.Printf` to stdout, and
`internal/cli` has neither tests for them nor any stdout-capture helper (§1.8).
"Manually verify" is not a gate, so:

- Extract `func formatConfigShow(res *config.Resolved) string` — pure, no I/O.
- `runConfigShow` becomes `fmt.Print(formatConfigShow(res))`.
- Table-test it directly. No `os.Pipe`, no stdout plumbing.

`doctor` keeps its current structure (it interleaves an error return with
output); its `models:` line is covered by the manual Phase 4 sanity pass plus the
existing `make verify`.

---

## 8. Migration + docs

### 8.1 Migration: one rename, loudly enforced

`model = "x"` → `default_model = "x"`. That is the whole migration, and it
applies to exactly two files (§1.9), both rewritten by Phase 5. Any config that
misses the rename fails at load naming the new key (D2); nothing silently does
the wrong thing.

Declaring `models` on top of that is optional and is what turns enforcement on.

### 8.2 `.mivia/mivia.toml.example` and `.mivia/mivia.toml`

Both files. The live edit is not just dogfooding — after this change the current
`.mivia/mivia.toml` **will not load** (three `model =` keys, §1.9), so Phase 5 is
a hard dependency of Phase 1, not a documentation nicety.

```toml
[providers.deepseek]
models = ["deepseek-v4-flash", "deepseek-v4-pro"]  # allowlist; [0] is the default
default_model = "deepseek-v4-pro"                  # optional; must be a member
api_key_env = "DEEPSEEK_API_KEY"
base_url = "https://api.deepseek.com/v1"

[providers.zai]
models = ["glm-5.2"]
api_key_env = "ZAI_API_KEY"
base_url = "https://api.z.ai/api/paas/v4"

[providers.openrouter]
# No `models`: openrouter serves an effectively unbounded catalog, so it stays
# unrestricted — --model and /model accept any org/model slug. default_model
# still pins the startup default.
default_model = "deepseek/deepseek-v4-flash"
api_key_env = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"
http_referer = "https://github.com/MiviaLabs/mivia-agent"
x_title = "mivia"
```

Leaving openrouter unrestricted is deliberate: it preserves the live config's
existing behavior (§1.9), demonstrates both modes, and keeps the escape hatch
discoverable.

Also in Phase 5, from §1.9:

- Fix the stale `.mivia/mivia.toml:20` comment (`# fast. For hard reasoning:
  deepseek-v4-pro` on a `deepseek-v4-pro` value). The rewritten block above makes
  the flash/pro split self-evident, so the comment simply goes.
- Keep zai's coding-endpoint warning comment (`.mivia/mivia.toml:33-36`) intact —
  it is unrelated to this change and load-bearing for key/endpoint pairing.

> `.mivia/mivia.toml.example` may have uncommitted local edits — check
> `git status` and rebase onto them.

### 8.3 `docs/product/config.md`

- Rewrite the per-provider model table (`:28-31`, hardcode §1.7.3) to point at
  `models` instead of enumerating models in prose.
- New subsection under "Set up a provider":

> ### Model allowlists
>
> A provider's `models` array is the set of models it may use. When declared,
> `--model`, `/model` and resuming a saved session are all restricted to that
> set — a typo is rejected immediately instead of failing on the next request.
>
> `default_model` names the provider's default. Without it the first `models`
> entry wins; with `models` declared it must be a member of the array.
>
> Omit `models` to leave a provider unrestricted — `default_model` still sets the
> default, and any model name is accepted. Use this for OpenRouter, whose catalog
> is effectively unbounded.
>
> | | no `models` | `models` declared |
> |---|---|---|
> | no `default_model` | provider's built-in default, unrestricted | `models[0]`, restricted |
> | `default_model = "X"` | `X`, unrestricted | `X` (must be in `models`), restricted |
>
> To try an unlisted model without editing config, set
> `MIVIA_ALLOW_UNLISTED_MODEL=1` — the same escape-hatch pattern as
> `MIVIA_ALLOW_INSECURE_HTTP=1`.

---

## 9. Tests

### 9.1 `internal/config/load_test.go` — resolution

| Test | Asserts |
|---|---|
| `TestUnrestrictedNoConfigFile` | `AllowMissingConfig`, no file → descriptor default, `res.Models` nil, `res.AllowUnlistedModel` false (§1.5). |
| `TestUnrestrictedDefaultModelOnly` | `default_model = "X"`, no `models` → `res.Model == "X"`, `res.Models` nil (the live openrouter shape, §1.9). |
| `TestUnrestrictedAcceptsAnyOverride` | No `models`, `--model whatever` → `res.Model == "whatever"`, no error. |
| `TestManagedDefaultsToFirstEntry` | `models = ["A","B"]`, no `default_model` → `res.Model == "A"`. |
| `TestManagedDefaultModelWins` | `models = ["A","B"]`, `default_model = "B"` → `res.Model == "B"`. |
| `TestManagedIgnoresDescriptorDefault` | `models = ["A","B"]` where neither is the descriptor default → `res.Model == "A"` (D4). |
| `TestManagedRejectsUnlistedDefaultModel` | `default_model = "Z"` not in `models` → error listing the set. |
| `TestManagedRejectsUnlistedOverride` | `models = ["A","B"]`, `--model Z` → error naming the set and the env var. |
| `TestManagedAcceptsListedOverride` | `models = ["A","B"]`, `--model B` → `res.Model == "B"`. |
| `TestAllowUnlistedEnvBypassesOverride` | `MIVIA_ALLOW_UNLISTED_MODEL=1`, `--model Z` → accepted; `res.AllowUnlistedModel` true. |
| `TestModelsTrimAndDedup` | `["A", " A ", " B"]` → `["A","B"]`, order preserved. |
| `TestModelsRejectsEmptyEntry` | `["A","A",""]` → error naming **index 2** (declared index) and the provider block. |
| `TestModelsDoesNotAliasFileConfig` | Re-unmarshalling the same TOML after `Load` yields the original array (§5.1 trap 1). |
| `TestExampleConfigModelsField` | `.mivia/mivia.toml.example`: deepseek has both entries, openrouter has no `models` but a `default_model`. |

> **Sweep required:** `TestModelOverride` (`load_test.go:100-112`) and
> `TestLoadTOMLAndEnv` write `model = ...` TOML fixtures and will fail against
> the new schema. Unlike every prior revision, existing config tests **must** be
> edited — Phase 1's gate changes accordingly (§10). `config.DeepSeekProModel`
> (`defaults.go:60`) stays; it is still used by those fixtures.
>
> `TestExampleConfigIncludesZAI` swaps its `pc.Model != "glm-5.2"` check for a
> `Models` assertion, keeping the `APIKeyEnv` / `BaseURL` contract checks.
>
> No test asserts anything about a `model` key. It is not a deprecated key with a
> defined behavior — it is simply not part of the schema, and the plan carries no
> test, field, error, or doc line acknowledging it ever was.

### 9.2 `internal/config/policy_test.go` (new) — the predicate

`AllowsModel` across: nil `Models` (true for anything), populated + member,
populated + non-member, populated + non-member with `AllowUnlistedModel`,
whitespace-padded input.

### 9.3 `internal/cli/config_cmd_test.go` (new) — display

Table tests over `formatConfigShow` (§7.4): unrestricted (no `models=` line,
`model_policy=unrestricted`), managed, managed + env bypass, single entry (no
trailing comma).

### 9.4 `internal/cli` — `/model` enforcement

Extend the REPL slash-handler tests: accepted switch mutates `sess.Model`;
rejected switch leaves it **untouched** and returns no error; bare `/model`
lists the set when managed and prints generic usage when unrestricted.

### 9.5 `internal/chat` — resume

| Test | Asserts |
|---|---|
| `TestLoadKeepsModelWhenSavedModelUnlisted` | Saved model not in `AllowedModels` → `s.Model` unchanged, `ModelSubstituted` == saved value. |
| `TestLoadRestoresListedModel` | Saved model in the list → restored, `ModelSubstituted` empty. |
| `TestLoadUnrestrictedRestoresAnything` | Nil `AllowedModels` → restored verbatim (covers today's behavior). |
| `TestLoadRecoveredUnknownModelSubstituted` | `Model: "unknown"` (`persistence_cleanup.go:61`) → substituted, not installed. |
| — | Cover **both** assignment sites (`:284` store path and `:313` direct-I/O fallback). |

---

## 10. Phases, gates, checklist

### Phase 1 — Schema + resolution + `--model` enforcement

**Do Phase 5's config-file rewrite first, or in the same commit.** After this
phase the tracked `.mivia/mivia.toml` fails to load (§8.2) — the repo is broken
between the two.

- [ ] `ProviderConfig`: delete `Model`; add `Models`, `DefaultModel`, and derived `ResolvedModel \`toml:"-"\`` (§4.1).
- [ ] `Resolved`: add `Models`, `AllowUnlistedModel`.
- [ ] Add `normalizeModels` (§5.1) — `make`-allocated, declared-index errors.
- [ ] Rewrite the model block in `resolveProvider` (§5.2), rename guard first.
- [ ] Add `AllowsModel` / `ModelChoices` (§4.2).
- [ ] Sweep `load_test.go` fixtures off `model =` (§9.1 note).
- [ ] Tests: §9.1, §9.2.
- **Gate:** `go test ./internal/config/...` green. Unlike earlier revisions, existing config tests **do** change — that is expected here and is the one place in this plan where it is.

### Phase 2 — `/model` enforcement

- [ ] REPL: gate the switch, generate the usage string from `models` (§6.1).
- [ ] TUI: same against `m.config` (§6.2).
- [ ] Tests: §9.4.
- **Gate:** `go test ./internal/cli/...` green; `/model <bad>` leaves the session model untouched.

### Phase 3 — Session resume

- [ ] `Session`: add `AllowedModels`, `AllowUnlistedModel`, `ModelSubstituted`; populate in `NewSession` (§6.3).
- [ ] Gate **both** assignment sites in `persistence.go` (`:284`, `:313`).
- [ ] Surface the substitution at all four call sites (§1.6) — no `os.Stderr`.
- [ ] Tests: §9.5.
- **Gate:** `go test ./internal/chat/... ./internal/cli/...` green; a session saved under a since-removed model still opens.

### Phase 4 — Display

- [ ] Extract `formatConfigShow`; add `models=` and `model_policy=` (§7.1, §7.4).
- [ ] `doctor`: add `models:`, delete the deepseek branch (§7.2).
- [ ] Tests: §9.3.
- **Gate:** `go test ./internal/cli/...` green — the table test is the gate. `mivia doctor` spot-checked by hand for both modes.

### Phase 5 — Example + docs (land with or before Phase 1)

- [ ] Rewrite `.mivia/mivia.toml` and `.mivia/mivia.toml.example` (§8.2): `model` → `default_model`, `models` on deepseek + zai, openrouter left unrestricted.
- [ ] Drop the stale deepseek comment; keep zai's endpoint warning (§8.2).
- [ ] Update `docs/product/config.md`: rewrite the model table (`:28-31`) and the three `model =` samples (`:62, 67, 70`); add the allowlist subsection (§8.3).
- [ ] Add `TestExampleConfigModelsField`; update `TestExampleConfigIncludesZAI`.
- **Gate:** `make verify` green; `mivia doctor` loads the live config in both modes.

### Checklist

- [ ] `model` key is gone from both config files, docs, and all test fixtures.
- [ ] A leftover `model =` fails loudly, never silently (D2 guard + `toml:"-"`).
- [ ] `default_model` works **with and without** `models` (§1.9 openrouter case).
- [ ] Default chain is `default_model` → `models[0]` → descriptor (D3).
- [ ] Descriptor default never applies in managed mode (D4).
- [ ] No `models` declared → `--model` / `/model` / resume accept anything.
- [ ] All four enforcement points gated (§6) — especially resume.
- [ ] `MIVIA_ALLOW_UNLISTED_MODEL=1` bypasses all four; resolved once at load.
- [ ] `normalizeModels` allocates fresh; empty-entry errors name the declared index.
- [ ] No change to `provider.Request`, send path, or client factories.
- [ ] `doctor`'s deepseek hardcode and the REPL usage-string hardcode both deleted.
- [ ] No load-time network calls; no registry validation (D6).

---

## Appendix A — Sharp edges acknowledged, not fixed here

1. **Subagent model staleness:** `attachSessionDispatcher` freezes `res.Model`
   for subagent/skill handlers; `/model` switches never propagate. Pre-existing
   and orthogonal — but note enforcement makes it *more* visible, since the set
   of legal models is now explicit while subagents silently ignore switches
   within it. Separate plan.
2. **Whole-file provider validation:** §5.4 — malformed `models` in a non-active
   provider block is not caught until that provider is selected. Consistent with
   every other provider field.
3. **No completion UI:** this plan makes a `/model` picker or Tab-completion
   possible by giving the binary a model list for the first time. Wiring it is
   separate work; `.mivia/plans/composer-autocomplete.md` currently designs
   `/model` as `PreferInsert` (free-typed) and would need revising to consume
   `ModelChoices()`.

## Appendix B — Revision log

**rev 4, 2026-07-31 — `model` removed outright.** mivia is unpublished with one
user, so no deprecation window is owed. Verified §1.9: the only two `mivia.toml`
files in existence are tracked here, and `~/.config/mivia/` does not exist.

Changes from rev 3: `model` deleted from the schema outright — **no shim, no
alias, no rename guard, no test acknowledging it**. The resolved value moves to a
derived `ResolvedModel \`toml:"-"\`` field so nothing in `ProviderConfig` maps to
`model` at all. `default_model` promoted to work in **both** modes, which is what
makes the removal viable — rev 3 made it managed-mode-only and would have left the
live openrouter block (`default_model` with no allowlist, §1.9) inexpressible. D3
became a single ordered chain. Phase 5 promoted to a hard dependency of Phase 1,
since the tracked config stops loading in between.

An intermediate draft of this revision proposed a four-line `LegacyModel` guard to
turn a stale `model =` key into a loud error. Rejected: zero configs can carry the
key (§1.9), so the guard would have been permanent dead weight bought against an
empty risk.

**rev 3, 2026-07-31 — advisory → enforcing.** Product decision: `models`
restricts what `/model` and `--model` accept; `model` is retired in favor of
`models[0]` + optional `default_model`.

Inverted from rev 2: D2 (`model` authoritative → retired), D3 (contradiction
allowed → hard error), D4 (overrides unrestricted → gated), §7.3 (`doctor`
untouched → `doctor` lists the set and loses its hardcode). Blast radius
reclassified SMALL → MEDIUM; "purely additive" and "no behavior change visible"
withdrawn.

Added from codebase re-verification: §1.5 (no-config operation forces the opt-in
rule), §1.6 (session resume is a fourth enforcement point that bypasses the
gate), §1.3 (both `/model` surfaces already hold `*config.Resolved`, so the gate
needs no `Session` change), §1.7 (three hardcodes this generalizes), §1.8
(go-toml ignores unknown keys — why `model` cannot just be deleted), D5
(`MIVIA_ALLOW_UNLISTED_MODEL`, mirroring `MIVIA_ALLOW_INSECURE_HTTP`), D8 (resume
falls back and warns).

**rev 2, 2026-07-31 — challenge pass on the advisory design.** Corrected stale
line numbers; fixed an in-place-aliasing defect and a dedup/validate index skew
in the normalization helper (both carried into rev 3 §5.1); removed a false
`composer-autocomplete.md` dependency claim. Superseded by rev 3, which gives the
field the real consumer rev 2 concluded it lacked.
