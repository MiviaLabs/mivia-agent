# `internal/cli` — Structural Refactoring Review

**Scope:** `internal/cli/*.go` (package `cli`, ~90 production files, ~16k LOC + tests).
**Method:** Four parallel structural reviews over logical slices (TUI core, Chat/REPL,
Render/Bubbles, Orchestration/Dialogs/Misc). All claims verified against code at HEAD
via `grep`/`read_file`.
**Note:** This is an *advisory* architecture review of the shipped package — not a gate
review of a specific change. Findings are ranked by ROI (value ÷ risk). The structure
gate (`scripts/check_go_structure.py --strict --all internal/cli`) **passes** today.

---

## Overall Structural Health

**Moderately good.** The historical `tui.go` god-object (baseline cap 1682) has been
decomposed into ~90 focused files; `tui.go` is now 485 lines of type definition + ctor.
The `keymap.go` registry, the `dialog_geometry`/`dialog_compositor` base-plus-modal
system, the `MarkdownWriter` streaming pipeline, and the orchestration-handle/retention
registry are genuinely well-abstracted. Long functions (>120 LOC) are rare.

The systemic problems cluster in four areas:

1. **No color/style theme** — raw `lipgloss.Color("8")` literals ×~45 across 12 files,
   plus *two duplicate ANSI-constant vocabularies* (`ansi*` vs `hl*`).
2. **Duplicated slash-command dispatch** between the classic REPL and TUI.
3. **Copy-pasted access-control / SQLite-open blocks** in the orchestration tools.
4. **Tool/handler-name string literals** spread across ~10 files with no shared consts.

Plus a handful of dead code and magic-number promotions.

---

## Priority 1 — High-value, low-risk (do first)

These are mechanical, isolated, and already pinned by tests.

### P1.1 — Create a `theme.go`; consolidate color/style ownership  *(render slice)*
**Problem:** One color palette, three representations.
- `lipgloss.Color("8")`/`("9")`/`("14")`/`("236")` etc. appear as raw strings ~45×
  across 12 files (`tui.go:26-40`, `toolui.go:210-231`, `tui_run_dashboard.go:21-26`,
  `msgcard.go:46-47`, `fleetbox.go:50`, `livepanel.go:131`, `bubble_leftrail.go:37`…).
- **Two parallel ANSI-constant blocks** for the same colors: `ansiYellow/ansiCyan/…`
  (`markdown.go:16-31`) and `hlYellow/hlCyan/…` (`highlight.go:11-26`) — e.g. both
  `ansiCyan` and `hlCyan` = `"\033[36m"`, both `ansiBgDark` and `hlBgDark` = `"\033[48;5;236m"`.
- **Duplicate semantic style vars:** `tuiUserLabel`/`userLabelStyle`/`userRailStyle`
  (three identical color-12-bold), `tuiDimStyle`/`toolDimStyle`, `tuiErrorStyle`/`toolErrStyle`.
- **Inconsistency already present:** brand error is `chromeError="160"` but `tuiErrorStyle=9`.
- `tuiUserCardBg` (`tui.go:27`) is declared but **never used** (duplicates `_userBgStyle`).

**Refactor:** Extend `brand.go` (which already centralizes phase colors and the `chrome*`
tokens in `bubble_leftrail.go:13`) into a single theme module owning semantic named
colors + the consolidated style vars. Have `highlight.go`/`markdown.go` import the ANSI
consts; delete the `hl*` block and the `tui*Style`/`tool*Style` duplicates.

### P1.2 — Unify the slash-command dispatch layer  *(chat/REPL slice)*
**Problem:** Slash **discovery** (`slash_catalog.go`) is correctly centralized, but
**dispatch** is hand-maintained twice with ~6 copy-pasted concerns:

| Concern | Classic REPL | TUI |
|---|---|---|
| top-level switch | `chat_slash.go:19` (`handleSlash`) | `tui_slash_handlers.go:18` (`handleSlashImpl`) |
| `/model` parse | `chat_slash_handlers.go:26` | `tui_slash_handlers.go:54` |
| `/budget` `/steps` | `chat_slash_handlers.go:84` | `tui_slash_handlers.go:91` |
| `/save` `/load` … | `chat_slash_handlers.go:124` | `tui_slash_handlers.go:129` |
| `/resume` | `chat_slash_handlers.go:209` | `resume.go:207` |
| model-restore notice | `chat_slash_handlers.go:168` | `tui_slash_handlers.go:220` |

