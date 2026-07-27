# TUI Chat UX — Full Experience Implementation Plan

**Status:** Ready for implementation
**Date:** 2026-07-28
**Product goal:** One user Enter produces a readable **story of intent → work → answer** in the terminal, matching ChatGPT/Cursor cognitive model without leaving the CLI.

**North star rule:** Every moment of a turn answers “what is mivia doing?” Prefer model speech when present; otherwise show honest work status from tools; never a blank transcript.

---

## 0. Ground truth (current codebase)

Validated against master as of this plan. Agents must re-verify symbols before editing.

### Already shipped (do not re-break)

| Capability | Where | Tests / notes |
|------------|--------|----------------|
| Bridge content SoT + drain | `internal/cli/tui_stream.go`, `tui_message.go`, `pollCmd` in `tui.go` | `TestBridgePathAssistantToolsAndFinish`, liveness stress |
| Parallel/prune banners complete | `PushCompletedBanner`, `agentEventBridgeCallback` | `TestParallelBannerDoesNotStayActive` |
| Progressive tools → `ChatBlockTool` | `applyToolEvents` / `commitToolIndicesToHistory` in `tui_layout.go` | `TestChatTimelineProgressiveBlocks` |
| Thinking flushes before tools | `flushThinkingToHistory` | phase1 drain tests |
| Interim multi-bubble speech | `Detail: "interim"` in `agent/loop.go`; `PushInterim`; `bridgeDrain.Interim` | `TestInterimAssistantBecomesChatBubble`, multi-bubble order in tools test |
| Stream final answer | `FinalWriter` = bridge; `finishStream` → `ChatBlockAssistant` | smoke / phase1 |
| Message times | `provider.Message.CreatedAt`, hydrate → `ChatBlock.SentAt` | `TestSaveAndLoadPreservesCreatedAt` |
| Borderless user/model chrome | `msgcard.go`, `renderer.go` | msgcard + history tests |

### Known gaps (this plan)

1. Empty `Content` + tools → no speech, no synthetic status (blank story).
2. Weak interim (very short / noise) still becomes a bubble.
3. Status text is raw tool names / “model thinking (Ns)”, not verbs.
4. First-token waiting under user card is weak.
5. Scroll-lock / jump-to-latest missing.
6. Stop/cancel does not keep a clean “partial story” chrome.
7. Long turns: no collapsible work group.
8. Subagent nesting, parallel group card, classic CLI interim — deferred polish.

### Critical files (edit surfaces)

| Area | Files |
|------|--------|
| Agent emit / interim | `internal/agent/loop.go`, `internal/agent/emit.go` |
| Provider stream | `internal/provider/openai_compat_stream.go`, `provider.go` |
| Bridge | `internal/cli/tui_stream.go` |
| Event → bridge | `internal/cli/tui_events.go` |
| Timeline commit / finish | `internal/cli/tui_layout.go` |
| View / composer / status | `internal/cli/tui_view.go`, `composer.go`, `brand.go` |
| Tool render | `internal/cli/toolui.go`, `toolpanel.go` |
| Blocks | `internal/cli/chatblock.go`, `chatblock_render.go` |
| Keys / cancel | `internal/cli/tui_keys.go` |
| Session hydrate | `internal/cli/chatblock.go` `HydrateChatBlocks`, `chat/session.go` |
| Invariants | `.ai/invariants.md`, `Makefile` `invariants` target |

### Non-goals

- Fake model speech (“I’ll use the X tool”) as `ChatBlockAssistant` (dishonest).
- Requiring Content before tools in the agent loop.
- Full web parity (carousels, @-mentions, apply-to-editor).
- Changing session schema beyond optional status metadata if needed.
- ADRs or docs under `docs/` unless OWNERS-owned path is updated deliberately.

---

## 1. Phases overview

| Phase | Name | Outcome |
|-------|------|---------|
| **A** | Empty-Content status + verb map | Never blank during tools |
| **B** | Interim quality gates | No ghost/noise bubbles |
| **C** | Waiting / first-activity affordance | No “is it stuck?” after Enter |
| **D** | Scroll-lock + jump latest | Web-like scroll behavior |
| **E** | Stop / partial turn | Cancel keeps story |
| **F** | Work-group collapse (optional long turns) | Dense turns readable |
| **G** | Hardening | Invariants, smoke, bug-audit |

