# P1.1 — Create a `theme.go`; consolidate color/style ownership

**Status:** DESIGN-READY — implementation must pass ADLC Step 0 (plan challenge) before
any code is written. This is a **REFACTOR (TDD-preserving)**: every wave must keep the
existing test suite green; new tests are added only where behavior is genuinely new
(theme-as-source-of-truth assertions). The structure gate
(`scripts/check_go_structure.py --strict --all internal/cli`) passes today and must
still pass after each wave.
**Date:** 2026-07-31
**Depends on:** nothing. (Recommended, not required: run **P1.5 dead-code delete** first —
it removes `formatModelHeader`/`formatModelFooter`, `tuiUserCardBg`, and friends, which
shrinks the surface this plan touches. This plan is self-contained if P1.5 has not run.)
**Blocks:** **P2.2** (unify diff-line coloring — it consumes the theme tokens this plan
introduces), and the **P3 magic-number sweep** that promotes raw color indices to named
consts (it names those colors). Do P1.1 first.
**Blast radius:** MEDIUM — mechanical and isolated, but it touches ~12 files that all
render to the terminal, so a wrong value is a silent visual regression rather than a
compile error. No behavioral change to any user-facing flow is intended; rendered bytes
must be byte-identical except where the plan *deliberately* reconciles one documented
inconsistency (see §2.4).

---

## 1. Problem

There is one color palette and three representations of it in `internal/cli`. From the
refactoring review (`.mivia/reports/cli-internal-refactoring-review.md`, finding P1.1):

1. **~45 raw `lipgloss.Color("8")`/`("9")`/`("14")`/`("236")` … literals across 12 files.**
   Verified: `tui.go:26-34,40,191`, `toolui.go:210-230`, `tui_run_dashboard.go:21-26`,
   `msgcard.go:46-47`, `fleetbox.go:50`, `livepanel.go:131`, `messagebubble.go:151`,
   `welcome.go:164`, `bubble_leftrail.go` (via `chrome*`). A typo (`"9"` vs `"160"` for
   "error") is already shipping (see §2.4).

2. **Two parallel, byte-identical ANSI-constant vocabularies** for the same SGR codes:
   - `ansi*` block — `markdown.go:16-31` (`ansiBold="\033[1m"`, `ansiCyan="\033[36m"`,
     `ansiBgDark="\033[48;5;236m"`, `ansiReset="\033[0m"`, …).
   - `hl*` block — `highlight.go:11-26` (`hlCyan="\033[36m"`, `hlBgDark="\033[48;5;236m"`,
     `hlReset="\033[0m"`, …). Its own header comment even admits
     *"highlightAnsi codes reused from markdown.go constants"*.
   These are a perfect 1:1: every `ansiX` has a value-identical `hlX`. They diverge only
   in *prefix*.

3. **Duplicate semantic style vars** built from the same raw colors:
   - `tuiUserLabel` (`tui.go:28`), `userLabelStyle` (`msgcard.go:47`), `userRailStyle`
     (`msgcard.go:46`) — three identical `Foreground(color 12).Bold(true)`.
   - `tuiDimStyle` (`tui.go:29`) ≡ `toolDimStyle` (`toolui.go:214`) — both color 8.
   - `tuiErrorStyle` (`tui.go:30`) ≡ `toolErrStyle` (`toolui.go:212`) — both color 9.

4. **Dead code:** `tuiUserCardBg` (`tui.go:27`) is declared and **never referenced**
   (verified: zero hits across the package). It is a leftover copy of the live
   `_userBgStyle` (`messagebubble.go:151`).

5. **Inconsistency already present:** the brand's error color is
   `chromeError = brandColorError = "160"` (`brand.go:44`, surfaced via
   `bubble_leftrail.go:17`), but the inline TUI error styles use color index `9`
   (`tuiErrorStyle`, `toolErrStyle`). Two different "red"s for the same semantic role.

### What is NOT in scope
- Diff-line coloring unification (`highlightDiffLine` / `formatCodeLine` /
  `colorDiffLine` / `diff_render.go`) — that is **P2.2**. This plan only *names* the diff
  colors (`toolDiffAdd`/`toolDiffDel`/…) as theme tokens; it does not merge the four
  classify-by-prefix implementations.
- Status-glyph / terminal-floor magic-number promotion — that is the **P3 sweep** and it
  rides on this plan's names.

## 2. Goals and non-goals

### Goals
- One module owns the semantic color palette + the consolidated `*Style` vars. Either
  extend `brand.go` or add `theme.go` (decision in §3.2).
- Kill exactly one of the two ANSI vocabularies; the surviving one lives in the theme
  module and the other file imports it. **No code path may keep a private copy.**