The `/model` arg-parsing block is byte-for-byte duplicated. The two surfaces differ
only in *output sink* (`term.WriteString` vs `m.appendInfo`).

**Refactor (safe):** Extract pure logic into a shared `slash_shared.go`
(`parseModelArgs`, `parseNonNegInt`, `saveSession`/`deleteSession`, `modelRestoreNoticeText`)
with a small `slashSink` interface for output. *Better (larger):* give `SlashCommand` a
per-surface handler so the catalog becomes the dispatch table — kills both switches.
This is exactly what the baseline notes ("share slash with TUI") point at.

### P1.3 — Extract `accessibleOrchestrationHandle` + error consts  *(orchestration slice)*
**Problem:** The identical ~8-line run-handle lookup + accessibility gate is copy-pasted 4×:
`orchestrate.go:357` (`inspect_agents`), `orchestrate_lifecycle.go:160` (`join_run`),
`orchestrate_lifecycle.go:247` (`cancel_run`), `ledger_tools.go:301` (`list_run_events`).
The JSON literals `{"error":"unknown run_id"}` (×8) and `{"error":"run_id is required"}` (×4)
are inlined at each site. The repetition is *deliberate* (unknown ≡ inaccessible, INV-AG-9),
so the helper must preserve that indistinguishability.

**Refactor:**
```go
func accessibleOrchestrationHandle(ctx, runID, dispatcher, repo) (*orchestrationHandle, string)
const errJSONUnknownRunID = `{"error":"unknown run_id"}`
const errJSONRunIDRequired = `{"error":"run_id is required"}`
```
Tests already pin the exact JSON strings. ~32 lines removed across 4 files.

### P1.4 — Extract `openDurableLedgerRepo`  *(orchestration slice)*
**Problem:** The SQLite-open + recover + report block is triplicated:
`dispatcher.go:44`, `dispatcher.go:76`, `orchestration_state.go:196`. All three repeat
`storage.OpenSQLite` → identical `fmt.Fprintf(os.Stderr, "warning: failed to open SQLite
store %q: %v; falling back to memory backend\n", …)` → `ledger.NewStorageLedgerRepository`
→ `Recover` → `reportInterruptedRuns`. One wording fix can silently drift.

**Refactor:** `func openDurableLedgerRepo(cfg) (repo, ownedStore)` consumed by all three.

### P1.5 — Delete dead code  *(all slices)*
Verified zero non-test callers:
- **`applyToolEventFromBus` / `applyToolStartFromBus` / `applyToolEndFromBus`**
  (`tui_events.go:108-208`, ~100 LOC) — comment admits "Retained for tests"; grep shows
  no callers at all. Duplicates `applyToolEventsOpts`. **Delete all three.**
- **`newSessionDispatcher` + `newSessionDispatcherWithContext`** (`dispatcher.go:114,118`)
  — unexported wrappers with zero callers (only a comment in a test file). **Delete.**
- **`renderLabeledBody` + `renderStacked`** (`messagebubble.go:424,469`, ~70 LOC) —
  unreachable; `Render` goes through `renderBodyLines`/`renderPlain`. **Delete.**
- **`renderHalfBlocks`** (`pixel.go:202`) — "useful later", never called. **Delete.**
- **`logoFramesLegacy`** (`logo.go:253`) — no non-test caller, no test reference. **Delete.**
- **`formatModelHeader`/`formatModelFooter`** (`msgcard.go:52,59`) — return `""`, exist
  only to keep two tests green. **Delete functions + tests.**
- **`tuiUserCardBg`** (`tui.go:27`) — declared, never used. **Delete.**
- **`makeAgentUIWithRenderer`** (`classic_agent_ui.go:209`) — "legacy, test-only"; sole
  user is `renderer_test.go`. **Inline into the test or delete.**

---

## Priority 2 — Medium-value

### P2.1 — Centralize tool/handler-name string literals  *(orchestration slice)*
The set `{multi_step, delegate, oneshot, dispatch_tasks, spawn_agent, join_run,
inspect_agents, cancel_run}` is re-declared as literals across ~10 files
(`action.go:19`, `orchestrate.go:167`, `dispatch.go:143`, `model_binding.go:69`,
`dispatcher.go:168`, `tool_verbs.go:37`, `toolui.go:164`, `prompt.go`…). The enum list
`[]string{"multi_step","delegate","oneshot"}` is byte-for-byte duplicated between
`orchestrate.go:167` and `dispatch.go:143`. **Refactor:** a `tool_names.go` with consts +
`builtinHandlerNames` slice + `injectHandlerEnum` helper.

