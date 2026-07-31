# P3 — `internal/cli` consts and small logic cleanups

**Status:** Source-verified plan; mechanical Fast-Path sweep; ADLC Fast-Path eligible (see §4); no behavioral change; owner approval optional before code
**Date:** 2026-07-28
**SoT:** `.mivia/plans/cli-internal-refactor/p3-consts-and-cleanups.md`
**Target:** Eliminate the Priority-3 magic-number/string sprawl and a handful of dead-branch / no-op logic cases in `internal/cli/*.go`, with zero observable behavior change.

## 0. Source & method

- Origin: `.mivia/reports/cli-internal-refactoring-review.md` → **Priority 3 — Low-value cleanup**, two subsections: *Extract to const (magic numbers/strings)* and *Small logic cleanups*.
- All `file:line` references re-verified at HEAD via `grep`/`read_file` before this plan was written (see §7 verification log).
- This is an **advisory refactoring** of the shipped package, not a gate review of a specific change. The structure gate (`scripts/check_go_structure.py --strict --all internal/cli`) passes today and must still pass after.
- Discipline: **constants and code deletions only.** No new types, no new public API, no new files beyond the two tiny leaf modules (`glyphs.go`, and const additions to existing leaf files). No function signature changes.

## 1. Objective

Replace scattered magic numbers/strings with named constants following the package's existing good pattern (`maxToolResultPreview = 200` in `renderer.go:103`, `maxThinkingLines = 6`), and collapse four small no-op / dead-branch logic cases. One sentence: *make the literals self-documenting and remove the dead code, without touching behavior.*

## 2. Current ground truth (verified)

### 2.1 Magic numbers/strings — confirmed call sites

| Theme | Literal | Sites (verified) | Existing good pattern to mirror |
| --- | --- | --- | --- |
| Terminal floor | `80, 24` | `chat.go:65` | `defaultTermWidth/defaultTermHeight` |
| Card / pane floor | `width < 20`, `return 8` | `composer.go:15,26`, `chatblock_render.go:22`, `chatblock_workgroup.go:117`, `toolpanel.go:128`, `messagebubble.go:93` | `minCardWidth = 20`, `minPaneContentWidth = 8` |
| Status glyphs | `✓ ✗ ◆ ◇ ▸ ▾` | `toolui.go`, `toolpanel.go:189`, `chatblock_render.go`, `brand.go`, `msgcard.go` | new leaf `glyphs.go` |
| Preview/cap widths | `48` (fence bar, incl. no-op `min(48,48)` at `highlight_blocks.go:151`), `56` (rule width), `+2`/`+3` table padding, `peekLines=6` (`diff_render.go:55`), `maxExpandedLines=50` | as listed | `maxToolResultPreview=200` (`renderer.go:103`), `maxThinkingLines=6` |
| Composer placeholder | `"Message mivia… Enter send · Alt+Enter newline · /help"` | `tui.go:186`, `tui_keys.go:27,457,464`, `tui_message.go:269` | const |
| REPL prompt glyph | `" " + modelShort + " > "` | `chat_repl_loop.go:31,74,232` | helper `replPromptGlyph(modelShort)` |
| `MaxTokens` | `4096` | `dispatcher.go:192,238` | `defaultMaxTokens = 4096` |
| Retention | `10 * time.Minute` | `orchestration_state.go:114,180`, `resume.go:72` (resume hardcodes instead of calling `orchestrationHandleRetention(cfg)`) | route resume through `orchestrationHandleRetention` |
| Join wait | `3 * time.Hour` | `orchestrate_lifecycle.go:137` | `defaultJoinRunTimeout = 3 * time.Hour` |
| Poll interval | `25ms` | `orchestrate.go:446` | `orchestrationPollInterval = 25 * time.Millisecond` |
| Owner | `"mivia"` | `delegate.go:112`, `dispatch.go:274`, `orchestrate_spawn_tasks.go:49` | `defaultToolOwner = "mivia"` |
| Env vars | `MIVIA_CLIPBOARD_TTY`, `MIVIA_NO_MOTION`, `MIVIA_MOUSE`, `MIVIA_CONFIG` | raw strings across package | `const envClipboardTTY = "MIVIA_CLIPBOARD_TTY"` etc. |

### 2.2 Small logic cleanups — confirmed

