# Plan: High-fidelity TUI (edit diffs + tool states + motion)

Verification note (2026-07-28): Phases 0-3 and residual S.4-S.6 implementation are present; the required focused package sweep and `go test ./... -count=1` are green. Full `make verify` reaches structure-check but is blocked by the pre-existing oversized `internal/cli/tui.go` baseline warning. Manual interactive proof remains pending.

Residual closure note (2026-07-28): S.4 per-tool elapsed is now included in committed history rows; S.5 lifecycle-only failure reasons and case-insensitive failed states are covered; S.6 Tab can focus the live tool strip. Post-fix adversarial review is complete. `make verify` reaches structure-check but remains blocked by the pre-existing oversized `internal/cli/tui.go` baseline warning.

**Status:** Phases 0-3 and residual S.4-S.6 implemented; review fixes in progress; **hotfix shipped** for multi-tool status expand + false stall
**Date:** 2026-07-28 (updated same day)
**Product goal:** Make agent work in the mivia TUI readable at Grok Build–class terminal fidelity — **edit diffs**, **tool wave states**, and **honest live progress** — without porting Grok’s full Rust edit-block stack.

**Sources (evidence, not cargo-cult):**

| Source | Role |
|--------|------|
| `github.com/xai-org/grok-build` (Apache 2.0, local clone `/tmp/grok-build`) | Design reference: structured edit DTO → real line diff → gutter paint → collapsed `+N/-M` |
| Grok docs (`~/.grok/docs/user-guide/`) | Themes, permissions — not the core of fidelity |
| mivia `internal/tools/write.go`, `internal/cli/{toolui,toolpanel,chatblock_render}.go`, `internal/agent/loop_tools.go` | Current path |

**License:** Learn patterns from Grok Build. **Do not** paste large bodies of `edit.rs` / `diff.rs`. Independent Go reimplementation. If any Apache code is ever vendored, add NOTICE + attribution (anti-goal for this plan).

---

## 0. Ground truth (validated)

### What Grok Build actually does (portable design)

End-to-end:

```text
apply edit
  → SearchReplaceEditDetail { old/new text, old_line, new_line, ±context, line_prefix }
  → build_diff_hunks via similar::TextDiff (Equal | delete | insert + lo/ln)
  → optional stitch_overlapping_hunks (conservative; bail to separate hunks)
  → EditToolCallBlock surfaces:
       Collapsed: "Edit path +N/-M"
       Expanded / Fullscreen: gutters + content BG (no leading +/-) + optional syntect
```

Key fidelity ingredients (P0/P1 from research):

1. **Real LCS line diff**, not index-zip
2. **File-relative line numbers** + a few context lines
3. **Collapsed one-liner with true insert/delete counts**
4. **Expanded body:** single-column gutter, soft red/green **content** bands, gap markers
5. Progressive syntax HL, dual gutters, HL workers = **P2** (Grok defaults dual **off**)

Reference files under Grok:

- `/tmp/grok-build/crates/codegen/xai-grok-pager/src/diff.rs`
- `/tmp/grok-build/crates/codegen/xai-grok-pager/src/scrollback/blocks/tool/edit.rs`
- `/tmp/grok-build/crates/codegen/xai-grok-tools/src/types/output.rs` (`SearchReplaceEditDetail`)
- Snapshots: `…/scrollback/blocks/tool/snapshots/*diff_*.snap`

### What mivia does today (gaps)

| Layer | Behavior | Path |
|-------|----------|------|
| Diff algorithm | Index-aligned pairwise on **snippet only**; hunk always `@@ -1,N +1,M @@` | `internal/tools/write.go` `generateUnifiedDiff` |
| Stats | `+newLC −oldLC` = **snippet line counts**, not Insert/Delete tags | `formatSearchReplaceResult` |
| `write_file` | Create/overwrite stats only; **no** content diff; old file not fully loaded (by design) | `write.go` |
| Tool API | `Execute → string` only | `internal/tools/tools.go` |
| Model history | Tool result ≤ ~4k chars | `session.go` / `loop_limits.go` |
| **UI event Output** | `redactToolOutput` → **512 bytes** | `loop_tools.go:118–119` |
| Live panel expand | Last 6–10 lines; `colorDiffLine` for edit tools | `toolpanel.go` |
| History expand | First **50** lines, **all dim**, no diff colors | `chatblock_render.go:164–185` |
| Completed tools | Leave strip → `ChatBlockTool` collapsed | `tui_tools_apply.go` |