### P2.2 — Unify diff-line coloring (3-4 implementations)  *(render slice)*
The classify-by-prefix (`+++/---/@@/+/-`) → color logic exists in:
`highlight.go:329` (`highlightDiffLine`), `markdown.go:311` (`formatCodeLine`),
`toolui.go:272` (`colorDiffLine`), and again in `diff_render.go:9`. They even disagree
(`@@` is magenta in markdown/highlight but dim in `colorDiffLine`). **Refactor:** one
`renderDiffLine(line) string` in `diff_style.go` using theme tokens.

### P2.3 — Collapse `NewSessionDispatcher*` constructor explosion  *(orchestration slice)*
`dispatcher.go` exposes 5 public constructors + (the now-dead) 2 unexported ones, all
threading combinations of `(repo, maxContextTokens, maxTokens, budget)`. **Refactor:**
one `NewSessionDispatcher(opts SessionDispatcherOpts)` struct + a thin convenience
wrapper. Removes the over-abstraction.

### P2.4 — Split `handleSlashImpl` (~205 LOC)  *(chat/REPL slice)*
`tui_slash_handlers.go:11-216` is a flat ~16-case switch. The classic REPL already split
its equivalent into `handleSlashInfo`/`handleSlashLimits`/`handleSlashSessions`. Mirror
that split (or fold into the P1.2 registry).

### P2.5 — `toolPanelState.reindex()` helper  *(TUI slice)*
The two-line idiom `m.toolPanel.ordered = orderToolIndices(m.toolRows)` +
`clampToolScroll(...)` is repeated ~7× (`tui.go:341`, `tui_events.go:152,191`,
`tui_tools_apply.go:51,133`…). **Refactor:** method on `toolPanelState` → `m.toolPanel.reindex(m.toolRows)`.

### P2.6 — Use existing `ledger.TaskStatus*`/`RunStatus*` consts  *(orchestration slice)*
`tui_run_dashboard.go` and `dispatch.go` hardcode status words (`"completed"`, `"failed"`,
`"running"`, `"cancel_requested"`…) as literals while `orchestrate_salvage.go:43` and
`diagnostics.go:89` already use the typed constants. A typo (`"timed-out"` vs
`"timed_out"`) silently breaks status rollup. **Refactor:** use the typed consts.

### P2.7 — Generate `slashHelp` from the catalog  *(chat/REPL slice)*
`chat.go:~198` `const slashHelp` is a hand-maintained string that has drifted: it omits
`/resume` (a real handled command) and contains mojibake (`â†‘ â†“` for arrow glyphs).
**Refactor:** generate the command table from `slashCommands(...)` like the TUI's
`newHelpDialogFor` already does.

---

## Priority 3 — Low-value cleanup

### Extract to const (magic numbers/strings)
- **Terminal floor numbers** scattered unnamed: `80,24` (`chat.go:65`, `renderer_test.go:26`),
  `width<20`/`return 8` (`composer.go:15,26`, `chatblock_render.go:22`,
  `chatblock_workgroup.go:117`, `toolpanel.go:128`, `messagebubble.go:93`).
  → `const defaultTermWidth, defaultTermHeight = 80, 24` / `minCardWidth = 20`.
- **Status glyphs** (`✓ ✗ ◆ ◇ ▸ ▾`) hard-coded across `toolui.go`, `toolpanel.go:189`,
  `chatblock_render.go`, `brand.go`, `msgcard.go`. → a small `glyphs.go`.
- **Preview/cap widths**: `48` (fence bar, `markdown.go:297,307` — note a no-op
  `min(48,48)` at `highlight_blocks.go:151`), `56` (rule width), `+2`/`+3` table padding,
  `peekLines=6` (`diff_render.go:55`), `maxExpandedLines=50`. Follow the existing good
  pattern (`maxToolResultPreview=200` at `renderer.go:103`, `maxThinkingLines=6`).
- **Composer placeholder** `"Message mivia… Enter send · Alt+Enter newline · /help"` ×5
  (`tui.go:186`, `tui_keys.go:27,457,464`, `tui_message.go:269`). → const.
- **REPL prompt glyph** `" "+modelShort+" > "` ×3 (`chat_repl_loop.go:31,74,232`). → helper.
- **`MaxTokens: 4096`** repeated (`dispatcher.go:192,238`); **`10 * time.Minute`** retention
  ×3 (`orchestration_state.go:114,180`, `resume.go:72` — the last hardcodes instead of
  calling `orchestrationHandleRetention(cfg)`); **`3 * time.Hour`** join wait
  (`orchestrate_lifecycle.go:137`); **`25ms`** poll (`orchestrate.go:446`).
