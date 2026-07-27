# Message Bubble Left Rail & Status Chrome — Implementation Plan

**Status:** Ready for implementation (challenged; industry + Grok Build grounded)
**Date:** 2026-07-28
**Product goal:** Premium, scannable chat timeline: every block kind has a clear left identity without reintroducing box borders. Live work feels “on”; history stays calm.
**North-star UI:** **Grok Build (xAI)** polish — accent lines, block-type chrome, restrained live animation, theme-token colors. Secondary references: Cursor / Copilot Chat / Claude Code / Aider.

**Related:** `.ai/plans/tui-chat-ux-full-experience.md` (timeline SoT, work groups, status `→`), unfinished `internal/cli/messagebubble.go`.

---

## 0. Why this plan

User intent (condensed):

- Left-side indicators on message bubbles: **glyphs**, **vertical rails** (per-row color possible), **status animation** for live work.
- Wire **color constants** to real screen actions (tool runs, thinking, stream, error).
- Apply across **block kinds** (user, assistant, tools, thinking, system, work groups).
- Finish MessageBubble properly (structure gates, wire production paths).
- Research how other agentic IDE/chat products do it — and treat **Grok Build** as the premium bar.

This document freezes design, phases, color map, tests, and non-goals before code.

---

## 1. Industry research (what polished products do)

### 1.1 Grok Build (xAI) — primary reference

Public docs + product behavior (Grok Build TUI / theming):

| Pattern | What Grok Build does | Lesson for mivia |
|---------|----------------------|------------------|
| **Accent line on blocks** | Thinking / execute blocks have `accent_enabled`; collapsed groups use a dedicated accent glyph (`collapsed_accent_char`, default `❙`) | Left **1-cell accent** is premium chrome, not a full box |
| **Animate only while live** | `blocks.thinking.animate`, `blocks.execute.accent` animate **while** thinking/running | Motion = incomplete work only; history static |
| **Wave animation budget** | `[animation] fps`, `wave_rows` for accent wave cycle | Reuse one clock; don’t invent a second animation system |
| **Tool bullets** | Configurable bullet styles (`diamond` ◆, triangle ▸, circle, none) on tool headers | Tool identity = glyph + muted body, not rainbow stripes |
| **Expandable affordance** | `expandable_indicator` (`›`) on foldable rows | Collapse chrome stays one character, not a second rail |
| **Layout tokens** | `block_pad_left` between accent and content; outer hpad | Content budget is explicit; rail is part of pad math |
| **Theme tokens** | Central theme (`accent_user`, quantized to 256/16/truecolor); `NO_COLOR` monochrome | mivia: centralize on `brandColor*` + optional later theme |
| **Timeline navigation** | `/timeline` tick rail for turn jumps (changelog) | Separate from per-message accent; mivia has work-group + jump-latest already |
| **Status surface** | Live progress panels for long goals; status line as dashboard | Brand status bar remains primary “what’s happening” meter |

**Premium feel (what to copy):**

1. Quiet assistant text; loud chrome only on **work** (tools / thinking / execute).
2. Accent is **thin and continuous**, not a heavy border box.
3. Motion is **phase-locked** (running/thinking), then freezes to a solid accent.
4. Collapse/expand is **one glyph**, content densifies without losing role identity.
5. Colors come from a **named token table**, not ad-hoc `Color("N")` scatter forever.

**What not to copy blindly:**

- Full theme engine + `pager.toml` in PR1 (scope).
- High FPS wave animation (CPU + flicker on full history).
- Truecolor-only palettes that die on 256-color (GrokNight quantizes cleanly — keep mivia 256-first).

### 1.2 Cursor / Copilot Chat (GUI agent IDEs)

| Pattern | Observation | Lesson |
|---------|-------------|--------|
| **Stacked turn story** | User prompt → reasoning/thinking → tool steps → final answer | Same as mivia north star |
| **Tool steps as accordion** | Collapsed one-liners with icon + name; expand for detail | Aligns with `ChatBlockTool` collapsed + work groups |
| **Status on tabs / composer** | Cursor users request live status on chat tabs | Don’t put all activity only inside bubbles; keep composer/status bar |
| **“Explain before tools”** | Cursor prompts agents to narrate before tool calls | mivia already has interim speech + empty-Content status — rails must not replace honesty |

### 1.3 Claude Code (Ink CLI)