**Binding constraints (challenger-validated):**

1. Wrong algorithm
2. UI only sees 512B redacted string for live path
3. Model and UI share one string channel

Until those three are fixed, Grok-scale architecture (syntect worker, dual gutters, fullscreen) is unjustified.

### Non-goals (anti-goals)

- Porting Grok `EditToolCallBlock` / syntect / progressive full-file HL worker wholesale
- Dual line numbers as default or as success metric
- Putting full `write_file` body or full overwrite diffs into model context
- New heavy highlighter dep (chroma) in v1
- Selling prettier post-edit red/green as write **safety** (approval is a separate lane)
- Raising go-structure baselines to absorb a mega package
- Snapshot-only “fidelity” without LCS oracle tests
- Copying Apache-licensed Grok source without attribution decision
- Fake CSS-like motion in the terminal that burns CPU every frame without state meaning

---

## 0b. Hotfix shipped (2026-07-28): “Running N tools” toggle + false stall

### Bug: focus without toggle

**Symptom:** Tab/select could land on `→ Running 2 tools…` but Enter/Space did nothing useful.

**Root cause (confirmed):**

1. Multi-tool empty-speech status is a `ChatBlockSystem` with `Rendered` set.
2. `renderPreformattedBlock` always returned that single preformatted line for **all** system blocks, **ignoring `Collapsed`**.
3. `toggleSelectedBlock` flipped `Collapsed` but paint never changed.
4. Live tools lived only in the tool strip; status had no per-tool body and no link to the panel.

**Fix (implemented + tested):**

| Change | Where |
|--------|--------|
| Work-status skips preformatted early-exit | `chatblock_render.go` |
| `▸`/`▾` + multi-line per-tool detail | `renderWorkStatusBlock`, `toolBatchStatusDetail` |
| Multi-tool status starts **collapsed**; Expand shows `· Listing…` rows | `appendEmptySpeechToolStatus`, `statusBlockForTools` |
| Expanding live status focuses tool panel + expands selected row | `toggleSelectedBlock` → `focusLiveToolPanelFromStatus` |
| Regression | `TestIntegration_RunningNTools_StatusExpandsOnToggle` |

### Bug: “stalled” while tools still running

**Symptom:** Composer showed `⚠ stalled` during multi-tool waves that felt hung.

**Root cause:** Stall fired when turn age >5s and the current drain had no stream/tools/thinking — **tool-batch heartbeats only update `stepDetail`**, so open tools still counted as “quiet.”