- Replace every raw `lipgloss.Color("<index>")` for an in-scope semantic role with a
  named theme const/style. (Out-of-scope indices that genuinely have no semantic owner —
  e.g. `243` waiting gray, `237` selection bg, `88`/`22` dark diff bg, `11` time yellow
  — are *promoted to named consts in the theme module* but **not** semantics-renamed;
  see §3.3. This is safe, mechanical, and stops the spread.)
- Collapse the three duplicate style families to single shared definitions.
- Delete `tuiUserCardBg`.
- Reconcile the `160` vs `9` error-color inconsistency (decision in §3.4).

### Non-goals
- Do not change any rendered output that is *not* the one documented error-color
  reconciliation (§2.4 / §3.4). Byte-identical rendering is the safety contract.
- Do not merge diff-line coloring implementations (P2.2).
- Do not introduce a runtime theme-switch / light-mode / palette-config mechanism. The
  palette is compile-time consts, as today.
- Do not rename any exported package symbol whose name is part of a public contract
  (there are none here — all affected vars are unexported).
- Do not touch the brand *phase* color ramp in `brand.go` (`brandColorThinking`, etc.);
  those are the deliberate stop-2 of the state-logo shade ramp and already centralized.
  They become inputs *to* the theme, not outputs of it.

## 3. Architecture / Approach

### 3.1 Ownership direction
The theme module is a leaf dependency of the render/TUI files: `brand.go`,
`bubble_leftrail.go`, `tui.go`, `toolui.go`, `markdown.go`, `highlight.go`, etc. all
*import* theme tokens. The theme module imports nothing from the render slice. No new
import cycle (today `highlight.go` already could-not-but-doesn't import `markdown.go`;
this plan makes that dependency explicit and shared rather than duplicated).

### 3.2 Where the module lives — DECIDED: new `theme.go`, with `brand.go` as the phase-color source
Extend `brand.go` vs new `theme.go` were both viable. Decision: **new `internal/cli/theme.go`**.
Rationale:
- `brand.go` is already coherent as "the brand mark + phase-color ramp + status-line
  chrome renderer" (`brandPhase`, `brandColor`, `renderWorkChrome`, `renderStatusBar`,
  `sanitizeStatusDetail`, `countTools`…). Folding a generic semantic-palette + ANSI-SGR
  layer into it blurs that file's purpose and balloons it past a clean responsibility.
- A dedicated `theme.go` gives the P2.2 / P3 consumers a single, narrow import target.
- `theme.go` references the *already-centralized* phase colors
  (`brandColorError`, `brandColorThinking`, …) rather than redeclaring them — so the
  ramp stays owned by `brand.go` and the semantic layer composes on top of it. This is
  the existing good pattern: `bubble_leftrail.go:13-17` already builds `chrome*` from
  `brandColor*`.

### 3.3 The named surface (exact const set)
`theme.go` declares, under one `// Theme — the single semantic color/style source.`
doc comment:

**256-color indices** (raw-string source of truth — these are the names P3 and P2.2 reach for):
```
themeColorDim     = "8"     // dim/structural text   (was tuiDimStyle/toolDimStyle)
themeColorError   = "9"     // inline error red      (was tuiErrorStyle/toolErrStyle)  — see §3.4
themeColorInfo    = "14"    // cyan accent / info
themeColorUser    = "12"    // user label blue
themeColorWaitGray= "243"   // waiting-state mid-gray
themeColorCardBg  = "236"   // user-card / bar dark bg
themeColorSelBg   = "237"   // tool selection bg
themeColorDiffAdd = "10"
themeColorDiffDel = "9"     // alias role; reconciled with §3.4
themeColorDiffAddBg="22"
themeColorDiffDelBg="88"
themeColorTime    = "11"    // tool-time yellow
themeColorOk      = "2"     // run-dashboard "running"
themeColorStatusFailed = "9"
themeColorStatusDone   = "8"
themeThinkingDim  = "6"     // thinkingDimStyle
```
Phase colors are *not* redeclared — `theme.go` references `brandColorThinking` etc.

**ANSI SGR codes** (the one surviving vocabulary; the `hl*` block is deleted and
`highlight.go` imports these):
```
ansiBold="\033[1m" ansiBoldEnd="\033[22m" ansiItalic="\033[3m" ansiUnderline="\033[4m"
ansiDim="\033[2m"  ansiDimEnd="\033[22m"
ansiYellow="\033[33m" ansiCyan="\033[36m" ansiBlue="\033[34m" ansiGreen="\033[32m"
ansiRed="\033[31m"  ansiMagenta="\033[35m"
ansiBgDark="\033[48;5;236m" ansiBgReset="\033[49m" ansiReset="\033[0m"
```
(These keep the `ansi*` spelling because `markdown_test.go` asserts on `ansiBold`/
`ansiCyan` by name — keeping the name means those tests stay green with zero edits.
`highlight.go` switches from its local `hl*` aliases to these names; the
`highlight_test.go` assertions on `hlCyan`/`hlGreen`/`hlReset` are migrated in the same
wave — see Wave 3.)

