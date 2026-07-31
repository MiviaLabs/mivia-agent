# Composer Autocomplete — `/` suggestion popup + skills as slash commands

**Status:** v5. Phase 0 spike required before any UI wiring.
**Date:** 2026-07-31
**SoT:** `.mivia/plans/composer-autocomplete.md`
**Revisions:** v1 product → v2 industry → v3 implementation challenge → **v5 (v4 draft + 4 hostile reviews; 2 BLOCK verdicts folded in)**

**Product goal:** typing `/` in the TUI composer opens a filterable list above the input. ↑↓ navigate, Tab/Enter accept, Esc dismisses. Every `.mivia/skills/*` skill is a `/` command — `/bug` suggests `/bug-audit`.

---

## 0. What changed from v3, and why

v3 was built on five premises that are false against HEAD. Each one changed the implementation.

### 0.1 FALSE — "the viewport also consumes ↑↓, so suggest keys must fully early-return from `updateMessageImpl`"

`tui_keys.go:309`:
```go
return skipTextarea, skipViewport || focus != focusScrollback, cmds
```
The viewport gate is `focus != focusScrollback`, **independent of `skipTextarea`**. With the composer focused, `routeFocusKey` (`tui_focus.go:49-50`) returns `consumed=false` for `up`/`down`, so `skipTextarea=false`, `skipViewport=true`. The textarea gets the key; the viewport never does. Pinned by `tui_composer_keys_test.go:48` and `:68`.

The real competitor for ↑↓ is the **textarea** (`CursorUp`/`CursorDown`, bubbles `textarea.go:1039,1046`).

**Consequence:** v3's restructuring of `updateMessageImpl` is unnecessary. Intercept inside `handleChatKey` before `routeFocusKey` (`tui_keys.go:300`), returning `(true, true, nil)`. Nil cmds means the `len(c)>0` early return at `tui_message.go:96` does not fire, so the tail still runs.

### 0.2 FALSE — "local slash Enter does not Reset the buffer"

`tui_keys.go:186-192` calls `m.textarea.Reset()` when `handleSlash` returns true. v3's "naive bug #4" does not exist.

The **real** hazard v3 missed: when `handleSlash` returns false (`tui_slash_handlers.go:175-176`), the TUI silently sends the raw slash text to the model as a prompt (`tui_keys.go:207`) — unlike the REPL, which prints `unknown command`. Autocomplete makes this worse, because a menu teaches users command names they will then mistype. **Fixed in Phase 1, not deferred.**

### 0.3 FALSE — "no public cursor-column getter; use SetValue + KeyLeft×N"

`LineInfo().StartColumn + LineInfo().ColumnOffset == m.col` exactly (bubbles `textarea.go:823-844`). `SetCursor` (`:557`) and `InsertString` (`:352`) are exported. v3's synthetic-keystroke loop is unnecessary — and re-runs the sanitizer N times.

### 0.4 MISSING — there are **two** viewport-height computations

`chatViewLayout` (`tui_view.go:103-157`, floor `minVp=2`, `max(8, m.height)`) and `layout()` (`tui_layout.go:12-38`, floor 5 then `max(3, avail)`, raw `m.height`). `tui_view.go:96-101` carries a source comment warning both must subtract the same rows or the frame clips. There is also a bottom-truncating clamp at `tui_view.go:83-87`. v3 named only one and would have shipped a clipping bug.

### 0.5 STALE — ground truth

| v3 | Actual |
|---|---|
| `tui.go` ~454 | **478** (cap 600, `structure_test.go:14`) |
| `tui_keys.go` ~315 | **456** |
| `tui_message.go` ~250 | **304** |
| `tui_view.go` ~282 | **327** |
| `handleChatKey` ~77, "at soft 80" | **66** |
| early return `tui_message.go:76-78` | **`:96-98`** |
| "three divergent lists" | **four** + two `/search` intercept sites |

v3's catalog omitted `/new`, `/sessions`, `/select`, `/resume` (all real, `tui_slash_handlers.go:33,47,162,173`) and the REPL-only `/exit`, `/quit`, `/provider`, `/workspace` (`chat.go:151`, `chat_slash.go:20,43`).