Implement **A→G in one PR/session** if capacity allows; order is dependency-safe. Do not ship A without B.

---

## 2. Phase A — Verb map + empty-Content status lines

### Goal

When the model issues tools with empty/missing interim speech, the transcript still answers “what is mivia doing?” via **honest status** derived from tools (not invented assistant prose).

### Agent steps

1. **Add verb catalog**
   - New file: `internal/cli/tool_verbs.go` (keep ≤500 LOC).
   - API sketch:
     ```go
     // toolStatusLine returns a short human status for a tool start.
     // e.g. "Searching for auth…", "Reading internal/foo.go…"
     func toolStatusLine(name, detail string) string
     func toolVerb(name string) string
     ```
   - Map at least: `read_file`, `search` / local search tools, `grep`/`search_local` if present, `run_command`, `write_file`, `search_replace`, `delegate` / multi_step, `dispatch_tasks`, `list_dir`, `glob`, parallel/prune banners (skip or “Running tools in parallel…”).
   - Use existing redaction: never put secrets from `detail` into status (`redactPreview` / tool privacy helpers in `toolui.go`).
   - Cap length (~80 runes visible).

2. **Emit status when a tool batch starts without interim**
   - In `tui_layout.go` `applyToolEvents` (or right after first real tool start in a batch):
     - If this is the first open tool of a “wave” (same condition as `flushThinkingToHistory` when `openBefore == 0`), and
     - No interim assistant was just committed in this drain, and
     - No non-empty interim in the same `bridgeDrain`,
     then append a **status block** (see kinds below).
   - Prefer a dedicated kind or system styling so it is not confused with model speech:
     - **Option (recommended):** `ChatBlockSystem` with dim prefix `→ ` and text from `toolStatusLine`, **or**
     - New kind `ChatBlockStatus` if system is overloaded — only if needed; prefer system to avoid hydrate complexity.
   - For parallel batches: one status line summarizing the batch (first tool or “Running N tools…”) — not N status lines.

3. **Wire composer `stepDetail` to verbs**
   - On tool start/end in `applyToolStartEvent` / `applyToolEndEvent`, set `m.stepDetail = toolStatusLine(...)` (running) or clear when no open tools.
   - Keep `EventStep` heartbeats as fallback when no open tools (`tui_events.go` already sets `stepDetail`).
   - Composer already renders `stepDetail` in `composer.go` `composerBottomBorder`.

4. **Live tool row labels (optional same phase)**
   - In `writeToolPanelRow` / `formatToolPanelLine` (`toolpanel.go` / `toolui.go`), show verb + short object when status is queued/running.

### Validation (phase A)

| Check | How |
|-------|-----|
| Unit | `TestToolStatusLine_ReadFile`, `TestToolStatusLine_RedactsSecrets`, `TestToolVerbMap_KnownTools` |
| Timeline | Empty Content + tools: history has status system line then tool blocks; no empty `ChatBlockAssistant` |
| Composer | While tool running, bottom note shows verb status |
| Regression | `TestChatTimelineProgressiveBlocks`, parallel banner tests still pass |
| Privacy | Status never contains raw tokens/passwords from args |

### Definition of done (A)

- Empty-Content tool turn never shows only a blank viewport under the user message for >~100ms after first tool event.
- Status is derived, dim, and non-assistant.

---

## 3. Phase B — Interim quality gates

### Goal

Only promote real speech to interim bubbles; avoid “OK.” / whitespace / single-char ghosts.

### Agent steps

1. **Gate in one place** — prefer `streamBridge.PushInterim` or a shared helper `func shouldCommitInterim(s string) bool` in `tui_stream.go` or `tui_layout.go`:
   - Reject empty / whitespace-only.
   - Reject length &lt; 8 (configurable const).
   - Reject pure punctuation.
   - Optional: reject if equals lifecycle tokens (`queued`, `running`, `completed`).

2. **Apply gate** when appending interim in `updateFromDrain` (already `TrimSpace` — extend).