| Pattern | Observation | Lesson |
|---------|-------------|--------|
| **Streaming + permission dialogs** | Progress and tools in terminal chrome | Live incomplete work needs continuous feedback |
| **Status line / context bar** | Optional context usage status line | Separate from left rail |
| **Team panes** | Multi-agent = separate panes, not nested rainbow rails | Subagent nesting stays backlog |

### 1.4 Aider (classic CLI)

| Pattern | Observation | Lesson |
|---------|-------------|--------|
| **Configurable colors** | `--tool-output-color`, error/warning colors | Named tool success/fail colors |
| **NO_COLOR** | Honors monochrome | Rails must have ASCII path |

### 1.5 Emerging agent UX patterns (product design)

From agentic UX writeups (LinkedIn / pattern libraries):

- **Typing / progress indicator** for conversational handoff.
- **Chain-of-thought / tool use** as distinct visual stages.
- Intent → progress → decision communication.

mivia already encodes stages as block kinds; rails make them **scannable at a glance**.

### 1.6 Industry consensus (for mivia)

| Do | Don’t |
|----|-------|
| Thin left accent + role glyph | Full box borders on every message |
| Animate live incomplete only | Animate entire history every tick |
| Collapse tools; keep final answer outside | Bury answer inside tool chrome |
| Color + shape (glyph) | Color-only status |
| Theme tokens | One-off colors per call site |
| Status bar for global phase | Only bubble rails for “is it stuck?” |

---

## 2. Ground truth — mivia codebase

### 2.1 MessageBubble (unfinished)

**File:** `internal/cli/messagebubble.go` (~451 LOC)

| Surface | Status |
|---------|--------|
| `BubbleStyle`, `Padding`, `WithStyle`, `WithRenderer` | Implemented |
| `UserBubble` / `AssistantBubble` | Implemented |
| `Render(text, width, sentAt)` | Implemented but **>120 lines** (structure hard fail if committed as-is) |
| **LeftRail / accent** | **Missing** |
| **Production wiring** | **Gap:** `chatblock_render.go` still calls `formatUserMessageCard`, not `UserBubble` |
| Tests | `messagebubble_test.go` extensive; parity test expects bubble ≡ msgcard |

### 2.2 Block render pipeline

| Kind | Path | Current left chrome |
|------|------|---------------------|
| User | `formatUserMessageCard` | 2-space pad + bg bar + optional time |
| Assistant | `RenderMessageForHistory` | No pad border; markdown |
| Tool | `renderToolBlock` | Icon + name (collapsed) |
| Thinking | `renderThinkingBlock` | `▸/▾ thinking` |
| System / `→` status | system style | `⚙` or `→` |
| Work group | `formatWorkGroupHeader` | `▸/▾ Work · N tools` |
| Live tools | tool panel | brand glyph + yellow tools phase |
| Live stream | `renderStreamVP` | dim `▌` |

### 2.3 Color SoT today (reuse, do not invent)

| Token | 256 | Use |
|-------|-----|-----|
| User / stream | `12` | `tuiUserStyle`, `brandColorStream` |
| User bg | `235` | `tuiUserCardBg` |
| Thinking / multi | `13` | `tuiThinkingStyle`, `brandColorMulti` |
| Tools / time | `11` | `brandColorTools`, `toolTimeStyle` |
| Cyan run / accent | `14` | `brandColorThinking`, tool run |
| OK / queue | `10` | tool ok, brand queue |
| Error | `9` | tool err, brand error |
| Dim / cancel | `8` | dim text, cancel |
| Idle white | `15` | brand idle |

Animation SoT: `logoFrame` + `logoTickCmd` (~80ms), `brandWorkFrames` braille pulse — **status bar**, not transcript history.

### 2.4 Structure constraints

- File soft 500 / hard 800; func soft 80 / hard 120.
- `messagebubble.go` at soft ceiling — **LeftRail in new file**.
- Split `Render` into helpers before commit.
- Borderless policy: tests forbid `╭│╰` box model on message cards.

---

## 3. Design principles (challenged)

