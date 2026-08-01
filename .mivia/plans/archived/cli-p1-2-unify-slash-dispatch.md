# P1.2 - Unify the slash-command dispatch layer

**Status:** Implemented (2026-07-31) - pure slash helpers in `slash_shared.go`
(`parseModelArgs`, `parseNonNegInt`, formatters, `modelRestoreNoticeText`) +
`terminalSlashSink`; classic REPL and TUI dispatch rewired; TUI `switchModel`
delegates to `switchModelCommand`; three model-restore sites share one notice text.
`slash_catalog` untouched. Characterization tests in `slash_shared_test.go`.
**Date:** 2026-07-31
**Depends on:** nothing (independent of the other P1 items; the review's suggested
execution order places P1.2 last among P1, after P1.1/P1.3/P1.4, but it carries no
code dependency on them). **Rides along:** P2.4 (split `handleSlashImpl`) and P2.7
(generate `slashHelp` from catalog) are natural follow-ons but are **out of scope**
here - listed in §9 so they are not silently dropped.
**Blocks:** P2.4 (the `handleSlashImpl` split folds cleanly into the shared layer
once it exists).
**Blast radius:** MEDIUM - touches the two interactive command surfaces (classic
REPL + TUI) that every interactive session drives. No privilege surface, no
persisted-state migration, no untrusted-data boundary. The risk is behavioural
drift between the two surfaces, which is *already* pinned by a cross-surface parity
test (`budget_integration_test.go:26`).

---

## 1. Problem

Slash **discovery** is correctly centralized: `slash_catalog.go` is the single source
of truth for which commands exist, their aliases, surface scoping, and skill
collisions. `slash_catalog_test.go` pins that table. **Dispatch is not.** Two
hand-maintained switches carry ~6 copy-pasted concerns, and the two surfaces differ
only in *output sink* (`term.WriteString` vs `m.appendInfo`/`m.appendBlock`) plus a
set of surface-specific side-effects (dialog opens, in-memory block rebuilds).

The duplication, verified at HEAD:

| Concern | Classic REPL | TUI |
|---|---|---|
| top-level switch | `chat_slash.go:19` `handleSlash` | `tui_slash_handlers.go:18` `handleSlashImpl` |
| `/model` arg-parse | `chat_slash_handlers.go:26` (`handleSlashInfo` `case "/model"`) | `tui_slash_handlers.go:54` |
| `/budget` | `chat_slash_handlers.go:108` `handleBudget` | `tui_slash_handlers.go:91` |
| `/steps` | `chat_slash_handlers.go:76` (`handleSlashLimits`) | `tui_slash_handlers.go:108` |
| `/save` `/load` `/delete` `/list` `/session` | `chat_slash_handlers.go:124` `handleSlashSessions` | `tui_slash_handlers.go:135,143,163,181` |
| `/resume` | `chat_slash_handlers.go:209` `handleSlashResume` | `resume.go:207` `handleResumeSlash` |
| model-restore notice | `chat_slash_handlers.go:167` `writeModelRestoreNotice` | `tui_slash_handlers.go:220` `appendModelRestoreNotice` |

Three specific duplications, in increasing order of risk:

1. **The `/model` arg-parsing block is byte-for-byte duplicated.** The
   `if len(fields) >= 3 { providerName = fields[1]; modelName = strings.Join(fields[2:], " ") }`
   block is character-identical at `chat_slash_handlers.go:40-43` and
   `tui_slash_handlers.go:58-61`.

2. **Two near-identical model-switch functions.** `switchModelCommand`
   (`model_binding.go:109`) and `(m *tuiModel).switchModel` (`model_dialog.go:276`)
   have identical bodies - `PrepareBinding` → fallback `SelectModel` →
   `buildModelBinding` → `SwitchBinding` - differing only in the `m.config != nil`
   nil-guard the TUI version carries. `switchModelCommand` is already shared by the
   REPL slash path (`chat_slash_handlers.go:46`) and the model dialog test
   (`model_dialog_integration_test.go:171`); the TUI carries its own copy.

3. **The model-restore notice exists three times.** `writeModelRestoreNotice`
   (`chat_slash_handlers.go:167`), `(m *tuiModel).appendModelRestoreNotice`
   (`tui_slash_handlers.go:220`), and an inline copy in `chat_repl_loop.go:70`
   (`restore()`). All three read `sess.ModelRestoreNotice()` and format the same
   sentence; one wording fix can silently drift across three sites.

