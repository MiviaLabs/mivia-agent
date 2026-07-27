# Clean Chat CLI UX — Research-Validated Implementation Plan

**Status:** Implementation-ready
**Date:** 2026-07-27
**SoT path:** `.ai/plans/tui-clean-chat-ux.md`
**Supersedes:** `.ai/plans/tui-ux-revamp.md`, root `HANDOFF.md`, deleted `tui-revamp-implementation-plan.md`

---

## 1. Research method (validated)

Four independent read-only lanes ran against **current tree** (including post–mivia-agent self-updates `ea21c47` and uncommitted focus/key cleanup):

| Lane | Scope | Evidence |
|------|--------|----------|
| A | `internal/cli/*` architecture audit | Source, `go.mod`, structure policy |
| B | Charm stack best practices | Bubble Tea / Bubbles / Lip Gloss docs, chat example, teatest, nested-model guides |
| C | Peer agent CLI UX | Crush, OpenCode, Codex, Claude Code, Grok Build user-guide |
| D | Plan ownership / duplicates | `.ai/INDEX.md`, OWNERS, plan inventory |

**No claim of interactive smoke PASS** until R5 real TTY gate. Headless tests must be re-run per PR.

---

## 2. Stack (locked)

| Keep | Version | Reject for this plan |
|------|---------|----------------------|
| Bubble Tea | v1.3.10 | Ink/React rewrite, Ratatui port, tview |
| Bubbles | v1.0.0 (`viewport`, `textarea`, `spinner`) | Second event loop |
| Lip Gloss | v1.1.0 | Hardcoded ANSI sprawl without theme struct |
| Dual surface | TUI + `--plain` + `-p` | Card rewrite of plain REPL |
| Structure gates | soft 500 / hard 800 LOC | Raising baselines |

Charm v2 migration is a **separate** task.

---

## 3. Current code reality (2026-07-27)

### Shipped / largely done

- Welcome gate + session picker + auto labels + `ctrl+o` continue-last
- Typed `ChatBlock` SoT + `RenderChatBlocks` + hit ranges
- Focus: composer | scrollback | tools (tools half-orphaned after strip removal)
- Stream bridge with caps + turn fence
- User cards + composer card; assistant **header + footer** (`formatModelFooter`)
- Live tool **strip removed from View** → one status line (`◐ N running · M done`)
- Grouped tools at finish → one collapsed `ChatBlockTool`
- Turn duration as `ChatBlockDivider` (not system spam)
- Thinking blocks + `adjustThinkingScroll` + Ctrl+T policy (verify help sync)
- Brand status + headless journey/mouse/keys tests
- `tui.go` ~480 LOC (split); do not re-inflate

### Critical bugs from older plan — status

| Bug (old plan) | Status |
|----------------|--------|
| Assistant card no footer | **Fixed** (`renderer.go`) |
| Duplicate model header in `startAI` | **Fixed** |
| `liveThinkingScroll` dead | **Fixed** (`adjustThinkingScroll`) |
| Duration as system line | **Fixed** (`ChatBlockDivider`) |
| Live tool strip clutter | **View fixed**; layout still reserves strip height |

### Residual pain (clean-chat blockers)

1. **Live user path ≠ history:** `startAI` still pre-paints user card via `appendMsg` → `ChatBlockSystem{Rendered}`; hydrate uses `ChatBlockUser` — width rebuild / selection inconsistency.
2. **Layout ≠ View:** `layout()` still budgets `calcToolPanelLines()` while View paints 0–1 status line → wasted viewport height / jitter.
3. **Dead tool UI:** `toolpanel.go` + `focusTools` keys without painted strip.
4. **Dual event registration risk:** strong `agentEventBridgeCallback` in `startAI` vs weaker `sess.OnAgentEvent` in `runTUI`.
5. **Dual SoT residue:** `messages` cache + `blocks`; stream/thinking parallel until finish; line-count truncate vs multi-line blocks.
6. **Status chrome density:** brand + model + elapsed + tools + stepDetail + queue + msg count — too loud for chat.
7. **No follow-scroll affordance:** at-bottom auto-scroll OK; no “↓ new” when user scrolled up during stream.
8. **Scrollback focus invisible:** only composer border shows focus.
9. **Full rebuild per stream drain:** thrash risk on long sessions (Charm anti-pattern).
10. **Mouse always-on:** copy/paste friction (Bubble Tea + Crush lesson).

---

## 4. External research synthesis

### 4.1 Charm / Bubble Tea best practices

Primary refs:

- https://github.com/charmbracelet/bubbletea
- https://github.com/charmbracelet/bubbletea/tree/main/examples/chat
- https://github.com/charmbracelet/bubbles
- https://github.com/charmbracelet/lipgloss
- https://charm.land/blog/teatest/
- https://leg100.github.io/en/posts/building-bubbletea-programs/
- https://donderom.com/posts/managing-nested-models-with-bubble-tea/
- https://github.com/charmbracelet/crush