3. **Agent side** — in `loop.go`, only emit `Detail: "interim"` when `shouldCommitInterim(resp.Content)` (or always emit and let TUI gate — TUI gate is enough; agent emit for bus is fine).

4. **If interim rejected and tools follow** — Phase A status must still fire (empty-Content path).

### Validation (B)

- `TestInterimRejectedWhenTooShort`
- `TestInterimAcceptedWhenRealProse`
- Multi-bubble test still passes with real sentences

### Definition of done (B)

No ghost assistant bubbles; empty-Content turns still get status from A.

---

## 4. Phase C — Waiting / first-activity affordance

### Goal

After user sends, before first token/tool/status, the transcript is not empty.

### Agent steps

1. **On `startAI`** (`tui.go`): after appending user block, set a live flag e.g. `m.awaitingFirstActivity = true` (or derive: `waiting && no tools && stream empty && no interim this turn`).

2. **`renderStreamVP`** (`tui_layout.go`): if awaiting first activity and elapsed &gt; ~300ms (or immediately), show dim line under history:
   ```text
     … planning
   ```
   Use brand glyph optional (`brandGlyph` / `phaseAwaiting` in `brand.go`).

3. **Clear awaiting** on first of: interim commit, tool start, stream bytes, status line, or finish.

4. **Align `deriveBrandPhase`** with `phaseAwaiting` when waiting and no activity yet (already partially exists — verify `tui_view.go` / brand wiring).

### Validation (C)

- Unit: model in waiting, empty chrome → view contains planning affordance.
- After tool event → affordance gone.
- No double “thinking” + “planning” spam (prefer one).

### Definition of done (C)

User never wonders if Enter worked during first 1–2s of a slow model call.

---

## 5. Phase D — Scroll-lock + jump to latest

### Goal

Auto-scroll only when user is following the bottom; expanding tools must not yank viewport.

### Agent steps

1. **Track follow mode** on `tuiModel`: e.g. `followOutput bool` default true.
   - Set false when user scrolls up (mouse wheel on transcript, pgup, etc. in `tui_message.go` / `tui_keys.go`).
   - Set true on jump-to-latest or when user is at bottom after send.

2. **`renderStreamVP` / `renderVP`:**
   - If `followOutput` (or `viewport.AtBottom()` at start of update): `GotoBottom`.
   - Else: preserve `YOffset` (already partial logic — make consistent).

3. **Hint line** in `chatViewLayout` / `tui_view.go`: when `waiting && !followOutput`, append dim ` ↓ latest ` (click or key).
   - Key: e.g. `end` or `ctrl+end` / existing pattern — document in slash help if needed (owned docs only if you touch help strings in code).

4. **Mouse:** wheel up on transcript → `followOutput = false`.

### Validation (D)

- Test with fake viewport offsets if possible; at minimum unit-test helper `shouldFollow(atBottom, userScrolled)`.
- Manual: scroll up during long tools; content grows without jumping; press jump → bottom.

### Definition of done (D)

Matches web chat scroll behavior for long tool turns.

---

## 6. Phase E — Stop / partial turn chrome

### Goal

Ctrl+C / cancel keeps interim bubbles + finished tools; marks turn stopped.

### Agent steps

1. **`handleChatCancel`** in `tui_keys.go` (current: clears tools, stream, waiting, appends system cancel).
   - Change to call a shared `m.abortTurn(context.Canceled)` that:
     - Cancels context (existing).
     - `flushThinkingToHistory()`
     - `forceCommitRemainingTools()` (or commit done tools only; open tools marked cancelled/failed).
     - Commit non-empty `streamBuf` as partial assistant if any.
     - Append divider: `(cancelled · duration)` (already in `appendTurnFooter`).
     - Do **not** wipe `m.blocks` history for the turn.
     - Clear live `toolRows` / buffers after commit.

2. Align bridge: `bridge.Close()` vs `Finish(canceled)` — prefer Finish so drain path can complete cleanly; avoid losing committed blocks.

3. **Queued messages:** keep queue behavior; document that cancel does not auto-send next unless already designed.

### Validation (E)

- `TestCancelKeepsInterimAndToolsInHistory`
- After cancel: waiting=false; blocks include user + interim/status + tools + cancelled divider.
- No panic if cancel before first activity.