### 1a. Severity - MEDIUM, and why it is still worth doing

No incorrect behaviour results today: the parity test
(`TestIntegrationModelBindingBudgetCommandParityAcrossREPLAndTUI`,
`budget_integration_test.go:26`) proves `/budget` agrees across surfaces, and the
catalog keeps discovery honest. The cost is **drift risk, not malfunction**: a fix
landed in one switch (e.g. the `/load` usage typo - note `chat_slash_handlers.go:131`
prints `"usage: /load <name"` with a missing closing `>`, while the TUI at
`tui_slash_handlers.go:162` prints the correct `"usage: /load <name>"`) is already
evidence the two copies are diverging. This is exactly the class of bug a shared
layer prevents.

---

## 2. Goals and non-goals

### Goals

- Collapse the byte-for-byte and near-byte-for-byte duplication listed in §1 into a
  single pure-logic module, so an argument-parsing or wording fix lands once.
- Keep the two output sinks distinct (`term.WriteString` vs `m.appendInfo`) behind a
  small `slashSink` interface - the surfaces legitimately render differently
  (terminal prose vs styled chat blocks), and that difference must not be flattened.
- Preserve every observable behaviour, including the cross-surface parity that
  `budget_integration_test.go:26` enforces.
- Leave the discovery layer (`slash_catalog.go`) untouched - it is correctly owned
  and separately tested; this plan touches **dispatch only**.

### Non-goals

- Do **not** unify the surface-specific side-effects (TUI dialog opens,
  `m.messages = nil`, `HydrateChatBlocksForView` rebuilds, `pendingResume`/`pendingSelectCmd`
  staging). Those are genuine surface differences, not duplication - folding them into
  a shared layer would force the shared code to know about `*tuiModel`. See §3 (why B
  is rejected for the side-effects) and §9.
- Do **not** change command semantics, wording, aliases, or the set of handled
  commands. The `/load` usage typo (`chat_slash_handlers.go:131`) is **out of scope**
  for this refactor - fixing it now would change behaviour under the "no behaviour
  change" constraint. It is recorded in §9 as a follow-up to land *after* the shared
  layer exists, where it becomes a one-line fix.
- Do **not** touch P2.4 (`handleSlashImpl` split) or P2.7 (generate `slashHelp`) -
  see §9.
- Do **not** add a new handler field to `SlashCommand`. The discovery struct stays
  pure data (§3, option B rejected).

---

## 3. Options - recommended: **A**

This mirrors the decision shape of `25-skill-triggers.md §3`. Two viable shapes were
considered; the deciding fact is in §3.3.

### 3.1 Option A - Extract pure logic into `slash_shared.go` + `slashSink`  ✅ RECOMMENDED

A new `slash_shared.go` holds the pure, sink-agnostic logic; each surface keeps a thin
adapter that calls into it and applies its own side-effects:

```go
// slashSink is the only thing the two surfaces differ on: where text goes.
// The classic REPL writes prose to the terminal; the TUI appends styled blocks.
type slashSink interface {
    Info(s string)     // a plain informational line
    Error(s string)    // an error line (surfaces may style differently)
}

// slash_shared.go - pure logic, no *Terminal, no *tuiModel, no chat.Block.
func parseModelArgs(fields []string, currentProvider, defaultProvider string) (provider, model string, hasArg bool)
func parseNonNegInt(fields []string) (n int, hasArg bool, ok bool)
func modelSwitchChoices(res *config.Resolved, providerName, defaultProvider string) string
func modelRestoreNoticeText(saved, current string) string
func formatBudgetSummary(budget int) string
func formatStepsSummary(steps int) string
func saveSessionResult(name string, msgs, turns int) string   // success line
func loadSessionResult(name string, msgs, turns int) string
func deleteSessionResult(name string) string
```

The surfaces keep their own `handleSlash*` entry points but each `case` body shrinks to
"parse via shared helper → mutate surface state → emit via `slashSink` or
surface-native call". The shared string formatters kill the wording-drift risk; the
shared parsers kill the arg-parsing drift. Side-effects (dialog opens, block rebuilds)
stay in the surface, where they belong.