**Consolidated styles** (the duplicates collapse onto these):
```
dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDim))    // was tuiDimStyle == toolDimStyle
errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorError))  // was tuiErrorStyle == toolErrStyle
infoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo))   // was tuiInfoStyle
accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo)).Bold(true) // was tuiAccentStyle
waitStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorWaitGray))        // was tuiWaitingStyle
userLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser)).Bold(true) // was tuiUserLabel==userLabelStyle==userRailStyle
userStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser))            // was tuiUserStyle
```
**Compatibility shim:** the old names (`tuiDimStyle`, `toolDimStyle`, `tuiErrorStyle`,
`toolErrStyle`, `tuiInfoStyle`, `tuiAccentStyle`, `tuiWaitingStyle`, `tuiUserLabel`,
`tuiUserStyle`, `userLabelStyle`, `userRailStyle`) become **aliases** =
`(the consolidated var)` declared in `theme.go`. This lets Wave 4–5 (the ~45 raw-literal
sweeps across 12 files) land file-by-file without a big-bang rename, and each alias
removal becomes its own safe micro-task. Aliases are marked `// TODO(P1.1): collapse —
duplicate of <x>`.

### 3.4 Error color reconciliation — DECIDED: keep `9` for inline TUI error
The inconsistency is `chromeError="160"` (brand phase-error, vivid red, used in the
status-bar diamond / left-rail strict-failure) vs inline `tuiErrorStyle`/`toolErrStyle`
= `9` (standard bright red, used for inline tool-result `✗`, slash-command errors,
model-dialog notices).

Decision: **leave the two roles distinct** — they are *semantically different*, not a
bug:
- `160` = the **brand/status** error (the diamond goes red; high-saturation to read as
  one palette with the phase ramp).
- `9` = **inline content** error glyphs and notices (standard bright red, reads well on
  the card backgrounds and next to `✗`/`●` glyphs).

This plan's job is to make that distinction *explicit and named* rather than accidental:
`themeColorError="9"` (inline) vs the existing `brandColorError="160"` (brand), with a
comment cross-referencing them. **No rendered byte changes.** If a future plan decides
they should unify, that becomes a one-line edit at a single named site — which is the
whole point of this refactor. (The review's "reconcile" is satisfied by *naming* both
sides, not by collapsing them.)

## 4. Implementation waves

Every production task is preceded by a compiling RED test that fails an assertion, per
ADLC. For a pure-rename/alias refactor the "new behavior" is narrow, so most waves are
**GREEN-preserving**: the proof is `go build ./... && go test ./internal/cli -count=1`
staying green, *plus* a focused byte-stability test (§5) guarding that rendered output
is unchanged.