1. **Grok-like premium = restraint.** Accent + glyph, not decoration everywhere.
2. **Borderless stays.** Vertical accent is a **gutter decoration**, not a box border revival.
3. **Motion only for incomplete live work.** History rails are static (Grok: animate while thinking/running).
4. **Brand bar remains primary liveness** (`deriveBrandPhase`, `stepDetail`). Rails answer **what kind of block** is this.
5. **Glyph + color**, never color alone (CVD / monochrome).
6. **View-layer only** — never persist rail chrome into `Session.Messages`.
7. **One width budget** — rail ≤ 1 cell + existing pad; content width decreases by rail width once.
8. **Header-first rail** on multi-line blocks (MVP); full-height continuous rail is polish (Grok accent line is continuous but we pay for wrap carefully).

---

## 4. Target visual language

### 4.1 Left accent modes

| Mode | Cells | When |
|------|-------|------|
| **Off** | 0 | `NO_COLOR` optional; minimal |
| **Glyph** | 1 | First line: `›` `◆` `▸` `→` `!` |
| **Accent bar** | 1 | `┃` / `│` / `❙` (Grok collapsed accent) / ASCII `\|` |
| **Live pulse** | 1 | Same cell, `brandWorkFrames[frame%n]` or brightness step while live |

**MVP:** glyph **or** solid accent (not both stacked). Prefer:

- User: `›` in blue (inside bg bar pad)
- Assistant: quiet dim `│` or no glyph (content-first)
- Tool: keep tool icon; optional tools-yellow `│` on header only
- Thinking: magenta `┊` or keep `▸`
- System `→`: leave text semantics
- Error: red `!`
- Work group: keep `▸/▾` only

### 4.2 Continuous multi-color rail (polish, not PR1)

Grok-like wave / multi-row accent:

- Split block height into **segments** with weights (thinking / tools / stream).
- Per-row paint with `brandColor*`.
- **Only** for live work-group / multi-tool wave while `waiting`.
- History: solid tools-yellow `┃` for tool groups, no wave.

### 4.3 Animation policy

| Surface | Animate? |
|---------|----------|
| Status brand bar | Yes (existing) |
| Live open tool panel icons | Yes (existing brand glyph) |
| Live thinking / stream incomplete | Optional Phase 2 — single cell pulse |
| History blocks | **Never** |
| Scrolled-up (`!followOutput`) | Freeze any live pulse |
| Expanded tool dump body | Never |

Piggyback `logoFrame`; no second ticker. Kill criteria: if brand bar + `stepDetail` already answer liveness, **drop bubble animation permanently**.

---

## 5. Color → action map (canonical)

Central table (new `bubble_chrome.go` or extend `brand.go` comments):

| Semantic action on screen | Kind / phase | Rail / glyph | Color token | Existing symbol |
|---------------------------|--------------|--------------|-------------|-----------------|
| User message | `ChatBlockUser` | `›` | `12` | `tuiUserLabel` / `brandColorStream` |
| Assistant final / interim speech | `ChatBlockAssistant` | `│` or none | `8` dim or `15` | `tuiDimStyle` |
| Streaming answer (live) | streamBuf | `▌` (keep) | `8` | existing stream chrome |
| Model thinking | `ChatBlockThinking` / live thinking | `▸`/`┊` | `13` | `tuiThinkingStyle` / `brandColorMulti` |
| Awaiting first token | planning line | brand glyph | `14` | `brandColorThinking` |
| Tool running | live tool row | brand glyph | `11` | `brandColorTools` |
| Tool done ok | tool block | `✓` | `10` | `toolOkStyle` |
| Tool failed | tool block | `✗` | `9` | `toolErrStyle` |
| Parallel / multi tools | phase multi / work group | `◆`/`▸` | `13` or `11` | `brandColorMulti` / tools |
| Empty-speech status | system `→` | keep `→` | `8` | status line |
| Error footer | divider / error | `!` | `9` | `tuiErrorStyle` |
| Cancelled | divider | dim | `8` | `brandColorCancel` |
| Work group header | synthetic | `▸`/`▾` | dim / tools | workgroup |
| Queue | brand only | — | `10` | `brandColorQueue` |

**Config wiring (Phase 1 code constants; Phase 3 optional config):**

```go
// chromeTokens — single place for block left chrome (256-color strings).
const (
  chromeUser      = brandColorStream  // "12"
  chromeAssistant = brandColorCancel  // "8" quiet
  chromeThinking  = brandColorMulti   // "13"
  chromeTools     = brandColorTools   // "11"
  chromeOK        = brandColorQueue   // "10"
  chromeError     = brandColorError   // "9"
  chromeAwait     = brandColorThinking // "14"
)
```