### Definition of done (E)

Cancel feels like “stop generation” on the web, not “wipe the turn.”

---

## 7. Phase F — Work-group collapse (long turns)

### Goal

Turns with many tools stay scannable: optional group header over thinking + tools; final answer always outside.

### Agent steps

1. **View-layer group (prefer first)** — without new persistence kind:
   - When rendering consecutive `ChatBlockThinking` + `ChatBlockTool` (+ status system lines) between user and final assistant, allow collapse via a synthetic header in `RenderChatBlocks` or a post-pass.
   - Harder: add `ChatBlockWorkGroup` only if view-layer is insufficient.

2. **Simpler MVP for F:**
   - Collapsed tools already exist (`Collapsed: true` on tool blocks).
   - Add **header line** in live panel only: `Work · N tools · elapsed` using `countTools` + `turnStart`.
   - History: ensure tool blocks stay collapsed by default (already).

3. **Full group (if time):** toggle on selected range or auto-collapse when tool count ≥ 4.

### Validation (F)

- Long turn with 6 tools still usable at 24 rows height.
- Final assistant never inside collapsed group.

### Definition of done (F)

MVP live header is enough to mark phase complete if full group is too large; document residual.

---

## 8. Phase G — Hardening, invariants, audit

### Agent steps

1. **Update `.ai/invariants.md`** with new tests (exact `func Test` names).
2. **Extend `Makefile` `invariants` `-run` regex** if new critical tests must always run.
3. **Run:**
   ```bash
   go test ./internal/cli/ ./internal/agent/ ./internal/chat/ ./internal/provider/ -count=1 -timeout 180s
   go test ./internal/cli/ -run 'ChatTimeline|Interim|Parallel|Bridge|TuiTick|Smoke|Stress' -count=1
   make invariants   # if available
   go build -o mivia ./cmd/mivia/
   ```
4. **Bug-audit checklist (adversarial):**
   - Double interim + status on same step.
   - Status with secret in args.
   - Cancel mid-stream.
   - Force-send / turn fence.
   - Reload session vs live order.
   - Parallel batch status spam.
   - `finishStream` idempotent.
   - Structure gates: files ≤500 soft, funcs ≤80 soft (split if needed).

5. **Commit** with scopes: `feat(cli): …` for UX; `test(cli): …` if tests-only; `chore(ai): …` for invariants.

---

## 9. Cross-phase data flow (target)

```
User Enter
  → ChatBlockUser (+ SentAt)
  → awaiting chrome (Phase C)

Model streams Content (no tools yet)
  → streamBuf live ▌

Tool calls arrive
  → RevokeStream (clear optimistic final stream)
  → if Content interim-worthy → PushInterim → ChatBlockAssistant  [existing]
  → else → status system line from first tools (Phase A)
  → tool starts/ends → live panel + progressive ChatBlockTool
  → composer stepDetail = verb status (Phase A)

More steps
  → more interim bubbles and/or status + tools

Final Content only
  → streamBuf → ChatBlockAssistant → done divider

Cancel
  → commit partial story + cancelled divider (Phase E)
```

---

## 10. Test matrix (must all pass end-to-end)

| # | Scenario | Expect |
|---|----------|--------|
| 1 | Final only, no tools | Single assistant; stream OK |
| 2 | Interim + tools + final | Multi-bubble order (existing) |
| 3 | Empty Content + tools + final | Status line(s) + tools + final; no ghost assistant |
| 4 | Short “OK” + tools | No interim bubble; status + tools |
| 5 | Parallel tools | No sticky yellow parallel; status not N× spam |
| 6 | Cancel mid-tools | History keeps speech/status/tools + cancelled |
| 7 | Scroll up during tools | No jump; jump-to-latest works |
| 8 | Reload session | Content+tools hydrate matches live kinds order |
| 9 | Privacy | No secrets in status/tool expand |
| 10 | Liveness | Poll chain stress still green |

---