- **`appendMsg` dead tail** (`tui_layout.go:223`): body is `m.appendBlock(...)`; then `if len(m.messages) == 0 { return }`; then a final bare `return`. The trailing `return` and the guard are both no-ops (a func with no return values returns unconditionally). The `if len==0 {return}` guard does nothing the implicit return wouldn't.
- **`onAssistant` dead branch** (`classic_agent_ui.go:133–147`): the `if !already { ui.interimPrinted = true } else { ui.interimPrinted = true }` sets the same value in both arms. The *meaningful* predicate is `already` (used after unlock to decide whether to print). Collapse to a single unconditional `ui.interimPrinted = true`; keep the `if already { return }` after unlock unchanged.
- **`renderStreamVP` pure alias** (`tui_layout.go:250`): body is `m.renderVP()`. The header comment already documents *why* it is retained (live content moved to `livepanel.go`). Decision: **keep as a documented alias** — callers read more clearly with the streamVP name, and inlining would churn ~call sites for no gain. No code change beyond optionally tightening the comment.
- **`repositoriesMatch` uses `reflect`** (`orchestration_state.go:99`): currently `reflect.TypeOf` + `Value.Interface()` compare. If `ledger.LedgerRepository` instances are always pointer-typed at the call sites, this collapses to `==`. **Caveat:** this is the one item that is NOT provably Fast-Path — `LedgerRepository` is an interface and call sites may pass distinct concrete types (e.g. memory repo vs storage repo) that are intentionally equal-by-pointer. Do **not** blindly replace with `==`; verify concrete types first (see §4 risk note + §7 verification).

## 3. Proposed changes

Grouped by theme. Each group is a self-contained Fast-Path wave (mechanical, single concern, no behavioral change).

### 3.1 Wave A — New leaf `glyphs.go` (TUI render slice)

**File to create:** `internal/cli/glyphs.go`

```go
package cli

// Glyphs centralize the single-character status markers used across the TUI
// render surface (toolui, toolpanel, chatblock_render, brand, msgcard).
const (
	glyphCheck   = "✓"
	glyphCross   = "✗"
	glyphDiamond = "◆"
	glyphLozenge = "◇"
	glyphTriR    = "▸" // right-pointing triangle (collapsed)
	glyphTriD    = "▾" // down-pointing triangle (expanded)
)
```

**Files to modify (literal → const):** `toolui.go`, `toolpanel.go:189`, `chatblock_render.go`, `brand.go`, `msgcard.go`.

Acceptance: `go build ./internal/cli` clean; glyphs render byte-identically (no Unicode normalization drift — keep the exact codepoints).

### 3.2 Wave B — Terminal / card floor consts (TUI slice)

Add to an existing TUI leaf (e.g. alongside `composer.go` or a `tui_dimensions.go` const block — do not create a new file unless the leaf is full):

```go
const (
	defaultTermWidth  = 80
	defaultTermHeight = 24
	minCardWidth      = 20
	minPaneContentWidth = 8 // the `return 8` pane-content floor
)
```

**Files to modify:** `chat.go:65`, `composer.go:15,26`, `chatblock_render.go:22`, `chatblock_workgroup.go:117`, `toolpanel.go:128`, `messagebubble.go:93`.

### 3.3 Wave C — Preview/cap width consts (render slice)

Mirror `maxToolResultPreview=200` / `maxThinkingLines=6`:

```go
const (
	fenceBarWidth       = 48   // markdown.go fence bar
	ruleWidth           = 56
	tablePadCols        = 2    // the +2 table padding
	tablePadRows        = 3    // the +3 table padding
	peekLines           = 6    // diff_render.go:55
	maxExpandedLines    = 50
)
```

**Files to modify:** `markdown.go:297,307`, `highlight_blocks.go:151` (delete the no-op `min(48,48)` → `fenceBarWidth`), `diff_render.go:55`, plus the `+2`/`+3` table-padding and `maxExpandedLines` sites.

### 3.4 Wave D — UI string consts (chat/REPL slice)

```go
const composerPlaceholder = "Message mivia… Enter send · Alt+Enter newline · /help"

func replPromptGlyph(modelShort string) string { return " " + modelShort + " > " }
```

**Files to modify:** `tui.go:186`, `tui_keys.go:27,457,464`, `tui_message.go:269` (→ `composerPlaceholder`); `chat_repl_loop.go:31,74,232` (→ `replPromptGlyph`).

### 3.5 Wave E — Orchestration consts (orchestration slice)