**Fix:** Clear stall on step heartbeat; only set stall after **15s** with no stream/tools/thinking/**step** activity (quiet since `stepDetailAt` or `turnStart`).

### Hang / long-running tools (analysis, not fully fixed)

| Fact | Path |
|------|------|
| Tool-batch heartbeat every 2s | `startToolBatchHeartbeat` → `EventStep` `"tools k/n done · Ts"` |
| Per-tool timeout | `prepareToolTasks` + capability timeout (default can be minutes) |
| UI commit of open tools | `forceCommitRemainingTools` only at **turn end** |
| If a tool never ends and timeout is huge | Status stays “Running N tools…”, panel spins; looks hung |

**Follow-ups (Phase S below):** surface heartbeat on the status line (`tools 0/2 · 12s`); per-tool elapsed in panel; optional cancel-one-tool; ensure failed/timeout ends always paint `failed` with reason in expand body.

### Hotfix (2026-07-28): `cat .env` / failure not reaching parent

**Confirmed:** `run_command ["cat",".env"]` **succeeded** and leaked file body (`exit=0`) because `cat` is allowlisted and **only** `read_file`/`write`/`grep` used `isSecretPath` — shell utilities bypassed the block.

**Confirmed:** dispatcher `fail()` dropped `Output` on tool errors, so parent loop/UI sometimes only saw lifecycle `failed` with empty Result (felt like “failed but not propagated”).

**Fixed:**

| Change | Where |
|--------|--------|
| Block secret-like paths in `run_command` argv | `secretPathInArgv` + check in `run.go` |
| Preserve tool body on fail | `failResult` + `toolHandler` error body |
| `toolEndDetail` treats `exit=N≠0` / `error:` as failed | `loop_tools.go` |
| UI failed detection for any non-zero exit= | `toolResultFailed` |
| Tests | `run_secret_test.go`, `fail_output_test.go`, toolEndDetail cases |

---

## 0c. States, microanimations, and “alive” fidelity (research)

Fidelity is not only diffs. Grok and mivia both sell **state legibility** under low terminal bandwidth.

### State machine (target, single source)

```text
turn idle
  → awaiting_first_activity   (composer pulse; no fake tools)
  → thinking                  (thinking block / cyan rail pulse)
  → streaming_interim         (assistant bubble(s))
  → tools_running             (status ▸ + tool strip; brand phaseTools/Multi)
  → tools_partial             (k/n done heartbeat; strip updates)
  → streaming_final           (answer)
  → turn_done | cancelled | error
```

| State | Visual today | Gap |
|-------|--------------|-----|
| awaiting | brand phase, hero braille | OK |
| thinking | `┊` rail + thinking block | live vs history mute already tested |
| tools_running | glyph spin via `logoFrame`; status line | **was** non-expandable; fixed |
| tools_partial | heartbeat in composer footer | status line text stays “Running N…” (static) |
| stalled | `⚠ stalled` footer | **was** false positive; fixed |
| tool done | strip → history `ChatBlockTool` | history expand dim, 512B UI path |
| error | red rail / failed glyph | need reason in expand |

### Microanimation inventory (keep cheap)

| Motion | mivia | Grok parallel | Rule |
|--------|-------|---------------|------|
| Brand / braille glyph cycle | `logoFrame++` on ticks | spinner / theme accent | Only while `waiting`; not every idle frame forever |
| Left-rail pulse (cyan) | `RailState*Live` + `railView{Live}` | muted collapsed tools | History **never** pulses while waiting (`TestRailState_HistoryNeverPulsesWhileWaiting`) |
| Tool strip spinner glyph | `brandGlyph(logoFrame+ti)` on open rows | running icon | Keep; add elapsed ms like Grok tool blocks |
| Work-group parallel rail | `RailStateParallelLive` | coalesce multi-edit | OK |
| Hero braille wipe/pulse | `pixel.go` sequence | — | Welcome only; do not run during tools |
| Collapse affordance flip | ▸/▾ | collapsed edit one-liner | **Now** on work-status |

**Principles (do not violate):**

1. **Motion = state.** If nothing is running, stop logoFrame-driven chrome in the tool strip.
2. **One live pulse surface.** Prefer tool strip + composer footer; avoid transcript rows animating after commit.
3. **No full-redraw thrash.** Tick only invalidates strip/footer, not entire scrollback (already mostly true via layout).
4. **ASCII/NO_COLOR path** keeps static glyphs (`>` / `*`) — no braille dependency for meaning.
5. **Elapsed time is fidelity.** Grok stores `elapsed_ms` on tool blocks; mivia has Start/End on `toolRow` but history often drops it — Phase S.

### Interaction fidelity (focus graph)

```text
composer  ⇄  scrollback blocks (Tab)
                ├ work: header     → toggle group
                ├ → status         → expand detail + focus live strip (fixed)
                ├ tool history     → expand I/O (Phase 2: color diffs)
                └ thinking         → expand / scroll window
live tool strip (when open)
                enter/space expand selected row (requires Focused/Selected)
```

Gap remaining: keyboard path into tool strip without first expanding status (optional Phase S: Tab visits strip when `len(toolRows)>0`).

---

## 1. Architecture decisions

### A. Two budgets, one algorithm

| Channel | Purpose | Budget (initial) |
|---------|---------|------------------|
| **Model tool result** (`RoleTool` / `Execute` string) | Model continuity | Keep ≤ `searchReplaceResultMaxBytes` (4096) or session `MaxToolResultChars` |
| **UI preview** (`EventToolEnd.Output` → tool panel / chat blocks) | Human review | Edit tools: raise to e.g. **4–8 KiB** after secret redact; non-edit stay 512 |

UI must **not** grow model context. Prefer:

- Option **UI-1 (minimal):** for `search_replace` / `write_file` only, use a higher `truncatePreview` in `redactToolOutput` / `emitToolEnd` when `Name` is an edit tool.
- Option **UI-2 (clean):** emit short model string + optional `events.Event.Metadata` / side field for UI-only structured preview (path, start lines, counts). Use Metadata only if string-only is insufficient after Phase 0–2.

**Do not** change `Tool.Execute` signature in v1 (high fan-out).

### B. Diff core package

Add pure Go package **`internal/diff`** (or `internal/tools/linediff` if structure prefers tools-local):

- Myers / LCS line diff → tags: Equal | Delete | Insert
- Optional context radius (default **3**, Grok parity)
- Caps: max input bytes per side, wall-clock timeout
- Formatters:
  - `FormatUnified(path, hunks)` — model-facing classic `---`/`+++`/`@@`/`+/-`
  - `Stats(hunks) (ins, del)` — true tag counts
  - Later: `FormatGutterText` for TUI without `+/-`

**Dependency policy:** prefer **zero new deps** (small internal Myers). If a dep is used (`sergi/go-diff`), wrap behind this package + caps; document in PR. **No chroma** in this plan’s P0–P1.

### C. Visual contract (v1)

Inspired by Grok defaults, not dual mode:

```text
Collapsed:  ✓ ✎ search_replace  path  +3/-1
Expanded:
  10  context
  11  old line          # red content BG (or red FG if no truecolor)
  12  new line          # green content BG
  … 4 unchanged lines
  17  more
```

- **v1 may keep leading `+/-`** if cheaper with existing `colorDiffLine`; prefer removing them when gutter + BG land (Grok-like).
- Dual gutters = config later, default **off**.
- Reuse lipgloss styles in `toolui.go` / share with history path.

### D. Structure / file split

- `write.go` is ~318 LOC — extract format/diff helpers rather than grow past soft 500.
- New render helpers under `internal/cli` (`diff_render.go`) shared by tool panel + chatblock.
- Keep tools language-generic (rule 60): no Go-specific wording in `Description()`.

---

## 2. Phases

### Phase S — Tool wave states & liveness (UI, parallel with 0–2)

**Goal:** Multi-tool waves always answer “what is running / stuck / done?” without false stall or dead focus.

| Task | Detail | Status |
|------|--------|--------|
| S.1 | Expandable multi-tool status + live panel focus | **Done** |
| S.2 | Stall only after quiet (no step/stream/tools) ≥15s | **Done** |
| S.3 | Status / composer show live `k/n done · Ts` (wave counters + heartbeat) | **Done** |
| S.3b | Quit after cancel never strands (`agentDone`, force 3rd Ctrl+C, waiter) | **Done** |
| S.4 | Per-tool elapsed in strip + history one-liner | Done |
| S.5 | On tool timeout/fail, expand body always has reason (never lifecycle-only empty Result) | Done |
| S.6 | Optional: Tab cycle includes live tool strip when open | Done |
| S.7 | Status tense: “Running” → “Used N tools” when wave commits | **Done** (formatLiveToolWaveSummary) |

**Acceptance:**

- Multi-tool status Expand always shows ≥1 detail line when ≥2 tools.
- With heartbeat every 2s, `stalledWarning` stays false.
- After real silence >15s with open tools, stall appears.
- Timeout tool Result contains non-empty error text in history expand.

### Phase 0 — Truthful diffs (tools only)

**Goal:** Stop lying about what changed.

| Task | Detail |
|------|--------|
| 0.1 | Implement `internal/diff` LCS line diff + unit oracle table (≥15 cases) |
| 0.2 | Replace `generateUnifiedDiff` with LCS; fix UTF-8-safe truncate |
| 0.3 | Stats: use Insert/Delete counts **or** rename header so it is not fake gitstat |
| 0.4 | Keep result shape recognizable (`--- a/`, `+++ b/`, `@@`) so `resultLooksLikeDiff` still works |
| 0.5 | Update `TestSearchReplaceResultStatsAndPreview` + add cascade-insert case that **fails old code** |

**Acceptance (A1, A2, A3, A5, A6, A7 from research):**

- Mid-block insert produces one `+` line + equals, not a cascade of mismatched pairs.
- Model result ≤ 4096 with intact header when truncated.
- Pathological large inputs bounded + timed out.

**Verify:** `go test ./internal/tools/ ./internal/diff/…`

---

### Phase 1 — Stop starving the UI (agent)

**Goal:** Live TUI can show the diff that tools already produce.

| Task | Detail |
|------|--------|
| 1.1 | In `emitToolEnd` / `redactToolOutput`, special-case edit tools to higher budget (e.g. 4096–8192) after secret redact |
| 1.2 | Ensure `commitToolIndicesToHistory` stores enough text for expand (not permanently 512) |
| 1.3 | Document: model message still capped independently via `capToolResult` |

**Acceptance (A4, A9):**

- Expanded edit tool shows ≥ **40** complete change lines when available.
- Secret-like patterns still redacted in UI path.

**Verify:** `go test ./internal/agent/ ./internal/cli/` (tool end + panel tests)

**Risk residual:** session **reload** hydrates from model messages (≤4k). Live vs reload parity is best-effort until optional UI state persistence (out of scope unless needed).

---

### Phase 2 — Shared diff renderer in history (cli)

**Goal:** Expanded history tools look as good as live panel (today history is dim-only).

| Task | Detail |
|------|--------|
| 2.1 | Extract `renderDiffBody(text, width, maxLines)` using `colorDiffLine` / improved gutter paint |
| 2.2 | Wire `renderToolBlock` expanded path + `writePreviewSection` |
| 2.3 | Prefer **change-centric** truncation (keep hunk headers + first change cluster), not only first-50 / last-10 |
| 2.4 | Collapsed summary: path + true `+ins/-del` when parseable |

**Acceptance:**

- History expand for `search_replace` uses red/green (or theme styles), not only `tuiDimStyle`.
- Width ≤ 80 leaves ≥ 40 content cells with single gutter (A10).
- Existing tool panel tests still pass; add history expand coloring test.

**Verify:** `go test ./internal/cli/ -run 'Diff|Tool|ChatBlock'`

---

### Phase 3 — File-relative hunks + context (tools)

**Goal:** Diffs read like real file reviews.

| Task | Detail |
|------|--------|
| 3.1 | On `search_replace`, locate match offset in full file content; set `old_line` / `new_line` |
| 3.2 | Include ±3 file context lines in unified output (budget-capped) |
| 3.3 | Multi-occurrence `replace_all`: emit multiple hunks or gap markers (`… N unchanged lines`) when cheap |
| 3.4 | `write_file` overwrite: **capped** old vs new diff only if old content load stays within explicit byte budget (e.g. ≤256–512 KiB); else header + “diff omitted (size)” — **preserve no-OOM invariant** (today only line-count scan) |
| 3.5 | Create path: stats only; do not invent empty-file noise |

**Acceptance (A8):**

- Hunk headers use real file line numbers for single unique replace.
- Large overwrite never loads unbounded old content for diff.

**Verify:** tools tests with multi-line files; size-cap tests.

---

### Phase 4 — Optional polish (only after 0–3)

| Item | Notes |
|------|-------|
| Drop leading `+/-` when gutter + BG present | Grok-like |
| Dual line numbers config | Default off |
| Optional light content HL via existing `highlight.go` heuristics | No chroma |
| Work-group aggregate “N files · +x −y” | Parse headers |
| Pre-edit approval / permission mode | **Separate product lane** |
| Progressive full-file HL worker | Defer; only if HL cost justifies it |

---

## 3. File touch map (expected)

| Phase | Files |
|-------|--------|
| 0 | `internal/diff/*` (new), `internal/tools/write.go`, `internal/tools/tools_test.go` |
| 1 | `internal/agent/loop_tools.go`, agent tests |
| 2 | `internal/cli/diff_render.go` (new), `chatblock_render.go`, `toolpanel.go`, `toolui.go`, tests |
| 3 | `write.go`, maybe small helpers; tests |
| Docs | Only if product docs own “tool output format” — prefer plan + tests; update owned docs only if user-facing contract changes (`docs/OWNERS.yaml`) |

---

## 4. Measurable acceptance criteria (no “looks good”)

| ID | Criterion |
|----|-----------|
| A1 | Oracle table N≥15 (old,new) → golden LCS unified form |
| A2 | Cascade-insert case fails pre-change, passes post-change |
| A3 | Model `search_replace` result ≤ configured max; header intact when truncated |
| A4 | UI edit preview ≥ K complete change lines (K=40) without growing RoleTool past A3 |
| A5 | Inputs over maxBytes → bounded omit; no unbounded alloc |
| A6 | Diff under time budget / timeout for large inputs |
| A7 | `+/-` stats = true Insert/Delete **or** renamed honestly |
| A8 | `write_file` never dumps full content to model; oversize omit |
| A9 | Secret-like body redacted on UI path |
| A10 | Dual numbers off by default; width 80 still usable |
| A11 | No unjustified deps; go-diff only behind internal API if used |
| A12 | Independent implementation; no unattributed Grok source |

---

## 5. Verification ladder (per phase)

1. Unit oracle for `internal/diff`
2. `go test` packages touched
3. `go test ./internal/cli/ -run …` for render
4. Manual: `mivia` interactive — `search_replace` mid-file insert, expand history block
5. `make verify` before merge of multi-phase stack
6. Never claim PASS without running the command

---

## 6. Risks / open decisions

| Risk | Mitigation |
|------|------------|
| Live vs session-reload fidelity | Phase 1 documents; optional later persist UI preview |
| Parallel same-file edits | Line numbers may drift; do not stitch unless tested (Grok bails conservatively) |
| Privacy | Diffs contain file content; keep redact + caps; never log raw diffs in fixtures with secrets |
| False security | Plan is **presentation**, not approval |
| Structure limits | Split `write.go` / render helpers early |
| Stats rename breaks model habits | Prefer true counts first; keep wording `+ins −del` only when true |

**Open product decisions (do not block Phase 0–1):**

1. Exact UI budget (4k vs 8k) for edit tool events
2. Whether Phase 3 `write_file` diff is worth any old-file read vs “omit” always for overwrites

---

## 7. Implementation order (PR slices)

0. **PR0 (shipped):** Multi-tool status expand + stall quiet window (Phase S.1–S.2)
1. **PR1:** `internal/diff` + wire `search_replace` (Phase 0)
2. **PR2:** UI budget for edit tools (Phase 1)
3. **PR3:** Shared history/panel diff paint (Phase 2)
4. **PR4:** Heartbeat on status + elapsed + timeout reason (Phase S.3–S.5) — can ship beside PR2
5. **PR5:** File context + line anchors + write caps (Phase 3)
6. **PR6+:** Optional polish (dual LN, syntax in ±, Tab→strip) only if still needed

Each PR must include acceptance-linked tests and a short verify note (commands + results).

---

## 8. Research verification record

| Claim | Verified how |
|-------|----------------|
| Grok uses structured `SearchReplaceEditDetail` + `similar::TextDiff` | Read `/tmp/grok-build/.../diff.rs`, `output.rs` |
| Grok dual LN default **false** | `DiffRenderConfig` default in `edit.rs` |
| Grok collapsed `+ins/-del` | `header_line` + snapshots |
| Grok tool blocks carry `elapsed_ms` | `web_fetch.rs` / `web_search.rs` / edit block patterns |
| mivia naive pairwise diff | `write.go:249–277` |
| mivia UI Output 512B | `loop_tools.go:118–119` |
| History expand no diff color | `chatblock_render.go` tool expand path |
| Work-status preformatted ignored Collapsed | Repro + `TestIntegration_RunningNTools_StatusExpandsOnToggle` |
| Stall false positive on open tools | `tui_layout.go` quiet path used turn age only |
| Challenger: 20% features → 80% quality | Phase 0–2 + S remain critical path |

Parallel agents used: Grok fidelity explore, mivia gap explore, risk challenger. Claims cross-checked against source; hotfix verified with `go test ./internal/cli/`.
