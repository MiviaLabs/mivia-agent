# TUI Revamp — Implementation Plan (rebased)

## Status

**Implementation-ready.** Rebased 2026-07-27 after a four-lane research swarm:

| Lane | Focus | Result |
|------|--------|--------|
| A | mivia codebase (`internal/cli/*`) | Bubble Tea MVU already split; blocks + focus + hit-map **partially shipped** |
| B | GitHub agent CLIs | Crush / OpenCode / Claude Code (Ink) / Codex (Ratatui) patterns |
| C | xAI Grok Build (user guide + crate layout) | Focus ladder, foldable blocks, queue vs interrupt, theming, doctor |
| D | HANDOFF / agentkit / docs conventions | agentkit is **not** a TUI reference; SoT is this plan + residual HANDOFF |

`HANDOFF.md` Phase 0–1 file scaffolding is **stale as greenfield work**. Files exist; residual risk is **integration, parity, and polish**.

## Current phase

**R0 — Revalidate residual acceptance** (do not recreate `chatblock.go` / `tui_focus.go`).

## Stack decision (locked)

| Keep | Reject for this plan |
|------|----------------------|
| Bubble Tea `v1.3.10` + Bubbles + Lip Gloss | tview, gocui, termui, Ink/React rewrite, Ratatui port |
| Dual surface: full TUI + `--plain` REPL + one-shot `-p` | Card-style plain REPL rewrite |
| Subagents as in-process tasks | Process-per-agent UI chrome farm |
| Structure gates (≤500 soft / 800 hard file LOC) | Raising `go-structure.json` baselines |

Coordinated Charm v2 migration is a **separate** task after behavior is stable.

---

## Research synthesis (why these improvements)

### Grok Build (primary UX north star)

Evidence: `~/.grok/docs/user-guide/` (shortcuts, theming, plan mode, sessions, terminal support). Runtime split: **pager TUI** vs **shell agent** (Rust crates); UI is one consumer of session/events.

| Pattern | Grok behavior | mivia today | Gap |
|---------|---------------|-------------|-----|
| Foldable transcript blocks | Entry-level collapse/expand; sticky user headers | `ChatBlock` + collapse; live stream still hybrid | Finish tools as summary blob; dual cache |
| Focus | Simple (default) + Vim opt-in; printable → composer | 3-pane focus; printable → composer | Esc ladder incomplete; no vim mode (optional later) |
| Queue vs interrupt | Enter queues; family-specific cancel-and-send | Queue while `waiting` | No cancel-and-send chord; no queue chips |
| Thinking | Foldable; Ctrl+E expand all thinking | Live `thinkingBuf` + history thinking blocks | Help claims Ctrl+T; **no handler** |
| Tool UI | Collapsed-by-default diamond headers | Live strip max 6 + history summary | Live ≠ history painter |
| Status | Still-running counts above composer | Brand status + step detail | No unified tasks/subagent pane |
| Theming | Theme slots + quantize + auto OS + minimal mode | Hardcoded lipgloss ANSI | No theme registry |
| Slash | Fuzzy menu + palette Ctrl+P | Slash handlers exist | No fuzzy palette / MRU |
| Terminal | doctor, alt-screen policy, clipboard routes | Basic TTY/plain/dumb split | No capability matrix / doctor UX depth |
| Plan mode | Approval modal + edit gate | Not product-ready | Defer (needs agent policy, not chrome only) |

### GitHub ecosystem (architecture lessons)