| Wave | Scope (1 file/task) | RED | GREEN gate |
|---|---|---|---|
| **1** | Create `theme.go`: declare the color-index consts (§3.3), the ANSI SGR block (§3.3), and the consolidated `*Style` vars + the backward-compat **aliases**. No callers changed yet. | New `theme_test.go`: assert the canonical values exist and the aliases are value-identical to their targets (e.g. `render(tuiDimStyle.Render("x")) == render(dimStyle.Render("x"))`). Also assert `themeColorError=="9"` and `brandColorError=="160"` coexist (pins §3.4). | `go test ./internal/cli -run TestTheme -count=1` green; full package still green. |
| **2** | Delete `tuiUserCardBg` (`tui.go:27`). | (Behavior is dead-code removal; covered by `go vet`/compile + the byte-stability suite. No new assertion needed — there is no new behavior.) | `go build ./internal/cli`; package tests green; `grep -rn tuiUserCardBg internal/cli` returns nothing. |
| **3** | Consolidate ANSI vocab: delete the `hl*` block (`highlight.go:11-26`); make `highlight.go` use the `ansi*` names from `theme.go`. Migrate `highlight_test.go`'s `hlCyan`/`hlGreen`/`hlReset` assertions to the `ansi*` names (value-identical, so assertions still hold). | `highlight_test.go` already pins the *rendered output* contains the SGR bytes; keep those assertions, only swap the symbolic name referenced. RED = test referencing `ansiCyan` before the rename compiles-but-fails until `highlight.go` emits `ansi*`. | `go test ./internal/cli -run 'TestHighlight' -count=1` green; byte-stability suite green (highlight output unchanged). |
| **4** | Collapse the three duplicate style families onto the aliases in `theme.go`: edit `toolui.go:212,214` (`toolErrStyle`/`toolDimStyle` → aliases, delete local decls), `msgcard.go:46-47` (`userRailStyle`/`userLabelStyle` → aliases), `tui.go:28` (`tuiUserLabel` → alias). Because Wave 1 already declared them as aliases, this is deleting duplicate *definitions* and is byte-neutral. | byte-stability suite (§5) must stay green — it proves the rename didn't change output. | full package green; each formerly-duplicate var now has exactly one definition (verify with `grep`). |
| **5** | Raw-literal sweep — file by file (≤1 file/task): replace `lipgloss.Color("<idx>")` with `lipgloss.Color(themeColor<Name>)` in `tui.go` (26-34,40,191), `toolui.go` (210-230), `tui_run_dashboard.go` (21-26), `fleetbox.go:50`, `livepanel.go:131`, `messagebubble.go:151`, `welcome.go:164`, `bubble_leftrail.go` (`chrome*` already reference `brandColor*` — verify only). | byte-stability suite green per file. | per-file `go test ./internal/cli -count=1` green; final `grep -rn 'lipgloss.Color("[0-9]' internal/cli` returns only deliberately-unswept indices (none expected after §3.3 promotion). |
| **6** | Review wave: a reviewer task reads `theme.go` + a sample of callers and confirms (a) no file retains a private color/SGR copy, (b) aliases are all marked TODO(P1.1), (c) `brand.go` phase ramp untouched. Then hostile audit (ADLC Step 5). | — | audit reports zero confirmed bugs; `make verify`/`make invariants` green. |

**Wave ordering rationale:** Wave 1 is additive (zero risk, unblocks all later waves).
Wave 2 is a pure delete (smallest possible change, do early). Wave 3 kills one ANSI
vocabulary and is gated by its own tests. Waves 4–5 are the mechanical sweeps that depend
on the aliases from Wave 1, sequenced one-file-per-task so each is independently
revertible. Wave 6 is the ADLC review+audit gate.

## 5. Verification

Minimum gates (run after every wave, and as the final Step 6):

```text
go build ./...
go vet ./...
go test ./internal/cli -count=1
go test -race ./internal/cli -count=1
scripts/check_go_structure.py --strict --all internal/cli
make invariants
make verify
```

**Byte-stability suite (the load-bearing safety contract):** add/extend tests that
snapshot the rendered bytes of representative surfaces and assert they are unchanged by
this refactor. Concretely, assert the ANSI-byte output of:
- `MarkdownWriter` rendering a sample markdown doc (covers `ansi*`) — extend
  `markdown_test.go`'s existing contains-assertions into a byte-snapshot of one fixture.
- `highlightLine` for a Go + a diff sample (covers the `ansi*` rename in `highlight.go`).
- one tool row, one chatblock tool line, one slash-error line (covers `tool*Style`/`tui*Style` collapse).

Because the refactor is byte-neutral by construction (aliases = same value), these
snapshots must not change; if one does, the wave has accidentally altered rendering and
must be reverted (ADLC rollback rule: "Step 5 fix breaks existing tests → halt, revert").

**Mutation guards** (each must flip a test red):

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Change `themeColorError` from `"9"` | byte-stability suite (inline error glyph) + `theme_test.go` value pin |
| M2 | Make a "collapsed" alias diverge from its target (e.g. re-add a local `toolDimStyle` with a different color) | `theme_test.go` alias-equality assertion |
| M3 | Re-introduce the `hl*` block in `highlight.go` | review wave + `grep` check in Wave 3 gate |

## 6. Rollback

Every wave is independently revertible because Wave 1 is purely additive and Waves 2–5
are file-scoped. Rollback granularity = one wave (one `git revert` of the wave's commit).

- If the byte-stability suite (§5) goes red in any wave: **halt, revert that wave,
  re-analyze** (ADLC Step 5 rule). The whole point of the alias shim is that a botched
  collapse cannot escape its own file.
- If the error-color reconciliation (§3.4) is later judged wrong: it is a one-line change
  at `themeColorError`; no rollback of the refactor is needed.
- If the structure gate (`check_go_structure.py --strict`) regresses: revert the
  introducing wave — `theme.go` must remain a single cohesive leaf module, not a grab-bag.

The end state is recoverable to the pre-refactor tree at any point: no data migration, no
config change, no exported-API break. The only artifacts are a new `theme.go` +
`theme_test.go` and edits to ~12 render files, all of which compile and test green at
every intermediate commit.