| # | Practice | Apply to mivia |
|---|----------|----------------|
| 1 | Pure fast `Update`/`View`; I/O in `Cmd` | Keep streamBridge + poll; never LLM in Update |
| 2 | Nested models: root = layout + focus router | Extract transcript/composer/status as clear modules (files already partial) |
| 3 | Measured layout via `lipgloss.Height` | Fix layout() vs View tool height bug |
| 4 | Official chat: viewport + textarea; resize rewrap; GotoBottom when following | Add explicit **follow** flag |
| 5 | Throttle stream paint (30–60 ms / batch tokens) | Debounce drain → single render |
| 6 | Message structs as SoT; re-render dirty tail | Finish live user as `ChatBlockUser` only |
| 7 | Adaptive / LightDark colors | Theme struct v0; NO_COLOR path |
| 8 | Mouse opt-in or wheel-only; keyboard copy | Config or default mouse off for copy-friendly mode |
| 9 | teatest goldens for layout; model asserts for stream | Extend headless tests |
| 10 | Domain ≠ TUI (Crush) | Agent events → msgs only |

**Anti-patterns to avoid:** work in View; full history re-render every token; hardcoded heights; always-on mouse; auto-scroll while reading history; mega-Update; golden flakiness without fixed size/color.

### 4.2 Clean agent chat UX (peers)

| Principle | Source | mivia target |
|-----------|--------|--------------|
| Transcript is the product | Codex empty state | Quiet chrome; answers dominate |
| Progressive disclosure | Grok folds, paste collapse | Tools collapsed; thinking folded |
| Status ambient | Claude statusline, Crush strip | Slim status: model + phase + queue |
| Stream prose; batch tools | Crush / Grok | Status line live; tools in history |
| Composer sacred | Grok Esc ladder | Draft-preserving cancel; focus ring |
| Density over decoration | Crush compact | Prefer accent rail over nested boxes |
| Minimal / monochrome path | Grok `--minimal` | Keep plain + NO_COLOR |
| Quiet motion | All | One working glyph, not spinner farm |
| One-line trust surface | Permissions | Later; out of R0–R3 |

**Product rule:** If the user cannot scan the last answer and active tool in under a second, fold or delete chrome.

### 4.3 Target layout (clean default)

```
┌─ you ─────────────────────────────┐
│ user text                         │
└───────────────────────────────────┘
╭─ model ───────────────────────────
│ assistant markdown (stream)       │
│ ▸ thinking · 1.2s                 │
│ ✓ tools · N  (collapsed)          │
╰───────────────────────────────────
  ─── · 4.2s · ───
┌─ you / queue ─────────────────────┐  ← mode border
│ › …                               │
└───────────────────────────────────┘
 mivia · model · working · ?          ← slim status + hint
```

---

## 5. Product defaults (resolved for this plan)

| Decision | Default | Rationale |
|----------|---------|-----------|
| Persist thinking in session JSONL | **No** | Privacy + size |
| Focus after send | **Stay composer** | Productive chat default |
| Continue-last | **`ctrl+o` welcome** | Already present |
| Historical tool expand | **Capped** (same redaction as live) | Privacy contract |
| Vim mode | **Out of scope** | Later |
| Mouse default | **Keep on for now**; add `/mouse` or config toggle in R4 | Copy pain known |

---

## 6. Non-goals

- Side-by-side diff pane
- Full Grok plan-mode / workflows UI
- Subagent multipane IDE
- Bubble Tea v2
- Rewriting `--plain` to cards
- Skill protocol (`ChatBlockSkill`) without real events
- Raising go-structure baselines
- Porting Crush/OpenCode code wholesale

---

## 7. Cross-cutting contracts

1. **Blocks own transcript;** `messages` = render cache only.
2. **Only Bubble Tea Update mutates `tuiModel`.**
3. **turnID fence** on all stream/tool/thinking events.
4. **Privacy:** thinking ephemeral; tool previews redacted + capped on expand.
5. **A11y:** semantic text for states; keyboard-complete path.
6. **Structure:** new files preferred; no growth of grandfathered `tui.go` ceiling.

---

## 8. Phased implementation

### R0 — Correctness: layout, events, live user path

**Goal:** Remove residual breakage after Phase-1 visual fixes.

| Work | Files |
|------|-------|
| Align `layout()` height with View (status line 0–1, not toolpanel window) | `tui_layout.go`, `tui_view.go` |
| Live user → `ChatBlockUser{Text}` only (delete pre-rendered system card lines) | `tui.go` `startAI` |
| Single agent→bridge path; remove or align dead `OnAgentEvent` in `runTUI` | `tui.go` |
| Cap/truncate by blocks not raw message lines | `chatblock.go` |
| Retire or quarantine `focusTools` when strip gone (or restore minimal strip intentionally) | `tui_focus.go`, `tui_keys.go` |

**Acceptance**

- [ ] Viewport height stable when tools run (no jump from phantom strip budget)
- [ ] Width reflow: live user matches hydrated user paint
- [ ] No double tool events / missing ToolCallID under cancel
- [ ] `go test ./internal/cli/ -count=1`
- [ ] `python3 scripts/check_go_structure.py --all`

**Stop if:** persistence schema change required.

---

### R1 — Clean hierarchy: quiet chrome + follow-scroll