Two thin `slashSink` adapters:
- `type terminalSlashSink struct{ t *Terminal }` → `Info`/`Error` call
  `t.WriteString("\n" + s)`, matching current REPL prose.
- The TUI does **not** need a sink struct: it calls `m.appendInfo`/`m.appendBlock`
  directly (those already exist at `tui_layout.go:227` / `chatblock.go:12`). Only the
  shared string formatters are called; the TUI keeps its own `appendInfo(formatResult(...))`
  calls. This keeps `slash_shared.go` free of any `*tuiModel`/`ChatBlock` import.

`switchModelCommand` (`model_binding.go:109`) becomes the single model-switch path:
the TUI's `(m *tuiModel).switchModel` (`model_dialog.go:276`) is rewritten to delegate
to `switchModelCommand(m.session, m.config, provider, model)`, dropping the duplicate
body. The `m.config != nil` guard moves into a thin wrapper or is preserved by passing
a non-nil `*config.Resolved` (the TUI always has one - `m.config` is set at construction;
verify in Wave 1).

`writeModelRestoreNotice` (`chat_slash_handlers.go:167`) becomes a one-liner that calls
`modelRestoreNoticeText` and writes via the sink; `(m *tuiModel).appendModelRestoreNotice`
(`tui_slash_handlers.go:220`) and the inline `chat_repl_loop.go:70` copy both call the
same formatter.

### 3.2 Option B - Per-surface handler on `SlashCommand` (catalog becomes dispatch table)

Add a handler to the catalog so the two top-level switches disappear entirely:

```go
type SlashCommand struct {
    // ...existing discovery fields...
    PlainHandler func(line string, sess *chat.Session, res *config.Resolved, term *Terminal) (bool, error)
    TUIHandler   func(m *tuiModel, fields []string) bool
}
```

`handleSlash`/`handleSlashImpl` reduce to `cmd := findSlashCommand(...); return cmd.handler(...)`.

**Rejected as the primary mechanism.** Three reasons, the third decisive:

1. **Couples discovery to dispatch.** `slash_catalog.go` is currently pure data +
  sorting + collision logic, with no dependency on `*Terminal` or `*tuiModel`. Putting
  handlers on `SlashCommand` makes it import both surfaces and the `chat`/`config`
  packages - inverting the clean direction the review praised ("discovery is correctly
  centralized"). `slash_catalog_test.go` would now transitively pull in TUI machinery.

2. **Two handler fields per command is awkward.** A single signature cannot serve both
  `*Terminal`+`*chat.Session` (REPL) and `*tuiModel` (TUI) without an interface that
  abstracts *both* the sink *and* the surface-specific side-effects - at which point B
  collapses into A with extra indirection.

3. **The surfaces' side-effects are not duplication.** (§2 non-goal, §3.3.) `/model` in
  the TUI opens a dialog (`m.openModelDialog()`, `tui_slash_handlers.go:56`) and sets
  `m.modelName`; in the REPL it prints prose. `/load` in the TUI clears `m.messages`,
  rebuilds blocks via `HydrateChatBlocksForView`, and resets `m.msgOffset`
  (`tui_slash_handlers.go:147-156`); the REPL calls `NewChatRenderer(...).RenderHistory`.
  These are genuine, irreducible surface differences. A dispatch-table handler can
  *call* the surface code, but it cannot *share* it - so B removes the switches while
  leaving the actual duplicated logic (arg parsing, wording, model-restore) untouched.

B is the "larger" option the review names ("give `SlashCommand` a per-surface handler so
the catalog becomes the dispatch table - kills both switches"). It does kill the
switches, but it does not kill the duplication this plan exists to remove. **If the
switches themselves are the pain** (they are P2.4's concern), B is the right tool - but
that is a different, later goal.

### 3.3 The deciding fact

> The two surfaces differ not only in *output sink* but in *side-effects on their own
> model state*. The duplication is in the **pure logic** (arg parsing, string wording,
> model-switch binding), and that is exactly what A extracts. B moves the dispatch
> *site* but leaves the duplicated *logic* in place.

**Decision: A.** Extract pure logic into `slash_shared.go` behind a `slashSink`
interface. B remains a viable *follow-on* for P2.4 (switch removal) and should not be
blocked by this plan landing first - A and B are composable (a future catalog-dispatch
table can route to the shared helpers).

---

## 4. Architecture

```
                ┌─────────────────────────────────────────┐
                │            slash_shared.go (NEW)         │
                │  pure: parseModelArgs, parseNonNegInt,   │
                │        modelSwitchChoices, formatters,   │
                │        modelRestoreNoticeText            │
                │  imports: config, strings, strconv ONLY  │
                └───────────────┬─────────────┬───────────┘
                                │             │
              (formatters/parsers)        (formatters)
                    │                           │
        ┌───────────▼──────────┐    ┌───────────▼──────────────┐
        │  classic REPL         │    │  TUI                     │
        │  chat_slash.go        │    │  tui_slash_handlers.go   │
        │  chat_slash_handlers  │    │  (keeps side-effects:    │
        │  + terminalSlashSink  │    │   dialog open, block     │
        │  (term.WriteString)   │    │   rebuild, appendInfo)   │
        └───────────────────────┘    └──────────────────────────┘
```

- `slash_shared.go` imports **only** `config`, `strings`, `strconv`. It must **not**
  import `chat`, `terminal`, or reference `*tuiModel`/`ChatBlock`. This keeps the
  dependency direction one-way (surfaces → shared) and makes the pure helpers unit-
  testable without spinning up a session or TUI.
- `slashSink` lives in `slash_shared.go` as a 2-method interface. The classic REPL
  gets a `terminalSlashSink` adapter (5 lines); the TUI needs no adapter - it calls
  `m.appendInfo(formatter(...))` directly, because its sink (`appendInfo`/`appendBlock`
  on `*tuiModel`) already renders styled blocks and must stay on the model.
- `switchModelCommand` (`model_binding.go:109`) becomes the sole model-binding path;
  the TUI's `m.switchModel` delegates to it.
- The model-restore notice text is centralized in `modelRestoreNoticeText`; all three
  call sites (`chat_slash_handlers.go:167`, `tui_slash_handlers.go:220`,
  `chat_repl_loop.go:70`) call it.

### 4.1 ADLC compliance

Per `05-adlc-agentic-development-lifecycle.md`:
- **Step 0 (Challenge & Lock):** this plan is DESIGN-READY. Before code, dispatch 2-3
  hostile reviewers (one applying skill `architecture-review` for structure/boundary
  check, one for correctness). The `slashSink` interface width and the `m.config != nil`
  handling in the delegated `switchModel` are the points most likely to draw findings.
- **Step 1 (Micro-tasks):** 1 file per task; a RED test task precedes each production
  task. Waves in §6.
- **Step 4 (TDD):** RED → GREEN per helper. Because this is a refactor, the RED tests are
  *characterization* tests that pin the **current** output of each extracted helper
  before extraction (so the extraction is provably behaviour-preserving).
- **Step 5 (Bug audit):** hostile auditors specifically check for wording drift and
  parity regression.

---

## 5. Implementation waves

Every production task follows a compiling RED test that fails an assertion before the
implementation is added. Because this is a refactor with a "no behaviour change"
constraint, RED tests are **characterization tests**: they assert the exact strings and
parse results the code produces *today*, so extraction is provably lossless.

| Wave | File | Type | Change |
|---|---|---|---|
| 1 | `internal/cli/slash_shared_test.go` (new) | test (RED) | Table-driven characterization of `parseModelArgs`, `parseNonNegInt`, `modelSwitchChoices`, `modelRestoreNoticeText`, and the `saveSessionResult`/`loadSessionResult`/`deleteSessionResult`/`formatBudgetSummary`/`formatStepsSummary` formatters. Each case pins the **current** output (copy the exact strings from `chat_slash_handlers.go` and `tui_slash_handlers.go` at HEAD). Include the `/model` 1-arg, 2-arg (`/model gpt-4o`), and 3-arg (`/model openrouter gpt-4o`) shapes. |
| 1 | `internal/cli/slash_shared.go` (new) | prod (GREEN) | `slashSink` interface + all pure helpers from the test. Imports `config`, `strings`, `strconv` only. **No `chat`/`terminal`/tui import.** |
| 1 | reviewer (in-context) | review | Reads `slash_shared.go`; confirms no surface imports, interface is 2-method, helpers are pure. |
| 2 | `internal/cli/chat_slash_handlers.go` | prod | Rewrite `handleSlashInfo` `/model` case to call `parseModelArgs` + `modelSwitchChoices`; rewrite `/budget`/`/steps`/`/save`/`/load`/`/delete` cases to call shared formatters; `writeModelRestoreNotice` → `modelRestoreNoticeText` via a `terminalSlashSink`. **Characterization tests must stay green.** |
| 2 | `internal/cli/terminal.go` (or `chat_slash_handlers.go`) | prod | Add `terminalSlashSink` adapter (`Info`/`Error` → `t.WriteString("\n"+s)`). |
| 2 | reviewer (in-context) | review | Diff the rewritten cases against HEAD output; confirm prose identical. |
| 3 | `internal/cli/tui_slash_handlers.go` | prod | Rewrite `/model`, `/budget`, `/steps`, `/save`, `/load`, `/delete` cases to call shared parsers/formatters; `appendModelRestoreNotice` → `modelRestoreNoticeText`. **Keep all side-effects** (dialog opens, `m.messages`/`m.blocks` clears, `HydrateChatBlocksForView`, `m.msgOffset`). |
| 3 | reviewer (in-context) | review | Confirm side-effects preserved; `budget_integration_test.go` parity holds. |
| 4 | `internal/cli/model_dialog.go` | prod | Rewrite `(m *tuiModel).switchModel` (`:276`) to delegate to `switchModelCommand(m.session, m.config, ...)`. Handle the `m.config == nil` case explicitly (verify whether it can occur - `m.config` is set at TUI construction). |
| 4 | `internal/cli/chat_repl_loop.go` | prod | Replace inline model-restore at `:70` with `modelRestoreNoticeText`. |
| 4 | reviewer (in-context) | review | `model_dialog_integration_test.go` + `provider_model_test.go` green; both model-switch paths identical. |
| 5 | (audit) | audit | Hostile audit (Step 5): wording drift, parity regression, dead sinks, missed call sites. |

**Wave gates (ADLC Step 4):** after each wave, `go build ./... && go test -race
./internal/cli/...` must pass. RED phase for Wave 1 is the characterization test
failing on undefined symbols (assertion-targeted, not a bare compile error per ADLC).

### 5.1 Dependency ordering rationale

Wave 1 (pure helpers + their tests) lands first because nothing else can be extracted
without them, and they carry zero surface coupling - lowest risk. Waves 2 and 3
(REPL then TUI) are independent surfaces and could parallelize, but are sequenced so
the parity test (`budget_integration_test.go:26`) is green after each rather than only
at the end. Wave 4 (model-switch + third restore site) is last because `switchModel`
delegation is the subtlest change (nil-config handling) and is pinned by the heaviest
existing tests (`model_dialog_integration_test.go`, `provider_model_test.go`).

---

## 6. Verification

Minimum gates, run after every wave and at completion:

```text
go test ./internal/cli/... -count=1
go test -race ./internal/cli/... -count=1
go vet ./...
go build ./...
make verify          # structure gate (check_go_structure --strict --all internal/cli)
```

Targeted regression guards (these already exist and must stay green - they are the
behaviour-preservation proof):

- `TestIntegrationModelBindingBudgetCommandParityAcrossREPLAndTUI`
  (`budget_integration_test.go:26`) - **the cross-surface parity invariant.** If this
  breaks, the refactor changed behaviour; halt and revert.
- `TestSlashNewPersistsOldSessionAndClears` / `TestHandleSlashNewClassic`
  (`new_session_slash_test.go:51,160`) - `/new` save+clear on both surfaces.
- `model_dialog_integration_test.go:171` and `provider_model_test.go:66`
  (`switchModelCommand`) - model-switch binding unchanged.
- `slash_catalog_test.go` - discovery untouched (regression guard: this plan must not
  edit the catalog).

New tests added by this plan (Wave 1 RED): the `slash_shared_test.go` characterization
table. Per ADLC Step 6 TDD audit, every new production file (`slash_shared.go`) has a
corresponding `_test.go`.

Manual spot-check: drive `/model <p> <m>`, `/budget 100`, `/budget 0`, `/steps 5`,
`/save x` / `/load x` / `/delete x`, and `/resume` in **both** `mivia chat` and
`mivia chat --plain`; confirm identical success wording and error wording to pre-refactor
(screenshots/diffs optional - the characterization tests encode the strings).

---

## 7. Rollback

Each wave is independently revertible because behaviour is unchanged - reverting a wave
returns to the prior copy-pasted state, which was correct.

- **Primary rollback:** revert the wave commits. No data migration, no persisted-state
  concern (slash commands are ephemeral session actions).
- **Partial rollback:** if Wave 3 (TUI) reveals a side-effect was lost, revert Wave 3
  only; Waves 1-2 (shared helpers + REPL) stand on their own and remain correct.
- **Kill criterion:** if the parity test (`budget_integration_test.go:26`) cannot be
  kept green after extraction, the extraction mis-characterized a surface difference -
  return to Step 0 and re-examine whether that concern is genuinely pure (it may belong
  in the surface, not `slash_shared.go`). Do **not** paper over a parity break by
  tweaking the shared formatter to special-case a surface.

---

## 8. Plan scorecard

| Criterion | Score | Notes |
|---|---|---|
| Compiles | PASS | additive new file + in-place rewrites |
| No cycles | PASS | `slash_shared.go` imports only `config`/`strings`/`strconv`; surfaces import it, not vice-versa |
| No breaking API | PASS | all changes are internal (unexported helpers); no public signature changes |
| Testable in isolation | PASS | pure helpers unit-testable without a session or TUI |
| Backward-compatible config | PASS | no config touched |
| Every function has a test | PASS | Wave 1 RED covers every extracted helper |
| No behaviour change | PASS (by construction) | characterization tests pin current output; parity test guards cross-surface |

---

## 9. What this does NOT solve (deliberately deferred)

These are recorded so they are not silently dropped; each is a separate, later plan:

- **The `/load` usage typo** (`chat_slash_handlers.go:131` prints `"usage: /load <name"`,
  missing `>`; TUI at `tui_slash_handlers.go:162` is correct). Out of scope under the
  "no behaviour change" constraint. After this plan lands, it is a one-line fix in the
  now-shared formatter - but fixing it *during* the refactor would violate the
  behaviour-preservation proof. Land it as a follow-up commit once `loadUsageHint` is
  shared.
- **P2.4 - split `handleSlashImpl`** (`tui_slash_handlers.go:11-216`, ~205 LOC flat
  switch). This plan extracts the *logic* but leaves the *switch structure* intact.
  P2.4 can either mirror the REPL's `handleSlashInfo`/`handleSlashLimits`/`handleSlashSessions`
  split, or adopt option B (§3.2) now that the shared helpers exist.
- **P2.7 - generate `slashHelp` from the catalog** (`chat.go` `const slashHelp` is
  hand-maintained and has drifted: omits `/resume`, contains mojibake `â†‘ â†“`). This
  plan does not touch help text.
- **Surface-specific side-effects are not unified** (TUI dialog opens, block rebuilds,
  `pendingResume` staging). They are correct per-surface and must not be folded into
  `slash_shared.go` (§2 non-goal, §3.3).
- **`switchModel` vs `switchModelCommand` nil-config divergence.** Wave 4 unifies them,
  but if `m.config` can legitimately be nil at the call site, the delegation must guard
  it rather than assume - verify in Wave 1 before Wave 4.

---

## 10. Reachability - required by `architecture-review` step 2

| Element | Prod callers at HEAD | Callers this change adds | Verdict |
|---|---|---|---|
| `parseModelArgs` | 0 (inlined ×2) | 2 (REPL `/model`, TUI `/model`) | Not a finding; this change is the remedy |
| `modelRestoreNoticeText` | 0 (inlined ×3) | 3 (`chat_slash_handlers.go:167`, `tui_slash_handlers.go:220`, `chat_repl_loop.go:70`) | Not a finding |
| `terminalSlashSink` | 0 | 1 (REPL handlers) | Not a finding |
| `switchModelCommand` (as sole path) | 2 (`chat_slash_handlers.go:46`, test) | +1 (TUI `m.switchModel` delegates) | Already reachable; widening use |

No element is added without a production caller in the same plan. The
`25-skill-triggers.md §14` lesson applies: a reachability table is only worth writing
if something verifies it after landing - the Wave reviewers (§5) must confirm each
helper has ≥1 caller at merge.