Do not scatter new `lipgloss.Color("N")` in render switch.

---

## 6. API sketch

**New file:** `internal/cli/bubble_leftrail.go` (or `chatblock_rail.go`).

```go
type LeftRail struct {
    Width   int    // 0|1 (MVP 1)
    Glyph   string // first-line optional
    Char    string // continuation / solid accent "│" or "┃"
    Color   string // 256 index token
    Animate bool
    Frame   int
    ASCII   bool
    Plain   bool   // NO_COLOR
}

type railOpts struct {
    ASCII, Color bool
    Frame        int
    Live         bool // incomplete work
}

func railForBlock(kind ChatBlockKind, toolFailed bool, opts railOpts) LeftRail
func applyLeftRail(lines []string, rail LeftRail, totalWidth int) []string
func chromeRenderOpts() railOpts // from terminalToolRenderOptions / NO_COLOR / dumb
```

**BubbleStyle extension:**

```go
type BubbleStyle struct {
    // ...existing...
    LeftRail *LeftRail // nil = no rail chrome
}
```

**Presets:** `RailUser()`, `RailAssistant()`, `RailThinking(live, frame)`, `RailTools(live, frame)`, `RailError()`.

---

## 7. Phases

### Phase A — Finish MessageBubble (required prerequisite)

| Step | Work |
|------|------|
| A1 | Split `Render` into ≤80-line helpers (`renderFastPath`, `renderWithLabel`, `renderStacked` already partial) so structure gate passes |
| A2 | `formatUserMessageCard` → **delegate** to `UserBubble.Render` |
| A3 | Assistant pure-content path: optional thin `AssistantBubble.Render` for content-only (tools stay icon lines) |
| A4 | Keep all `messagebubble_test.go` green; msgcard tests green |
| A5 | Commit `feat(cli): finish MessageBubble and wire user cards` |

### Phase B — Static left chrome MVP (one PR)

| Step | Work |
|------|------|
| B1 | Implement `railForBlock` + `applyLeftRail` |
| B2 | Apply in `RenderChatBlocks` / `appendRenderedBlock` (header line) |
| B3 | UserBubble defaults: user rail `›` + color 12 inside pad |
| B4 | Tool header / thinking / error static rails |
| B5 | NO_COLOR + ASCII matrix tests |
| B6 | Width budget tests; no box-border regression |
| B7 | Commit `feat(cli): static left rail chrome by block kind` |

**DoD B:** Scanning a mixed turn, role is obvious from left column; plain mode readable; no logoTick dependency.

### Phase C — Grok-like live accent polish (optional)

| Step | Work |
|------|------|
| C1 | Live-only pulse on incomplete thinking/stream/tool using `logoFrame` |
| C2 | Freeze when `!followOutput` |
| C3 | Optional continuous accent on live work-group only |
| C4 | Paint budget: no full history re-render solely for rail frame |
| C5 | Commit `feat(cli): live accent pulse for incomplete work` |

### Phase D — Token cleanup / config (backlog)

| Step | Work |
|------|------|
| D1 | Consolidate remaining ad-hoc colors into chrome/brand tokens |
| D2 | Optional config knobs (rail on/off, bullet style) — only if dogfood demands |
| D3 | Classic plain renderer parity (optional) |

---

## 8. Files to touch

| File | Role |
|------|------|
| `internal/cli/messagebubble.go` | Split Render; optional LeftRail field |
| `internal/cli/bubble_leftrail.go` | **New** — rail types, presets, apply |
| `internal/cli/msgcard.go` | Delegate to UserBubble |
| `internal/cli/chatblock_render.go` | Kind → rail application |
| `internal/cli/chatblock_workgroup.go` | Consistent header chrome |
| `internal/cli/brand.go` | Token comments / shared chrome colors only if needed |
| `internal/cli/toolui.go` | Reuse `terminalToolRenderOptions` |
| `internal/cli/*_test.go` | Rail matrix, parity, NO_COLOR, width |
| `.ai/invariants.md` | New INV for rail mapping if critical |

---

## 9. Test strategy