**Goal:** Transcript-first density.

| Work | Detail |
|------|--------|
| Slim status bar | brand + model + phase (`idle` / `working` / `queue N`) + elapsed only while working |
| Step detail | demote to composer footnote or single truncated chip (already partially there) |
| Follow flag | `followTail bool`; stream only GotoBottom when true; when false show `↓ new` chip |
| Scrollback focus | left accent or selected-block highlight (lipgloss; not color-only) |
| Hint bar | context-sensitive: focus × waiting × selection (max 1 line) |

**Acceptance**

- [ ] Status line ≤ ~model + phase + optional elapsed (no tool dump)
- [ ] Scrolled-up stream does not steal viewport; `↓ new` appears
- [ ] Selected block visible without mouse
- [ ] Keys/journey tests updated

---

### R2 — Stream performance + turn polish

**Goal:** Charm-grade streaming without thrash.

| Work | Detail |
|------|--------|
| Paint throttle | coalesce drain → min 33–50 ms between full `renderStreamVP` |
| Dirty-tail preference | document; incremental if cheap, else throttled full rebuild |
| Thinking default | collapsed after turn; expand cap unchanged |
| Tool history | one collapsible group per turn; expand uses `formatToolLine` + caps |
| Queue chips | optional ghost lines above composer (not only system info spam) |

**Acceptance**

- [ ] Long stream smoke: no multi-second UI freeze
- [ ] Tool expand redaction fixtures pass
- [ ] `go test -race ./internal/cli ./internal/chat`

---

### R3 — Terminal contracts + dead code

**Goal:** Respect shell muscle memory; delete noise.

| Work | Detail |
|------|--------|
| Mouse toggle | config or slash: off = native select/copy |
| Keyboard copy path | optional later; document wl-copy/xclip if OSC52 |
| Delete/trim | unused strip-only paths in View; keep `formatToolLine` |
| Help honesty | sync help MD with real keys (Ctrl+T, Tab, Esc) |
| doctor | cheap TUI checks: width, color, mouse on/off |

**Acceptance**

- [ ] Help matches handlers
- [ ] Mouse-off documented
- [ ] No orphan focusTools without UI (or strip restored on purpose)

---

### R4 — Theme v0 + welcome polish (bounded)

| Work | Detail |
|------|--------|
| Theme struct | extract colors from `brand.go` / styles into named slots |
| Dark default only | optional light later |
| Welcome | one-line session age + model if present; no feature wall |
| Compact padding | optional config flag later |

**Out of scope:** multi-theme picker, OS auto-theme, plan mode, palette Ctrl+P.

---

### R5 — Sign-off

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

**Real TTY (`mivia chat`):** welcome, send, queue, cancel, scroll-up during stream + `↓ new`, block expand, tool expand, resize, plain mode, `TERM=dumb`.

Never claim interactive done from headless alone.

---

## 9. PR slicing

| PR | Phase | Risk |
|----|-------|------|
| PR1 | R0 layout/events/user path | Medium |
| PR2 | R1 quiet chrome + follow | Medium |
| PR3 | R2 stream throttle + tools polish | Medium |
| PR4 | R3 mouse/help/dead code | Low |
| PR5 | R4 theme v0 + sign-off | Low |

Commit scope: `cli` for code; `ai` for this plan.

---

## 10. Later backlog (after R5)

1. Command palette / fuzzy slash (Grok / Crush)
2. Permission modal + allowlists
3. Plan mode + write gate
4. Multi-theme + auto light/dark + minimal mode
5. Tasks pane (subagent counts)
6. `@` file picker + `$EDITOR`
7. teatest goldens suite
8. Charm v2

---

## 11. File ownership (LOC-safe)

| Path | Role |
|------|------|
| `chatblock.go` / `chatblock_render.go` | SoT + paint |
| `tui_layout.go` | height budget, finishStream, renderVP |
| `tui_view.go` | View only |
| `tui_keys.go` / `tui_focus.go` | input routing |
| `tui_stream.go` / `tui_events.go` | bridge |
| `tui_hitmap.go` | mouse zones |
| `toolui.go` | shared tool lines (keep) |
| `toolpanel.go` | retire strip or restore deliberately |
| `brand.go` | slim status + theme slots |
| `composer.go` / `msgcard.go` | cards |
| `welcome.go` | session gate |

---

## 12. Relation to other artifacts

| Artifact | Role |
|----------|------|
| **This file** | Sole implementation SoT for clean chat UX |
| `docs/product/agent.md` | Update only after user-visible ship (OWNERS) |
| `docs/architecture/overview.md` | Stay thin |
| Root `HANDOFF.md` | **Deleted** — content folded here |
| `.ai/plans/tui-ux-revamp.md` | **Deleted** — superseded |

---

## 13. Global stop conditions

- Race, leak, stale event, post-cancel write
- Secrets/thinking in fixtures or session by default
- Structure gate only fixable by raising baseline
- Persistence format change without product approval

---

## 14. Completion report (each PR)

- Outcome
- Changed files
- Verification (commands + results)
- Residual risk
- Checkbox updates in this plan