```go
const (
	defaultMaxTokens         = 4096
	defaultJoinRunTimeout    = 3 * time.Hour
	orchestrationPollInterval = 25 * time.Millisecond
	defaultToolOwner         = "mivia"
)
```

**Files to modify:** `dispatcher.go:192,238` (→ `defaultMaxTokens`); `orchestrate_lifecycle.go:137` (→ `defaultJoinRunTimeout`); `orchestrate.go:446` (→ `orchestrationPollInterval`); `delegate.go:112`, `dispatch.go:274`, `orchestrate_spawn_tasks.go:49` (→ `defaultToolOwner`).

**Retention (special):** `orchestration_state.go:114,180` already define the `10 * time.Minute` fallback *inside* `orchestrationHandleRetention(cfg)` — promote that literal to `defaultHandleRetention = 10 * time.Minute` (one named const, used in both arms). For `resume.go:72`, **replace the hardcoded `10 * time.Minute` with a call to `orchestrationHandleRetention(cfg)`** so the resume path honors the configured retention instead of a private copy. *If `cfg` is not in scope at `resume.go:72`, add it as a parameter rather than reintroducing the literal.*

### 3.6 Wave F — Env-var consts (misc slice)

```go
const (
	envClipboardTTY = "MIVIA_CLIPBOARD_TTY"
	envNoMotion     = "MIVIA_NO_MOTION"
	envMouse        = "MIVIA_MOUSE"
	envConfig       = "MIVIA_CONFIG"
)
```

**Files to modify:** every `os.Getenv("MIVIA_…")` site. (Locate via `grep -n 'MIVIA_CLIPBOARD_TTY\|MIVIA_NO_MOTION\|MIVIA_MOUSE\|MIVIA_CONFIG'` during implementation.)

### 3.7 Wave G — Small logic cleanups

| Case | File:line | Change | Risk |
| --- | --- | --- | --- |
| `appendMsg` dead tail | `tui_layout.go:223` | Delete the `if len(m.messages)==0 { return }` guard **and** the trailing bare `return`. Keep `m.appendBlock(...)`. | None — both are no-ops for a void func. |
| `onAssistant` dead branch | `classic_agent_ui.go:133–147` | Replace `if !already { ui.interimPrinted = true } else { ui.interimPrinted = true }` with `ui.interimPrinted = true`. Keep the post-unlock `if already { return }`. | None — both arms were identical; `already` is still computed and used. |
| `renderStreamVP` alias | `tui_layout.go:250` | **Keep as documented alias** (no code change). Optionally tighten the comment. | None. |
| `repositoriesMatch` reflect | `orchestration_state.go:99` | **Conditional.** Only if it is proven that all call sites pass pointer-typed concrete repos. Replace the `reflect.TypeOf`/`Interface()` body with `==` (after the two `effectiveOrchestrationRepo` normalizations). | **Medium** — see §4. If any call site passes a non-comparable or distinct concrete type, keep `reflect` and add a comment documenting *why*. Default disposition if uncertain: **defer / leave with a clarifying comment.** |

## 4. ADLC path & risk

- **ADLC path:** All of Waves A–F and the first three rows of Wave G are **Fast-Path** (mechanical literal→const substitution and dead-code deletion; no new types, no API change, ≤5 lines per file per concern). Per `05-adlc-agentic-development-lifecycle.md`, Fast-Path skips Steps 0–3, implements directly in Step 4, runs **1** hostile auditor in Step 5 (not 3–4), and commits normally in Step 6.
- **The one non-Fast-Path item is `repositoriesMatch`.** `ledger.LedgerRepository` is an interface; replacing `reflect`-based comparison with `==` is only safe if (a) every concrete repo passed is pointer-typed and (b) no call site intends value-equality across distinct concrete types. This item must NOT be done blindly — it requires reading the concrete types at every `repositoriesMatch` call site and the `LedgerRepository` implementations in `internal/ledger`. If unverified, **defer it** with a clarifying comment and ship the rest.
- **Scope guard:** This plan is constants + dead-code only. It must not be expanded into P1.1 (theme.go), P2.1 (tool-name consts), or P2.2 (diff unify) — those are separate, larger refactors listed in the report. The `glyphs.go` here is deliberately tiny (7 single-char consts) and is **not** a theme/style module.

## 5. Tests and verification

These changes are behavior-preserving; existing tests are the guardrail. No new test file is required for pure const substitution (the literals are not behavior), but each wave should be verified:

```text
go build ./internal/cli
go vet ./internal/cli
go test -race ./internal/cli/... -count=1
make structure-check
```

Targeted regression focus per wave (run the relevant `-run` subset):

| Wave | Test focus |
| --- | --- |
| A (glyphs) | TUI render snapshot/string tests for toolui/toolpanel/chatblock/msgcard |
| B (floor consts) | composer / chatblock / toolpanel / messagebubble layout tests |
| C (cap widths) | markdown fence, diff_render peek, highlight_blocks |
| D (UI strings) | composer placeholder, REPL prompt formatting |
| E (orchestration) | dispatcher MaxTokens, `orchestrationHandleRetention`, `resume.go` retention path |
| F (env vars) | any test that sets `MIVIA_*` env |
| G (cleanups) | `appendMsg`, `onAssistant` interim-print behavior, `repositoriesMatch` callers |

**`resume.go:72` retention change** is the only line with a *behavioral* consequence (it starts honoring configured retention). If a test asserts the old fixed `10 * time.Minute` on the resume path, update the test to assert `orchestrationHandleRetention(cfg)` and document the behavior change in the commit body.

Verification order:

```text
go test ./internal/cli -count=1
go build ./... && go vet ./...
go test ./... -count=1
make structure-check
make verify
```

## 6. Suggested execution order

| Wave | Items | Why this order |
| --- | --- | --- |
| 1 | Wave G rows 1–3 (`appendMsg`, `onAssistant`, `renderStreamVP`) | Pure deletions/no-ops; shrink surface first; zero risk. |
| 2 | Waves A, B, C | Render/TUI const leaves; independent of orchestration. |
| 3 | Waves D, F | String + env-var consts; touch chat/REPL + misc. |
| 4 | Wave E (orchestration consts) | Includes the one behavioral change (resume retention). |
| 5 | Wave G row 4 (`repositoriesMatch`) — **only if verified pointer-typed** | Highest-risk item; do last and alone so it can be reverted independently. |

Each wave is independently committable and revertible.

## 7. Verification log (pre-plan source checks)

Re-confirmed at HEAD before writing this plan:
- `tui_layout.go:218–232` — `appendMsg` body confirmed: `appendBlock` + `if len==0 {return}` + bare `return` (both no-ops).
- `tui_layout.go:245–256` — `renderStreamVP` confirmed pure alias of `renderVP`, with explanatory comment.
- `classic_agent_ui.go:133–147` — `onAssistant` confirmed: `if !already { interimPrinted=true } else { interimPrinted=true }`, both arms identical; `already` recomputed and used post-unlock.
- `orchestration_state.go:99–105` — `repositoriesMatch` confirmed uses `reflect.TypeOf` + `reflect.ValueOf(...).Interface()` after `effectiveOrchestrationRepo` normalization on both args.
- `orchestration_state.go:178–180` — confirmed `10 * time.Minute` fallback lives inside `orchestrationHandleRetention(cfg)`.
- `resume.go:72` — confirmed `retention: 10 * time.Minute` hardcoded, does not call `orchestrationHandleRetention`.
- `dispatcher.go:192,238` — confirmed `MaxTokens: 4096` ×2.
- `delegate.go:112`, `dispatch.go:274`, `orchestrate_spawn_tasks.go:49` — confirmed `Owner: "mivia"` ×3.

Outstanding (must verify during implementation, not before): exact concrete types at every `repositoriesMatch` call site and every `LedgerRepository` implementation, to decide the `reflect`→`==` question.

## 8. Non-goals / deferred work

- Not P1.1 (theme.go / ANSI consolidation) — separate larger refactor.
- Not P2.1 (tool/handler-name string consts) — separate refactor.
- Not P2.2 (diff-line coloring unify) — separate refactor.
- Not P1.5 (dead-code deletion sweep) — separate refactor; only the two trivial dead-branch cases in scope here.
- No new public API, no signature changes, no new dependencies.
- `repositoriesMatch` simplification deferred if concrete-type verification fails.

## 9. Rollback criterion

The plan is killed (or an individual wave reverted) if:
- Any wave causes a behavioral regression that is not the single intended `resume.go` retention change.
- `make structure-check` regresses (file/function size limits).
- The `repositoriesMatch` change breaks any orchestration test that exercises cross-repo handle lookups — revert immediately and defer with a comment.