## 11. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Status looks like model speech | Dim system/`→` prefix; never `ChatBlockAssistant` for synthetic |
| Double status + interim | Only emit status when interim absent this wave |
| Secret leakage in status | Reuse redaction; prefer path/pattern fields only |
| Structure LOC | New `tool_verbs.go`; split layout helpers |
| Cancel races | Turn fence + idempotent finish; tests |
| Scope creep in F | Ship live “Work · N tools” header as F MVP |

---

## 12. Out of scope (explicit backlog)

- Nested subagent timeline indent
- Prompt-only “always narrate before tools” as sole fix
- Cost/token footers, @-mentions, apply-patch UI
- Human subjective “scroll feels right” under arbitrary remote TERM/tmux without scripted harness

### Residual risk disposition (closed)

| Residual | Disposition | Evidence |
|----------|-------------|---------|
| F full collapse | **Closed** | `chatblock_workgroup.go` view-layer groups ≥2 tools; auto-collapse ≥4; final outside |
| Classic REPL interim | **Closed** | `classic_agent_ui.go` interim + → status; FinalWriter-only finals |
| Hydrate status chrome | **Closed (view-only)** | `HydrateChatBlocksForView` / `ReconstructEmptySpeechStatus`; never mutates `Session.Messages` |
| Scroll beyond unit helpers | **Closed (model Update acceptance)** | `tui_scroll_acceptance_test.go` mouse/key/tick/finish paths |
| Cancel + dual Finish | **Closed (tests)** | `TestCancelThenTurnEndDoesNotDuplicateFooter` + `TestFinishStreamIdempotent` |
| tea.Program + PTY scroll | **Closed** | `tui_scroll_program_test.go`; Linux `tui_scroll_pty_test.go` (keys + SGR mouse) |
| CSI mouse over PTY | **Closed** | `TestScrollPTY_CSIMouseWheelUnfollows/DownRefollows`; auto-enable via `mouseAvailable` + `WithMouseCellMotion` |
| Paint/glyph frame budget | **Closed (View SoT)** | `TestScrollProg_PaintFollowShowsLatestMarker`, `TestScrollIndicator_GlyphWidthBounded` |
| True raster cell-grid paint | **Closed** | `tui_paint_raster_test.go`: timed paintSink + cols×rows cell bitmap from View paint path |

---

## 13. Implementation notes for agents

- Follow `AGENTS.md` + `.ai/` + engineering-working-contract.
- Prefer small reviewable commits per phase if multi-commit; single commit OK if clean.
- Never invent tool `Description()` Go/project-specific strings.
- Do not demote real interim speech back to thinking.
- Do not invent assistant speech for empty Content.
- Re-run structure gate before commit (`scripts/check_go_structure.py` / pre-commit).

---

## 14. Acceptance (full plan)

The TUI turn always has a readable story:

1. User message visible immediately.
2. Within first activity: speech **or** status **or** stream caret.
3. Tools appear as durable transcript blocks as they complete.
4. Final answer is a distinct assistant bubble.
5. Cancel preserves the story.
6. Scroll and long turns remain usable.

---

## 15. Short goal prompt (copy-paste for implementing agent)

```
Read and execute .ai/plans/tui-chat-ux-full-experience.md end-to-end (Phases A–G).

Goal: best-in-class CLI chat timeline. North star: every moment answers "what is mivia doing?" — model speech when present, honest tool-derived status when Content is empty, never a blank transcript. Do not invent fake assistant speech.

Implement all phases in order (A→G) against the concrete files listed in the plan:
- A: tool_verbs.go + empty-Content status lines + composer stepDetail verbs
- B: interim quality gates (no ghost bubbles)
- C: awaiting/first-activity affordance after send
- D: scroll-lock + jump to latest
- E: cancel keeps interim/tools + cancelled footer
- F: long-turn work header MVP (or full group if small)
- G: invariants, full tests, bug-audit, build

Ground every change in current symbols (PushInterim, bridgeDrain, applyToolEvents, finishStream, handleChatCancel, HydrateChatBlocks). Preserve existing regressions (parallel banners, progressive tools, multi-bubble interim, bridge drain, liveness stress).

Validate with go test on cli/agent/chat/provider, make invariants if present, go build ./cmd/mivia/. Commit with feat(cli)/test(cli)/chore(ai) as appropriate. Report outcome, files, verification, residual risk.
```