| Layer | Tests |
|-------|-------|
| Unit | `railForBlock` matrix: kind × Color × ASCII |
| Unit | `applyLeftRail` width ≤ total; continuation pad |
| Unit | MessageBubble Render after split still passes existing suite |
| Integration | Mixed timeline stripANSI has expected glyphs |
| Hydrate | View rails match kind (static); no session mutation |
| Env | `NO_COLOR=1`, `TERM=dumb` |
| Regression | Borderless history; work groups; progressive tools; interim/status |
| Phase C only | Live pulse does not run when `!waiting` or scrolled up |

---

## 10. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Scope = 3 features | Phases A→B required; C optional; multi-row wave deferred |
| Dual user render paths | A2 forces single path |
| Paint cost of animation | History static; live-only; freeze on scroll-up |
| Width blowout | Rail 1 cell inside existing pad; tests on 24-col |
| Color-only failure | Glyph always present when rail on |
| Structure gate | New file; split Render |
| Clutter vs tool icons | Header-only rail; don’t triple-stack icons |

---

## 11. Hard non-goals

1. Restoring box-border message cards.
2. Animating historical rails.
3. Persisting chrome into session JSONL.
4. Full Grok theme engine / `pager.toml` clone in PR1.
5. Nested subagent indent rails.
6. Making rails primary activity meter (brand bar wins).
7. Clickable rails / new hitmap zones for chrome.
8. Truecolor-only palettes without 256 fallback.

---

## 12. Open decisions (defaults locked for implementers)

| # | Question | **Default** |
|---|----------|-------------|
| 1 | Full-height continuous rail vs header-only | **Header-only** (MVP); continuous = Phase C work-group only |
| 2 | Assistant rail on/off | **Dim `│` optional**; can set off if dogfood clutter |
| 3 | Animate Phase C? | **Only if** dogfood says brand bar insufficient |
| 4 | Plain CLI parity | **TUI first** |
| 5 | Grok `❙` collapsed accent | Use for **work-group collapsed** optional glyph in B or C |

---

## 13. Implementation order (agents)

```text
1) Phase A — finish MessageBubble + wire formatUserMessageCard
2) Phase B — static rails + color token map + tests
3) Dogfood TUI mixed turns at 80×24 and 40×12
4) Phase C only if liveness still unclear without bubble motion
5) Invariants + structure gate + commit scopes feat(cli)/test(cli)
```

---

## 14. Acceptance

1. User/assistant/tool/thinking/system blocks scannable by left chrome alone (color+glyph).
2. Live incomplete work remains obvious (brand bar + optional Phase C pulse).
3. History never strobes.
4. `NO_COLOR` / dumb TERM readable.
5. MessageBubble committed under structure limits; production uses it for user cards.
6. Visual density competitive with **Grok Build** accent style without copying entire theme system.

---

## 15. Short goal prompt (implementing agent)

```
Read .ai/plans/message-bubble-left-rail.md.

North star: Grok Build-quality thin left accent chrome (not box borders).
Implement Phase A then B. Split MessageBubble.Render, wire formatUserMessageCard → UserBubble,
add bubble_leftrail.go with railForBlock using brandColor* tokens, header-only rails,
NO_COLOR/ASCII paths. No history animation. Tests: rail matrix, width, borderless regression,
messagebubble suite. Validate with go test ./internal/cli/ and structure gate. Commit feat(cli).
```

---

## 16. Sources (research)

**External / product**

- [Introducing Grok Build](https://x.ai/news/grok-build-cli) — terminal coding agent, premium TUI positioning.
- [Grok Build docs overview](https://docs.x.ai/build/overview) — rich mouse-interactive TUI.
- [Grok Build changelog](https://x.ai/build/changelog) — `/timeline` tick rail, navigation polish.
- Local Grok user guide: `~/.grok/docs/user-guide/06-theming.md` — accent lines, block styling (thinking/tool/execute/prompt), animation fps/wave_rows, expandable indicators, theme tokens, NO_COLOR.
- Cursor: agent chat + tool accordion patterns; status-on-tabs demand (forum).
- VS Code Copilot: tool call / thinking collapse styles (vscode issues).
- Aider: tool error/warning colors + NO_COLOR.
- Agentic UX patterns: progress indicators, CoT/tool stages as distinct visual states.

**Internal**

- `internal/cli/messagebubble.go`, `msgcard.go`, `chatblock_render.go`, `brand.go`, `toolui.go`, `chatblock_workgroup.go`
- `.ai/plans/tui-chat-ux-full-experience.md`