- **`Owner: "mivia"`** ×3 (`delegate.go:112`, `dispatch.go:274`, `orchestrate_spawn_tasks.go:49`).
- **Env vars** `MIVIA_CLIPBOARD_TTY`/`MIVIA_NO_MOTION`/`MIVIA_MOUSE`/`MIVIA_CONFIG` as raw strings.

### Small logic cleanups
- **`appendMsg` dead tail** (`tui_layout.go:223`): `if len==0 {return}; return` — no-op.
- **`onAssistant` dead branch** (`classic_agent_ui.go:155`): both arms set
  `interimPrinted=true`; collapse.
- **`renderStreamVP`** (`tui_layout.go:250`) is now a pure alias — inline or keep (documented).
- **`repositoriesMatch`** (`orchestration_state.go:99`) uses `reflect` for what is likely
  pointer equality — simplify to `==` if repos are always pointer-typed.

---

## What is FINE — do not refactor

- **`keymap.go`** — registry + `validateKeyRegistry` + `forbiddenKeys` is exemplary.
- **`dialog_geometry.go` / `dialog_compositor.go`** — the base-plus-modal system is
  correctly centralized; all 4 modals share geometry/frame/ANSI/overlay. *Do not* add a
  dialog registry abstraction (only 4 kinds, each with distinct measure logic).
- **`MarkdownWriter`** — clean streaming `io.Writer` pipeline; right-sized.
- **State-logo engine** (`logostate`/`logopaint`) — composable painters + `stateAnim` table.
- **Rail system** (`bubble_rail_roles` × `bubble_leftrail`) — clean role×state resolver.
- **Highlighter** (`langDefs` table) — data-driven, adequate ("OK-ish syntax tables" is fair).
- **Tool-panel windowing** (`toolPanelState`) — cohesive.
- **`root.go` command switch** — 5 commands, no shared options; a table would be over-engineering.
- **`statusFromErr` / `runTaskResults` / `storedResultRefs`** — correctly centralized.
- **`orchestrationHandle` + retention GC** — single registry, correct cleanup, deliberate
  unknown/inaccessible indistinguishability (INV-AG-9).
- **`subagentTracker`** — clean pure state machine.
- **`streamBridge`** — well-encapsulated; the done/turnID fencing is subtle but documented.
- **`tuiFocus` enum + `routeFocusKey`** — clean typed routing.

---

## Pattern-fitness notes (design patterns)

- **State machine hidden in booleans** *(TUI slice, MED, not urgent):* `waiting`/
  `cancelling`/`quitRequested`/`agentDone` (`tui.go:64,136,140,146`) = 4 booleans encoding
  ~5 valid states of 16. Worth a `cancelStage` enum *if* this area is already being
  modified; the comments make it survivable today. Do **not** merge with the already-typed
  `mode`/`focus` enums.
- **`BubbleRenderer` plugin API** *(render slice, LOW):* used by exactly 2 production
  renderers; `WithRenderer`/`MergeStyle` only in tests; `MergeStyle` ≡ `WithStyle`. Slightly
  over-abstracted — consider dropping `MergeStyle`. Not urgent.
- **Missing pattern — theme layer:** see P1.1 (the highest-leverage "pattern" gap).
- **`flagValue`/`flagVar` helpers** — correctly shared across subcommands. No CLI table needed.

---

## Suggested execution order

| Wave | Items | Why this order |
|---|---|---|
| 1 | P1.5 (dead code) | Zero risk, shrinks the surface before larger edits. |
| 2 | P1.1 (theme.go) | Unblocks P2.2 (diff unify) and names the colors P3 refers to. |
| 3 | P1.3, P1.4 (orchestration helpers) | Independent of theme; tests pin JSON. |
| 4 | P2.1 (tool-name consts) | Mechanical; touches ~10 files once. |
| 5 | P1.2 (slash dispatch) | Largest; do after the above stabilize. P2.4/P2.7 ride along. |
| 6 | P2.2–P2.6 | Independent medium cleanups. |
| 7 | P3 consts | Mechanical sweep, no behavioral change. |

---

*Report generated by architecture-review skill over `internal/cli` (advisory; no code
mutated). All file:line references verified at HEAD.*