**Kept from v3:** catalog as SoT · Tab never executes · replace the full token span · no mid-line `/` · phase-split `@` behind `wsRoot`.

---

## 1. Industry evidence

| Tool | Accept keys | Enter | Skills on `/`? |
|---|---|---|---|
| **Claude Code** | Tab accept, Esc dismiss, ↑↓ nav — [keybindings](https://code.claude.com/docs/en/keybindings) `Autocomplete` context | Enter is `chat:submit` in the **Chat** context, *not* an autocomplete action; it submits the completed buffer ([#25477](https://github.com/anthropics/claude-code/issues/25477)) | **Yes.** Dir name → `/skill-name`; `argument-hint` shown in the menu; **`user-invocable: false`** hides a skill ([skills](https://code.claude.com/docs/en/skills)) |
| **OpenCode** | `tab`=complete, `return`=select, ↑↓/`ctrl+p,n`, `esc` (`packages/tui/src/config/keybind.ts:214-218`) | two distinct actions; exact handler semantics unverified | **Excluded** — model calls a `skill` tool instead |
| **Crush** | `enter`/`tab`/`ctrl+y`; `/` opens a modal palette | palette-driven | **Yes, opt-in** via `UserInvocable` (`internal/commands/commands.go:64-69`) |
| **Gemini CLI** | `Tab`+`Enter` accept | per-command `autoExecute` bool | n/a |

**The decisive finding — [gemini-cli PR #13985](https://github.com/google-gemini/gemini-cli/pull/13985), verbatim:** *"Tab key behavior preserved: Tab always auto-completes, never auto-executes."* They first inferred executability from command shape; it broke `/chat share`; they replaced it with one explicit boolean. [PR #20136](https://github.com/google-gemini/gemini-cli/pull/20136) then fixed over-eager subcommand descent where a trailing space re-opened the menu and Enter ran the wrong thing.

This kills v3's three flags (`ArgsRequired` + `EmptyExecutes` + `PreferInsert`), which v3 itself collapsed to `EmptyExecutes && !PreferInsert && !ArgsRequired` — one boolean with three ways to typo it. **One flag: `AutoExecute`.**

**All three tools gate which skills reach `/`** (CC opt-out, Crush opt-in, OpenCode blanket exclusion). **This plan deliberately does not** — see §4.6. Their catalogs are large; nine skills under prefix narrowing are not.

**Footguns, checked against this codebase:**

| Footgun | Source | Applies? |
|---|---|---|
| Popup steals ↑↓ from prompt-history recall | CC [#11265](https://github.com/anthropics/claude-code/issues/11265), [#56923](https://github.com/anthropics/claude-code/issues/56923) | **No** — verified: this TUI has no composer history recall |
| Highlight not reset on filter change ⇒ Enter runs the wrong thing | CC [#25477](https://github.com/anthropics/claude-code/issues/25477), [#19107](https://github.com/anthropics/claude-code/issues/19107) | **Yes** — A19 |
| Cap applied before ranking ⇒ unreachable items | OpenCode [#17027](https://github.com/anomalyco/opencode/issues/17027) | **Yes** — and we reintroduce it at empty `/` unless fixed (§4.4) |
| List doesn't scroll past the window | OpenCode [#6718](https://github.com/anomalyco/opencode/issues/6718) | **Yes** — A21 |

**Correction to v3 Appendix B:** OpenCode is no longer Go/Bubble Tea — the Go TUI was deleted 2025-11-02 (`f68374ad`), rewritten in TS/Solid on OpenTUI; `sst/opencode` redirects to `anomalyco/opencode`. Crush is now bubbletea v2 + ultraviolet. The bubbletea-v1 references worth copying are the **archived** `opencode-ai/opencode` and Crush ≤ v0.10.0.

---

## 2. How a skill runs — content injection, not direct dispatch

This is the largest change from the v4 draft, which proposed `Dispatcher.Invoke(Kind: Subagent, …)` and was **BLOCKed** by review.

| | Mechanism | Verdict |
|---|---|---|
| A | `Dispatcher.Invoke(Kind: Subagent, Name, Input)` | **Rejected** — see defects below |
| B | Prompt rewrite, hope the model routes to `dispatch_tasks` | **Rejected** — nondeterministic after a menu promised otherwise |
| C | Insert only, no route (status quo) | **Rejected** — falls into §0.2 |
| **D** | **Inject the rendered `SKILL.md` as a real user message; run a normal turn** | **Ship this** |

### Why A was rejected

1. **Silent stale results.** `Dispatcher.Invoke` derives `req.ID` from `sha256(Name+Input)` when `ID` is empty and caches completed results for the dispatcher's life (`runtime/dispatcher.go:211-214,287-289`, cleared only by `Close()`). Running `/bug-audit internal/cli` a second time in one session returns the **first run's cached result** with `Status: "duplicate"`. Every existing caller mints a fresh key to avoid this (`delegate.go:110`, `dispatch.go:254,273`, `subagents.go:209-221`); v4 did not.
2. **Cancels the user's in-flight turn.** `tuiModel.bridge/cancel/waiting` are singular (`tui.go:62-69`) and `startAI` opens with `commitInFlightTurn()`. Slash commands already fire while waiting (`tui_keys.go:186` sits before the `:200` gate). "Reusing startAI's machinery" would silently kill a running conversation turn.
3. **No streaming.** `MultiStepHandler.run` sets no `FinalWriter` (`subagents/multi_step.go:97-111`), unlike `sendAgent` (`chat/session.go:368`). Only tool progress streams; the answer text is a one-shot JSON blob (`{output, steps, elapsed, step_count, status}`, with `output` **deleted** on error, `multi_step.go:151`).
4. **Effectively unbounded.** `registerSkillHandlers` never sets `TotalTimeout` (`dispatcher.go:161-172`); `timeoutContext` only bounds the run if the caller supplies `req.Timeout`.
5. **No landing spot for history.** No `chat.Session` method appends a synthetic exchange outside a `mu`-guarded, `turnID`-fenced turn (`session.go:182-186,395-405`).

### Why D is correct

This is what Claude Code does — [skills docs](https://code.claude.com/docs/en/skills): *"the rendered `SKILL.md` content enters the conversation as a single message and stays there for the rest of the session."*

It dissolves every defect above **and** the open question v4 was blocked on ("does skill output enter history?"). The turn *is* the conversation, so "fix the second one" just works. `startAI` already owns cancel, `m.waiting`, streaming, progress, and tools when `toolsOn`.

The counter-argument for A — `dispatcher.go:147-151`, *"skills like bug-audit need read_file, grep, list_dir, run_command"* — does not hold: a normal `startAI` turn has those tools. What A buys is **context isolation**, which CC models as opt-in per skill (`context: fork`), not the default.

**The one real change D needs.** `startAI(userText)` uses one string for both `SendUserWithEvent` and the displayed `ChatBlockUser.Text` (`tui_start.go:79`), so injecting a 19,813-byte skill body would render a 19KB user block. Split it:

```go
func (m *tuiModel) startAI(userText string) { m.startAIWithDisplay(userText, userText) }
func (m *tuiModel) startAIWithDisplay(sent, display string) // block Text = display; events Detail = display
```
`/bug-audit internal/cli` displays `⚙ /bug-audit internal/cli`, sends `Instructions + "\n\n" + args`. ~8 lines.

The skills registry is then needed for **enumeration and body access only** — no dispatcher plumbing, no worker path, no `startSkill`.

---

## 3. Rendering — decide by spike, do not assume

v4 asserted `PlaceOverlay` (string splicing) over a stacked layout. Review found the mitigation unbuildable as specified, so this is now an explicit **Phase 0 gate**.

**Option 1 — `PlaceOverlay`.** Splice the popup into transcript rows at `y = composerTop - popupHeight`.
- Zero changes to `chatViewLayout`, `layout()`, or the termH clamp. Total height invariant, so the clamp can never eat the composer. Transcript does not reflow. Gives `(x,y,w,h)` free for later mouse work.
- `tui_view.go:23-28` returns early for `m.sessionsDlg`/`m.overlay`, so a call at the tail of `renderChatView` never runs while a modal owns the screen — no conflict.
- **Risk:** the transcript carries raw, non-lipgloss SGR (`highlight.go:14-27,319-322` embeds literal `"\033[36m"`). Splicing must not bleed an unterminated style past the cut column.
- **Mitigation:** build on `charmbracelet/x/ansi` — **already in `go.mod:27` as indirect**, so this promotes an existing dep rather than adding one. Do **not** hand-roll the cut.

**Option 2 — stacked `parts` entry.** Costs edits to **both** height functions (§0.4) and puts the popup inside the bottom-truncating clamp.

**Phase 0 gate (must be green before any UI wiring):** implement `overlay.go` on `x/ansi` and prove, using **this repo's established convention** — `stripANSI` (`bubble_leftrail.go:420`) + substring/position assertions + `lipgloss.Width` accounting, as in `overlay_test.go` — that splicing over (a) an unterminated SGR run crossing the cut column, (b) double-width/CJK runes, (c) a full-width transcript row, preserves visible content and width.

**Do not use byte-exact ANSI goldens.** There is no `testdata/` convention in `internal/cli`, no `TestMain` pinning the color profile, and 79 `t.Parallel()` sites — a global renderer mutation for a golden would be a real data race. If the width assertions cannot be made green, fall back to Option 2 and **budget the two-function height edit**; do not discover this mid-wire.

lipgloss v2 has a real compositor (`NewLayer`/`NewCompositor`, GA 2026-02-24, module moved to `charm.land/lipgloss/v2`) — the eventual replacement, but a whole-repo bubbletea v2 migration. Out of scope.

---

## 4. Locked design

### 4.1 Catalog

```go
type SlashCommand struct {
    Name        string
    Aliases     []string
    Description string   // ≤ 1 line at 80 cols; skills get a short form, see §4.6
    ArgsHint    string   // "<name>", "[n]", "<path or scope>"
    Surface     Surface  // SurfaceTUI | SurfacePlain | SurfaceBoth
    Kind        Kind     // KindBuiltin | KindSkill — drives ordering AND a row glyph
    AutoExecute bool     // Enter runs it. Skills: always false.
}
```

**Complete inventory** (review found v4's list still incomplete):

- `AutoExecute=true`, TUI: `/help` `/h` `/?` `/clear` `/new` `/status` `/sessions` `/list` `/session` `/tools` `/plain` `/select`
- `AutoExecute=false`, TUI: `/model` `/budget` `/steps` `/save` `/load` `/delete` `/resume` `/search`
- `SurfacePlain` only: `/exit` `/quit` `/q` `/provider` `/workspace` — **required**, or rewiring `chat.go:144-169`'s `handleTab` onto the catalog silently regresses their REPL completion
- `KindSkill`: every user-invocable skill, `AutoExecute=false`

`/search` is in **neither** `isLocalSlash` nor `handleSlashImpl` — it is intercepted at `tui_keys.go:177` (chat) and `:439` (welcome). The catalog must carry it (A0g).

Catalog becomes the single source for `isLocalSlash`, `handleTab`, `/help` content, and the popup — collapsing four divergent lists into one.

### 4.2 Keys — and they must be registered

| Key (popup open, items > 0, **`m.focus == focusComposer`**) | Action | Return |
|---|---|---|
| `up` / `ctrl+p` | `Selected--` (wrap) | `(true, true, nil)` |
| `down` / `ctrl+n` | `Selected++` (wrap) | `(true, true, nil)` |
| `tab` | **accept: insert + trailing space + close** | `(true, true, nil)` |
| `enter` | accept as above; **then** execute iff `AutoExecute` **and** the accepted text is a complete command with no argument tokens | insert: `(true,true,nil)`; execute: existing slash path |
| `esc` | close **and set `dismissed`** until the trigger token text changes | `(true, true, nil)` |
| `shift+tab` | close (no reverse cycle in v1) | `(true, true, nil)` |
| `alt+enter` | newline → falls through → `syncSuggest` | fall through |
| printable / `bs` / `←→` | fall through → textarea → `syncSuggest` | fall through |
| `pgup/pgdn/home/end` | close, then existing route | fall through after close |
| `ctrl+c` / `ctrl+d` | existing cancel/quit wins | untouched |

**Placement:** in `handleChatKey`, after the modal/dashboard guards, **before** the Tab branch at `tui_keys.go:285` (or Tab focus-cycles instead of inserting). Extracted as `handleSuggestKey`.

**Focus scoping is mandatory** (review finding). Without `m.focus == focusComposer`, this repro breaks: type `/bug`, mouse-click a transcript block (`tui_message.go:278-300` → `setFocus(focusScrollback)`, textarea blurs, popup stays open), press Enter — the stale popup swallows the block-toggle at `tui_keys.go:290-296`. Also `closeSuggest()` on every path that moves focus off the composer or opens a modal.

**`syncSuggest` must be a no-op when the trigger token text is unchanged** since the last call. Otherwise the tail's `syncSuggest` re-runs after every ↑↓ press and resets `Selected` in the same key event, making navigation non-functional.

**INV-TUI-23 / INV-TUI-27 compliance is blocking.** `internal/cli/keymap.go`'s `keyRegistry` is the declared SoT for key meaning, and `/help` is generated from it (`tui_audit_fixes_test.go:194-208`, `TestEveryBoundKeyIsRegistered`). This design redefines `tab` (currently `keymap.go:107`, *"Cycle composer and history blocks"*) and conditionally rebinds `enter`/`up`/`down`/`esc`. It must add a **`scopeSuggest`** with registry rows and a `boundKeyProbes` surface (`keymap_probe_test.go:76-131` currently probes only sessions/overlay/welcome/dashboard, so this defect class would otherwise ship straight past the invariant test). `keymap.go` and `keymap_probe_test.go` go in the file table.

### 4.3 Trigger + token replace

Trigger: `strings.TrimLeft(value, " \t")` starts with `/`, cursor inside that first token, **and the buffer contains no newline anywhere**, and `!dismissed`.

The "no newline anywhere" condition is not cosmetic. `SetValue` = `Reset()` + `InsertString`, and `insertRunesFromUserInput` leaves `m.row` at the **last** row of the reconstructed buffer (bubbles `textarea.go:437-441`). `SetCursor` is row-scoped (`:557-562`). So with any newline after the token — reachable via `alt+enter` then arrowing back to row 0 — the caret lands on the wrong row at a clamped column. Restricting the slash trigger to newline-free buffers costs nothing (slash commands are single-line) and removes the bug class.

```go
func applyTokenReplace(ta *textarea.Model, from, to int, insert string) {
    full := []rune(ta.Value())
    next := string(full[:from]) + insert + string(full[to:])   // clamp from/to
    ta.SetValue(next)
    ta.SetCursor(from + len([]rune(insert)))                    // row 0, guaranteed
}
```

**Accept always appends a trailing space and closes the popup.** Without it: buffer becomes `/bug-audit`, cursor still in the first token, `syncSuggest` re-runs, exact match = 1 item (not zero, so the zero-match close does not fire), popup stays open, Enter inserts again — **the user can never run the skill they just selected.** This is gemini-cli #20136's exact failure class.

`CharLimit` is 0 (`tui_input_setup.go:19`), so `SetValue` cannot silently truncate.

### 4.4 Ranking and ordering

Tiers, cap applied **after** ranking: exact name/alias → name prefix (`/bug` → `/bug-audit`) → alias prefix → subsequence in name → description substring (skills only).

**Empty `/` must not sort alphabetically.** With 29+ entries every item ties in tier 2, so a plain alphabetical tiebreak puts `/architecture-review` first and pushes `/help` past the 8-row window; a hard cap of 25 over 29 entries makes ~4 commands **unreachable** — the very OpenCode #17027 bug this plan cites. Rules:
- Empty query: **builtins first**, then skills; within each, a curated frequency order for builtins (`/help`, `/clear`, `/model`, `/status`, …) and alphabetical for skills.
- No hard result cap that hides items. Rank everything; show an 8-row **scrolling** window with a `n more` affordance.
- `Selected = 0` on every query change.
- `Kind` drives a row glyph so skills and builtins are visually distinct.

`sahilm/fuzzy` is MIT and stdlib-only but is **not in `go.sum`** (verified) — a genuinely new direct dep. With ≤ 40 items, tiering is ~40 lines and every surveyed tool hand-rolls its tiering anyway. **Stdlib.**

### 4.5 Files

| File | Change |
|---|---|
| `overlay.go` + test | new — Phase 0 spike, built on `charmbracelet/x/ansi` |
| `slash_catalog.go` | new — SoT |
| `suggest.go` | new — state, detect, rank, `applyTokenReplace`, `syncSuggest`, `closeSuggest`, `handleSuggestKey` |
| `suggest_render.go` | new — box + row formatting |
| `keymap.go`, `keymap_probe_test.go` | **`scopeSuggest` rows + probe surface (INV-TUI-23/27)** |
| `tui.go` | `suggest suggestState` field only (122 lines headroom to the 600 cap) |
| `tui_keys.go` | suggest guard before `routeFocusKey`; `handleSuggestKey` |
| `tui_message.go` | `syncSuggest()` after `textarea.Update`; `closeSuggest()` on the `:96` early return; `closeSuggest()` on focus-changing mouse paths |
| `tui_view.go` | overlay call **and** a popup-open hint variant (the hint line currently reads `enter send` at `:124`, which the popup contradicts) |
| `tui_slash.go`, `chat.go`, `tui_help_content.go` | read from catalog |
| `tui_slash_handlers.go` | **unknown slash becomes explicit** (Phase 1, not deferred) |
| `tui_start.go` | `startAIWithDisplay` split (§2) |
| `chat_repl.go`, `internal/chat/session.go` | retain `*skills.Registry` on `chat.Session` (no import cycle: `internal/skills` imports only `runtime` + `provider`; `chat` already imports `runtime`) |
| `internal/skills/loader.go` | `argument-hint` + `short-description` frontmatter keys (add to `knownSkillKeys`, INV-AG-17); skip-with-warn instead of startup abort |
| `internal/skills/skills.go` | rune-safe truncation (§4.7 S5) |
| `internal/cli/session_test_helpers_test.go` | skills-registry-attached test session (Phase 2 tests need it; v4 did not budget this) |

**Must not:** grow `handleChatKey`/`handleChatEnter` past 80 · put suggest logic in `tui.go` · change `routeFocusKey` · add a fuzzy dep · hand-roll ANSI cutting.

### 4.6 Skills wiring

**No skills gate in v1 — all skills are listed.** CC (`user-invocable: false` opt-out), Crush (`UserInvocable` opt-in) and OpenCode (blanket exclusion) all gate, but with 9 skills and prefix narrowing (§4.4) the menu is never a wall of options: `/b` already leaves one match. Revisit only if a skill proves to be a bad command in practice — the known candidate is `engineering-working-contract`, which is standing rules rather than a task, so `/eng` + Enter injects 6.7KB of doctrine and gets a confused turn. Backlog, not a v1 blocker.

**`argument-hint` frontmatter** — CC's cheapest borrowed feature, and §2's `/bug-audit internal/cli` flow is undiscoverable without it. Populates `ArgsHint`.

**`short-description`** (optional, ≤ 60 chars). Current skill descriptions are 144–199 chars of model-routing prose (*"Not for implementation."*, *"Use for bug audits, defect hunts."*). At 80 cols a row has ~74 cells; `/engineering-working-contract` alone eats 29. Fall back to the first clause of `description`, ellipsised on a rune boundary.

**Slug rule:** lowercase, `[a-z0-9-]`, spaces/underscores → `-`. A name that does not slug cleanly is excluded from `/` with a warning. *All 9 current skills already slug cleanly* — this guards future authoring, it is not a present defect.

**Builtins win** on collision, flat namespace (CC's model). Shadowed skill is excluded from `/` and reported once at startup. Rejected: `/skill:bug-audit` prefixing — it defeats `/bug` → `/bug-audit`.

**Startup-abort hazards must be fixed** because advertising skills invites authoring more:
- One malformed `SKILL.md` aborts `mivia chat` entirely (`chat_repl.go:79-81` → `chat_command.go:80-83`).
- A skill named `multi_step`/`delegate`/`oneshot` aborts startup via `duplicate handler` (`runtime/dispatcher.go:193-195`), since `registerSkillHandlers` (`dispatcher.go:139-179`) runs after those are registered (`:95-137`).

Both → **skip the offending skill, warn, continue.**

**Degradation must be visible.** `--no-tools` ⇒ zero skills (`chat_repl.go:72-74`); launching from a subdirectory ⇒ zero skills (`workspace.Open` never walks up for `.mivia`, `root.go:18-41`). The popup shows a live count and `/help` explains a zero.

### 4.7 Security

| # | Finding | Action |
|---|---|---|
| S1 | No privilege escalation: skills are already registered `runtime.Subagent` with `FullRegistry` and reachable via `dispatch_tasks` (`dispatcher.go:139-179`). Option D is weaker still — a normal turn. | State explicitly; verify the session principal is unchanged (INV-AG-9/19) |
| S2 | ANSI injection from a `SKILL.md` description into the terminal | Safe **only if** the catalog sources sanitized `Definition.Name`/`Description` — `SanitizeModelFacingText` (`skills.go:104-121`) strips bytes `< 0x20` including ESC, applied at `loader.go:78-79`. Never render `Instructions`. Make this dependency explicit. |
| S3 | Phase 3 `@`: `tools.isSecretPath` (`tools.go:330`) is **fail-open by default** — INV-SEC-1, *"an unconfigured workspace skips nothing"* (`secret_path_test.go:109-113`). Exporting it is **not** by itself a boundary. | The `@` picker must thread the session's configured patterns, or it surfaces `.env` by name |
| S4 | Popup provenance leak | None — `Definition` has no `Path` field (`skills.go:14-27`) |
| S5 | `SanitizeModelFacingText` truncates by **byte** index (`skills.go:117-119`), unlike the package's own rune-safe `truncateRunes` (`loader.go:120-130`). A CJK name near the 64/200-byte caps yields invalid UTF-8. Harmless in a `%q` prompt today; **this plan is what first feeds it to `lipgloss`**, corrupting width math and box borders. | Fix to rune-safe; add a `utf8.ValidString` case |

### 4.8 Phases

| # | Deliverable | Ships alone |
|---|---|---|
| **0** | `overlay.go` spike — decide overlay vs stacked (§3). **Gate.** | n/a |
| **1** | Catalog (builtins + skills) · `scopeSuggest` keyRegistry rows · rewire `isLocalSlash`/`handleTab`/`/help` · unknown slash explicit · loader skip-with-warn + `user-invocable`/`argument-hint`/`short-description` · registry on `chat.Session` | Maintainer-visible only |
| **2** | **The user's ask, whole:** popup + ↑↓ + Tab/Enter accept, over builtins **and** skills; skills run by content injection (§2); popup-open hint line | **Yes** |
| **3** | `wsRoot` + `@` file paths (WalkDir, S3 boundary) | After 2 |
| **4** | Mouse hit-testing (overlay already yields x/y/w/h) | Optional |

**v4 put skills in Phase 3, behind a refactor and a builtins-only release. That was wrong** — skills are the headline ask, and under option D they are cheap. Phases 1+2 together are smaller than v4's 1+2 and deliver the entire request.

### 4.9 Tests

**Phase 0:** O1 splice preserves visible text under an unterminated SGR run crossing the cut column · O2 double-width/CJK width accounting via `lipgloss.Width` · O3 full-width row · **method: `stripANSI` + substring/width, not byte goldens** (§3).

**Phase 1:** A0a every `handleSlashImpl` case ∈ catalog · A0b every catalog TUI entry has a handler · A0c `isLocalSlash` ≡ catalog · A0d `handleTab` ≡ catalog **including `/exit` `/quit` `/provider` `/workspace`** · A0e `AutoExecute` table · A0f aliases · A0g `/search` present · A0h unknown slash does not reach `startAI` · A0i malformed `SKILL.md` ⇒ startup survives, skill absent · A0j reserved-name skill ⇒ startup survives · A0k all 9 skills present in the catalog.

**Phase 2:** A15 `applyTokenReplace` goldens incl. **a post-token newline case** · A1 `/` opens · A2 `/lo` → `/load` · A3 Tab inserts **with trailing space and closes** · A4 Enter on `/help` executes + `Reset` · A5 Enter on `/model` accepts only · A5b `/bug` + Enter → `/bug-audit ` then a second Enter runs the skill (dead-end regression) · A6 Esc closes **and stays closed on the next keystroke** · A7 Tab still focus-cycles when closed · A8 `hello /lo` no popup · A11 waiting + accept ⇒ queue unchanged · A11b zero items ⇒ closed · A12 `height=8` (the real `termH` floor) with a full window ⇒ header and composer still visible · A14 `tui.go` ≤ 600 · A16 ↑ moves selection, caret unmoved, transcript unscrolled · A16b **two `down` presses reach `Selected==2`** (syncSuggest reset regression) · A17 shift+tab closes · A18 early-return send path closes · A19 selection resets on query change · A20 nothing unreachable at empty `/` · A21 window scrolls · A22 `/bug` ranks `/bug-audit` first · A23 skill accept sends `Instructions`, displays the short label · A24 popup + focus change: click transcript, Enter toggles the block **not** the popup · A25 popup + modal precedence · A26 popup + resize against the `tui_view.go:83-87` clamp · A27 popup + live panel / run dashboard visible · A28 zero skills · A29 CJK skill name renders with correct box width · A30 `boundKeyProbes` covers `scopeSuggest`.

**The "no turn started" assertion needs care.** `startAI` sets `m.waiting` and appends `ChatBlockUser` synchronously before spawning (`tui_start.go:58,79`), so `!m.waiting && len(pendingQueue)==0 && no new ChatBlockUser` is a sound proxy **for calls through `startAI`** — which, under option D, is now every path. (Under v4's option A it was falsifiable: a tool-enabled subagent could run to completion with every clause holding. Another reason D wins.)

**Policy:** `.mivia/rules/20-agent-quality.md` requires **mutation proofs** for guard-class assertions (A3/A4/A5/A11/A18/A24) and naming the invariant IDs exercised (INV-TUI-16/23/27, INV-AG-9/17/19, INV-SEC-1). Budget both.

---

## 5. Residual risk

| Risk | Severity | Mitigation |
|---|---|---|
| ANSI splice correctness | **High** | Phase 0 gate on `x/ansi` + width assertions; named fallback with budgeted cost |
| `keyRegistry` drift bypassing INV-TUI-23/27 | **High** | `scopeSuggest` rows + probe surface in Phase 1 |
| Injecting a 19KB skill body inflates every subsequent turn's context | Medium | Matches CC's documented behavior; revisit if token cost bites — `context: fork` (option A, done properly with a unique `req.ID`) is the escape hatch |
| Skill descriptions unreadable at 80 cols | Medium | `short-description` + rune-safe ellipsis |
| Skills invisible from a subdirectory | Medium | Live count + `/help` copy; upward `.mivia` walk is a separate plan |

**Non-goals (v1):** welcome-screen popup · mid-line `/` · `@` body injection · bubbletea v2 migration · fuzzy dep · upward workspace-root discovery · per-skill context isolation.

## Appendix — related plans

- `tui-chat-ux-full-experience.md` — deferred `@`; this plan owns composer suggest
- `tui-clean-chat-ux.md` — monomorphic model / no nested `tea.Model`