| Project | Stack | Steal for mivia |
|---------|-------|-----------------|
| [charmbracelet/crush](https://github.com/charmbracelet/crush) | Go + Bubble Tea | Modular TUI packages, permissions modal, session busy signals, logs ≠ UI |
| OpenCode (anomalyco) | TS TUI | `@` files, `!` shell, `/details` tool toggle, git undo (later), mouse optional for copy |
| Claude Code (public analyses) | React/Ink | Single agent loop for all surfaces; minimal chrome; permission modes |
| Codex CLI | Rust + Ratatui | Headless `exec` path; full-screen copy-paste pain → keep plain mode |
| Aider | Python REPL | Git-native edits; proves multipane is optional if agent is strong |

**Architecture rule (all three agree):** agent runtime emits **typed events**; TUI is a **subscriber**. Never mutate `tuiModel` from tool callbacks.

### mivia code reality (2026-07-27)

**Shipped / partial:**

- Welcome + session picker + auto-save labels (`welcome.go`, `logo.go`)
- User cards + composer card (`msgcard.go`, `composer.go`)
- Live tool strip (`toolpanel.go`, max 6)
- Brand status bar (`brand.go`)
- Typed blocks + render (`chatblock.go`, `chatblock_render.go`) — dual `messages []string` cache remains
- Focus enum + Tab/Esc/printable (`tui_focus.go`)
- Hit-map (`tui_hitmap.go`) — composer click → focus incomplete
- Coalesced `streamBridge` (`tui_stream.go`)
- Headless tests (`tui_*_test.go`, journey, tools, scroll)
- Structure: `tui.go` ~476 LOC (split); **do not re-inflate**

**LOC snapshot (approx):**

| File | LOC | Role |
|------|-----|------|
| `tui.go` | ~476 | model, startAI, runTUI |
| `tui_message.go` | ~488 | Update body (fat — extract further if needed) |
| `tui_layout.go` | ~332 | height, tools, finishStream |
| `tui_view.go` | ~284 | View |
| `toolpanel.go` | ~350 | live strip |
| `brand.go` | ~420 | status chrome |

**agentkit:** Cobra control-surface only — **not** a TUI pattern source.

---

## Decision summary

1. Keep Bubble Tea; finish **block-as-SoT**, **focus matrix**, **tool paint parity**, **mouse completeness**, **help/key honesty**.
2. Adopt Grok-style **interaction rules** (queue vs interrupt, Esc ladder, fold defaults) without porting Rust pager wholesale.
3. Adopt Crush/OpenCode **modularity** (submodels via files/packages, not god `Update`).
4. Theme registry + plan mode + full palette = **later waves** after residual R0–R4.
5. Privacy: thinking **ephemeral by default**; tool previews **capped + redacted** (existing `redactPreview` must remain on all paint paths).
6. No persistence schema change without explicit product decision.

## Open product decisions (block only listed phases)

| # | Decision | Default until decided | Blocks |
|---|----------|----------------------|--------|
| D1 | Persist model thinking in session JSONL? | **No** (ephemeral) | Thinking product semantics only |
| D2 | After send: stay composer or auto scrollback? | **Stay composer** (Grok-ish productive default) | Focus polish |
| D3 | Welcome continue-last key | **`ctrl+o`** open latest auto (avoid conflict with `ctrl+l` clear if present) | Cheap win |
| D4 | Historical tool expand: full vs cap | **Cap** (same as live preview) | Tool parity |
| D5 | Vim mode | **Out of scope** this plan | — |

---

## Non-goals

- Side-by-side git diff pane
- Full Grok plan-mode / workflows dashboard
- Subagent multipane “IDE”
- Bubble Tea v2 / dependency upgrades
- Rewriting `--plain` to cards
- Skill protocol invention (`ChatBlockSkill` only when real event exists)
- Raising go-structure baselines
- Copying Crush monorepo layout wholesale

---

## Cross-cutting contracts

### State / events

1. `tuiModel` owned only by Bubble Tea `Update`.
2. `streamBridge` publishes; never mutates blocks.
3. Active turn has monotonic `turnID`; stale events dropped.
4. Cancel idempotent; queue starts only after fence.
5. No unbounded channels / per-event goroutines.

### Transcript

1. `blocks []ChatBlock` = SoT; `messages []string` = **render cache only**.
2. Stable block IDs; width re-render from raw fields, never ANSI parse.
3. Collapse/focus UI state not persisted unless product says so.
4. Hydrate legacy session messages deterministically.

### Privacy

1. Thinking not in JSONL/logs/fixtures by default.
2. Tool expand cannot bypass redaction/caps.
3. No secrets, `.env`, private-key markers, raw OSC in snapshots.

### A11y / terminal

1. Semantic text for state (not color-only).
2. Full keyboard path; mouse optional enhancement.
3. `NO_COLOR` / ASCII glyphs remain legible.
4. Narrow width and resize never panic or select stale hit ranges.

---

## Rebased phases (execute in order)

### R0 — Revalidate + dual-SoT cleanup

**Goal:** Prove what already works; make blocks authoritative; kill inconsistency.

**Read first:** `chatblock.go`, `chatblock_render.go`, `tui_layout.go` (`finishStream`, `renderVP`), `tui.go` (`loadMoreMessages`), `HANDOFF.md`, this plan.

**Work:**

1. Map each residual acceptance item below → PASS / FAIL / PARTIAL with test or smoke note.
2. Ensure all append paths go through `appendBlock` (no stray `appendMsg` for user/assistant/tool).
3. `messages` rebuilt only via `RenderChatBlocks` / cache invalidation; document invariant in code comment once.
4. Drop dead `OnAgentEvent` assignment in `runTUI` if still unused (verify with grep).
5. Align help text in `tui.go` with real key handlers (remove phantom Ctrl+T until R2 ships).

**Acceptance:**

- [ ] Hydrate session → blocks order stable at width 40/80/120
- [ ] Collapse one block does not drop others
- [ ] `loadMoreMessages` preserves YOffset (adapt existing scroll tests)
- [ ] `go test ./internal/cli/ -count=1` green
- [ ] `python3 scripts/check_go_structure.py --all` green

**Stop if:** persistence schema required; ANSI parse needed to rebuild state.

**Files (prefer):** `chatblock*.go`, minimal `tui_layout.go` / `tui.go`; **no** +200 LOC into `tui.go`.

---

### R1 — Focus matrix + Esc ladder + cancel fence

**Goal:** Desktop feel matching Grok simple-mode + safe cancel.

**Work:**

1. Complete key matrix (table tests):

| Key | composer | scrollback | tools |
|-----|----------|------------|-------|
| printable | type | →composer + insert | →composer or no-op if strip needs keys |
| Enter | send/queue | expand selected | expand tool |
| Space | type / tool if selected | expand | expand |
| ↑↓ empty composer | viewport | prev/next **block** | prev/next tool |
| Tab / S-Tab | cycle panes | cycle | cycle |
| Esc | layered: clear expand → blur to composer; **never destroy draft on first Esc** | clear selection → composer | tools → scrollback |
| PgUp/Dn Home/End | history | history | history |
| Ctrl+C | cancel turn / second = quit policy (document) | same | same |

2. Grok-aligned **queue vs interrupt** (minimal):
   - Enter while waiting → queue (exists)
   - Optional: `ctrl+enter` or documented chord → cancel + send immediately (if easy; else document deferred)

3. Stale-event fence: `turnID` on bridge events; ignore after finish/cancel.

4. Welcome continue-last: `ctrl+o` → open `LatestAutoSaveName` (D3).

5. Composer click on hit-map → `setFocus(focusComposer)`.

**Acceptance:**

- [ ] Table-driven focus tests cover matrix
- [ ] Tool wheel never moves viewport YOffset
- [ ] Printable from scrollback focuses composer and does not drop char
- [ ] Race test on cancel + late tick: `go test -race ./internal/cli ./internal/chat`

**Files:** `tui_focus.go`, extract key routing from `tui_message.go` into `tui_keys.go` if function >120 LOC; `tui_hitmap.go` / mouse path; tests.

---

### R2 — Thinking + unified tools

**Goal:** One visual family for tools; thinking as collapsible blocks only.

**Thinking:**

1. Stream updates one `ChatBlockThinking` (or live overlay mapped to same paint path).
2. After turn: collapse by default; expand cap (e.g. 6–12 lines) — align with `maxThinkingLines`.
3. Implement **Ctrl+T**: toggle global expand-default **and** expand selected thinking block.
4. Remove free-floating panel from `View()` once parity proven.
5. D1 default: not persisted.

**Tools:**

1. Single painter: `formatToolLine` / shared item already in `toolui.go` — route:
   - live strip
   - history `ChatBlockTool`
   - `finishStream` (prefer **one block per tool**, collapsed, not one summary blob — if height cost too high, multi-line collapsed list using same painter)
2. Historical expand uses same caps as live (D4).
3. Path chips + duration + status icons identical.

**Acceptance:**

- [ ] Live vs history golden tests for running/ok/err
- [ ] Ctrl+T + selected expand tests
- [ ] Redaction fixtures still pass on expand
- [ ] Live strip still max 6 rows

**Files:** `thinking.go`, `chatblock_render.go`, `toolui.go`, `toolpanel.go`, `tui_layout.go` (`finishStream`), tests.

---

### R3 — Mouse completeness + slash/system chrome

**Goal:** Hit-map is trustworthy; slash outcomes are blocks.

**Mouse matrix:**

| Zone | Click | Double | Wheel |
|------|-------|--------|-------|
| Transcript | select block | expand | scroll / loadMore near top |
| Tools | select | expand | window only |
| Composer | focus | — | no-op |
| Status/hint | no-op | — | no-op |

Invalidate hit-map on resize, scroll, collapse, stream paint, prepend.

**Slash / system:**

1. Successful local slash → `ChatBlockSystem` (dim chip), not user bubble.
2. Errors distinct style.
3. `ChatBlockSkill` deferred until real skill events exist.

**Acceptance:**

- [ ] Synthetic View → MouseMsg tests all zones
- [ ] Stale map version rejected
- [ ] Slash success/error appear in transcript after hydrate rules defined

**Files:** `tui_hitmap.go`, mouse path in `tui_message.go` (or extract `tui_mouse.go` if LOC demands), slash handlers, `chatblock_render.go`.

---

### R4 — Terminal polish wave (bounded)

**Goal:** Steal high-value Grok/OpenCode patterns without full port.

**In scope (small):**

1. **Context-sensitive hint bar** already exists — drive from `(focus × waiting × selection)`.
2. **Optional mouse toggle** config later; for now document that mouse capture may affect copy (OpenCode lesson).
3. **`mivia doctor`** already present — add TUI-related checks if cheap: color depth, width, plain-mode recommendation.
4. **Theme v0 (optional spike):** extract brand colors to a `theme` struct in `brand.go` / new `theme.go` with dark defaults only — no multi-theme picker unless timeboxed ≤1 PR.

**Out of scope for R4:** plan mode modal, palette Ctrl+P, vim mode, OS auto-theme, side diff pane.

**Acceptance:**

- [ ] Help text matches keys
- [ ] Narrow 20-col + resize smoke notes recorded
- [ ] No new dependency

---

### R5 — Sign-off (mandatory)

```text
go test ./internal/cli/ ./internal/chat/ -count=1
go test -race ./internal/cli ./internal/chat
python3 scripts/check_go_structure.py --all
go test ./... -count=1
go vet ./...
make verify
make build
make secret-scan
```

**Real TTY smoke (`mivia chat`):**

- Welcome: open session, continue-last, new chat
- Type / send / queue / cancel mid-stream
- Focus Tab cycle; block expand; tool expand
- Mouse click/wheel on transcript + tools + composer
- Resize during stream; exit clean
- `mivia chat --plain` still works
- `TERM=dumb` / non-TTY path

Never claim interactive done from headless tests alone.

---

## Suggested PR slicing

| PR | Content | Risk |
|----|---------|------|
| PR1 | R0 revalidate + help honesty + dual-SoT | Low |
| PR2 | R1 focus matrix + turn fence + continue-last + composer click | Medium |
| PR3 | R2 thinking Ctrl+T + tool painter parity | Medium |
| PR4 | R3 mouse + slash system blocks | Medium |
| PR5 | R4 polish + docs product-agent if behavior shipped | Low |

Commit scope: `cli` (or `ai` for plan-only; `docs` only if OWNERS path updated).

---

## Later backlog (not this plan)

Ranked after R5, informed by research:

1. Command palette + fuzzy slash (Grok Ctrl+P / Crush)
2. Permission modal (allow once / session / deny) — needs tool gateway hooks
3. Plan mode UI + agent write-gate (Grok; product + safety design first)
4. Theme packs + auto light/dark + minimal mode
5. Tasks pane for subagents/background (status counts first)
6. `@` file picker + `$EDITOR` external compose
7. Attention bell when terminal unfocused on permission/done
8. Charm v2 migration + teatest golden suite
9. Git-backed undo of agent edits (OpenCode; optional)

---

## File ownership (LOC-safe)

| Path | Responsibility |
|------|----------------|
| `chatblock.go` / `chatblock_render.go` | SoT blocks + width paint + ranges |
| `tui_focus.go` / `tui_keys.go` | Focus + key routing |
| `tui_hitmap.go` / optional `tui_mouse.go` | Hit testing |
| `tui_stream.go` / `tui_events.go` | Bridge + agent events |
| `tui_layout.go` | Budget, finishStream, tool apply |
| `tui_view.go` | View composition only |
| `tui_message.go` | Update dispatch (keep thin) |
| `toolpanel.go` / `toolui.go` | Shared tool paint |
| `thinking.go` | Thinking → blocks |
| `composer.go` / `msgcard.go` / `brand.go` / `welcome.go` | Chrome |

**Rule:** prefer new files; never grow `tui.go` toward grandfathered 1682 baseline.

---

## Verification ladder (per phase)

```text
go test ./internal/cli/ -count=1
go test ./internal/chat/ -count=1
python3 scripts/check_go_structure.py --all
git diff --check
```

Streaming/focus phases add:

```text
go test -race ./internal/cli ./internal/chat
```

Record `PASS` / `FAIL` / `NOT_RUN`. Do not claim NOT_RUN as pass.

---

## Global stop conditions

Stop and escalate if:

- baseline cannot be reproduced;
- persistence format must change without approval;
- race, leak, stale event, wrong-pane key, stale hit-map, post-cancel write;
- raw thinking/tool secrets or PII in output, fixtures, logs, or sessions;
- structure gate fails and only fix is raising baseline.

---

## Relation to other artifacts

| Artifact | Role |
|----------|------|
| **This file** | Implementation SoT for remaining TUI work |
| `HANDOFF.md` | Historical UX backlog; re-sync when PRs land |
| `docs/plans/004-crt-welcome-screen.md` | Welcome CRT — treat as largely done |
| `docs/product/agent.md` | Promote user-visible behavior after ship (OWNERS) |
| `docs/architecture/overview.md` | Keep thin; no second UX architecture doc |

---

## Completion report shape (each PR)

- Outcome
- Changed files
- Verification (commands + results)
- Residual risk
- Updated residual checklist in this plan (checkboxes)

Formal audits may use `.ai/templates/agent-report-v1.md`.
