# TUI Revamp and Refactor Implementation Plan

## Status

Implementation-ready after source and handoff reconciliation. No production implementation is authorized by this artifact alone.

## Current phase

Phase 0 — establish the typed transcript/event seam and compatibility tests.

## Last verified

2026-07-27. Source, tests, `go.mod`, `HANDOFF.md`, `.ai/` rules, and current worktree state were inspected. Three independent read-only review lanes validated the plan. Tests were not run from the UNC-backed PowerShell checkout; native WSL verification remains required.

## Next action

Create an isolated implementation worktree, re-run the baseline gates there, then execute Phase 0 only. Stop if baseline tests or persistence compatibility cannot be established.

## Decision summary

- Keep Bubble Tea as the sole interactive runtime.
- Reuse Bubbles and Lip Gloss; do not add tview, gocui, termui, gowid, or a second terminal event/rendering model.
- Do not perform a dependency upgrade as part of the first refactor. The repository currently pins Bubble Tea `v1.3.10`, Bubbles `v1.0.0`, and Lip Gloss `v1.1.0`. A coordinated v2 migration is a separate task after the refactor is behaviorally stable.
- Keep the plain `--plain` REPL behavior unchanged.
- Keep application state transitions inside the Bubble Tea update loop. Stream producers may publish events, but must not mutate `tuiModel` or transcript blocks directly.
- Preserve current user changes and do not reset, clean, overwrite, or commit unrelated worktree modifications.

## Evidence ledger

### Current implementation

- `internal/cli/tui.go:56-188` — `streamBridge` owns concurrent stream delivery, cancellation-related state, bounded buffers, and notifications.
- `internal/cli/tui.go:189-286` — `tuiModel` currently owns `messages []string`, `thinkingBuf`, `showThinking`, `pendingQueue`, Bubbles textarea/viewport/spinner, and tool state.
- `internal/cli/tui.go:334+` — `Update` owns key routing, queueing, cancellation, slash commands, stream events, tools, thinking, and viewport navigation.
- `internal/cli/tui.go:934-1010` — `finishStream` resets stream/tool/thinking state and starts queued work.
- `internal/cli/tui.go:1013-1128` — append, viewport rebuild, truncation, and history prepend remain string/cache based.
- `internal/cli/tui.go:1234-1379` — local slash commands currently render through `appendMsg`/`appendInfo`.
- `internal/cli/tui_view.go:12-126` — dynamic height budgeting, transcript, tool strip, sticky chrome, and viewport layout.
- `internal/cli/toolpanel.go`, `internal/cli/toolui.go` — live tool ordering, selection, row limits, and rendering.
- `internal/cli/renderer.go:157-386` — history rendering and tool-result preview formatting, separate from live tool rendering.
- `internal/cli/welcome.go` and `internal/cli/chat.go` — welcome/session picker and separate plain-mode path.
- `internal/chat/persistence.go` and `internal/chat/session.go` — existing session JSONL/message persistence boundary.

### Existing coverage

`internal/cli/tui_bridge_test.go`, `tui_tools_test.go`, `tui_view_test.go`, `tui_journey_test.go`, `tui_keys_test.go`, `scroll_fix_test.go`, `toolpanel_test.go`, `toolui_test.go`, `welcome_test.go`, `msgcard_test.go`, `renderer_test.go`, `renderer_history_test.go`, `markdown_test.go`, `table_test.go`, `input_test.go`, and `pixel_test.go` cover useful headless behavior. They do not yet prove typed hydration, stale-event rejection, privacy redaction, race freedom, or real terminal behavior.

### Handoff reconciliation

`HANDOFF.md` correctly identifies the flat ANSI transcript, focus modes, thinking blocks, unified tool presentation, system/slash presentation, composer polish, and mouse hit maps. The following handoff items are corrected here:

- Dynamic composer height is already implemented in `tui_view.go`; do not reimplement it.
- Thinking and tool persistence are not assumed. Default retention is ephemeral/bounded until an explicit product decision approves persistence.
- Phase 6 is split into hit-map infrastructure and mouse behavior because it depends on the typed block model and focus state.
- Real binary/PTY and `-race` gates are mandatory, not optional closeout checks.

## Scope

Implement a typed, width-aware, focusable TUI transcript and interaction model while preserving existing chat/session/tool behavior, the plain REPL, persistence compatibility, and structure limits.

## Non-goals

- No tview or alternative TUI framework.
- No Bubble Tea/Bubbles/Lip Gloss v2 migration in this task.
- No rewrite of `--plain` into cards or Bubble Tea.
- No full side-by-side diff view.
- No speculative skill protocol; add skill blocks only when a real event contract exists.
- No persistence of raw thinking or unbounded tool output by default.
- No raising `.ai/policy/go-structure.json` baselines.
- No auth, provider, workspace-tool, or session-schema redesign beyond the minimum compatibility adapter.

## Cross-cutting contracts

### State and event ownership

1. `tuiModel` and all transcript blocks are owned by the Bubble Tea update loop.
2. `streamBridge` publishes typed events only; callbacks never mutate model fields.
3. Every active turn has a monotonic `turnID` and every stream/tool event carries that ID.
4. Events for a completed, cancelled, or superseded turn are ignored deterministically.
5. Cancellation is idempotent; cleanup completes before a new turn becomes active.
6. No unbounded channels or new per-event goroutines are permitted.
7. Any new synchronization must document ownership and lock ordering and be covered by `go test -race`.

### Transcript and identity

1. Typed blocks are the source of truth; rendered ANSI lines are cache/output only.
2. Each block has a stable unique ID scoped to a session/turn and a kind: user, assistant, tool, thinking, system, or turn divider.
3. Streaming updates mutate the existing block identified by `(turnID, blockID)`, never append duplicates.
4. Ordering is event-order plus explicit sequence numbers; out-of-order or duplicate events are handled without panics or silent reordering.
5. Renderers accept raw structured content and width; they never parse ANSI output to recover state.
6. Existing legacy session messages hydrate deterministically into blocks. No persistence-format change is allowed without a separate migration decision.

### Privacy and retention

1. Thinking is ephemeral and off by default for persistence, logs, snapshots, and diagnostics.
2. Tool previews are bounded by characters and rendered lines, with field-level allowlists/redaction before display or persistence.
3. Sensitive-looking values, credentials, private-key markers, `.env` content, OSC/control escapes, and unnecessary absolute paths must not appear in output, fixtures, snapshots, or session files.
4. Expanded views do not bypass redaction or caps.
5. If product later approves persistence, it requires explicit opt-in, versioned schema, retention/deletion behavior, export semantics, and privacy review before implementation.

### Accessibility and terminal compatibility

1. Every state has semantic text in addition to color/glyphs: focused, running, success, error, collapsed, and expanded.
2. Every action is keyboard reachable; focus is visible without color alone.
3. ASCII/no-color output remains legible; Unicode glyphs are enhancement only.
4. TUI behavior is scoped to TTY mode. `--plain`, piped stdin, `TERM=dumb`, EOF, SIGINT, and cleanup behavior remain compatible.
5. Minimum-width and resize behavior must not panic, lose input, or select stale hit-map ranges.

## Phased implementation

### Phase 0 — Typed transcript model, event mapping, and compatibility seam

Goal: replace the flat ANSI transcript as the source of truth without changing visible behavior or session persistence.

Read first:

- `internal/cli/tui.go`
- `internal/cli/tui_view.go`
- `internal/cli/renderer.go`
- `internal/cli/markdown.go`
- `internal/cli/toolui.go`
- `internal/chat/persistence.go`
- `internal/chat/session.go`
- `internal/cli/scroll_fix_test.go`

Expected files:

- Add `internal/cli/chatblock.go`.
- Add `internal/cli/chatblock_render.go`.
- Add `internal/cli/chatblock_test.go` and, if needed, `chatblock_render_test.go`.
- Minimal edits to `internal/cli/tui.go`, `renderer.go`, and existing scroll tests.

Implementation:

1. Define typed block and typed stream-event structures with stable IDs, sequence, turn ID, bounded payload fields, and explicit redaction/cap helpers.
2. Define deterministic mappings from provider/history messages, streamed assistant deltas, tool start/update/end, thinking deltas, slash outcomes, and queued turns into blocks.
3. Keep `messages []string` temporarily as a compatibility/render cache until all consumers migrate.
4. Implement pure width-aware rendering that returns lines plus block line ranges; render raw markdown/tool content at width 40, 80, 120, and narrow/zero-safe widths.
5. Adapt `loadMoreMessages` and viewport offset preservation to blocks without replacing the existing scroll invariants.
6. Define legacy hydration and round-trip behavior. UI-only collapse/focus state must not leak into session persistence.

Acceptance:

- Existing rendering output remains equivalent for current fixtures.
- Mixed user/assistant/tool turns hydrate in stable order with no duplicates.
- Re-rendering at multiple widths preserves content and block identity.
- Collapse/expand changes only one block.
- History prepend preserves viewport offset and handles empty/error/corrupt input safely.
- No production file exceeds 500 soft/800 hard LOC; no function exceeds 80 soft/120 hard; `tui.go` does not grow past its existing grandfathered ceiling.

Required tests:

- Width matrix, empty/very long content, malformed/duplicate/out-of-order events.
- Legacy session hydration and semantic round-trip.
- Collapse/expand isolation and stable IDs.
- Prepend/load-more offset preservation, including I/O failure and cancellation.
- Redaction/cap fixtures containing fake tokens, private-key markers, `.env`, ANSI/OSC escapes, and absolute paths.

Stop conditions:

- A persistence schema change is required.
- Blocks can only be reconstructed by parsing ANSI output.
- Any stream callback mutates blocks directly.

### Phase 1 — Cheap composer correctness and explicit focus state

Goal: establish keyboard focus before thinking/tools/mouse expansion.

Expected files:

- Add `internal/cli/tui_focus.go`.
- Add `internal/cli/tui_keys.go` or extract from the existing key handlers.
- Add `internal/cli/tui_state.go` only if state transitions cannot remain focused.
- Update `internal/cli/composer.go`, `tui_view.go`, `tui.go`, and journey/key tests.

Implementation:

1. Define `focusComposer`, `focusScrollback`, and `focusTools` with explicit transition rules.
2. Pass actual focus into composer rendering; do not use waiting state as focus.
3. Preserve draft text on focus changes. Printable input while scrollback is focused must either be explicitly rejected or return focus and insert the character; choose and test one rule. Recommended: return focus and preserve the character.
4. Define Tab/Esc/Enter/arrow/PgUp/PgDn/Home/End/Ctrl-C behavior per pane.
5. Add the welcome “continue latest auto-session” shortcut only if it does not conflict with current keys and document it in help.
6. Do not redo dynamic composer height, which already exists.

Acceptance/tests:

- Table-driven key routing proves each key is handled once.
- Focus ring tracks pane, queueing/cancellation remain unchanged, and tool scrolling never changes transcript offset.
- Full keyboard traversal works with no mouse.
- Welcome and chat focus behavior are explicitly separated and tested.

Stop on wrong-pane key handling, lost printable input, or changed plain-mode behavior.

### Phase 2 — Cancellation, stale events, and thinking blocks

Goal: make streaming state safe, then represent thinking as bounded ephemeral blocks.

Prerequisite: Phase 0 and the privacy contract pass.

Expected files:

- Add `internal/cli/thinking.go`.
- Update `tui.go`, `chatblock.go`, stream bridge tests, and journey tests.

Implementation:

1. Add active-turn ownership and stale-event rejection before changing thinking rendering.
2. Make Ctrl-C idempotent in every focus pane; preserve existing quit/cancel distinction.
3. Ensure reset/finish waits for or safely fences late events before starting queued work.
4. Convert thinking deltas into one current-turn block, collapsed after completion and capped when expanded.
5. Keep thinking out of session JSONL, logs, snapshots, and diagnostics by default.
6. Remove or constrain the free-floating thinking panel after parity is proven.

Required tests:

- Cancel during model stream, tool execution, thinking, queued send, immediately before completion, and twice.
- Late event after reset/new turn is ignored; no duplicate blocks or post-cancel writes.
- No-thinking turns create no empty block.
- Ctrl-T policy and selected-block expansion.
- `go test -race ./internal/cli ./internal/chat` and goroutine cleanup checks.

Stop on any race, goroutine leak, stale output, raw thinking retention, or non-idempotent cancel.

### Phase 3 — Unified bounded tool representation and renderer

Goal: make live, history, and completion tool presentation use one structured representation and painter.

Expected files:

- Add/modify `internal/cli/toolblock.go` or keep the type in `chatblock.go` if structure limits allow.
- Modify `toolpanel.go`, `toolui.go`, `renderer.go`, `chatblock_render.go`, and parity tests.

Implementation:

1. Define a canonical tool row with name, status, duration, safe path chip, diff summary, bounded preview, and expansion state.
2. Route live strip, historical block, and finish summary through the shared renderer.
3. Preserve live max-six rows, selection, running-first ordering, and existing status semantics.
4. Apply redaction and caps before rendering; never expose full raw tool output by expansion.
5. Keep tool results out of logs/snapshots unless sanitized fixtures explicitly prove the contract.

Required tests:

- Live/history golden-equivalence for running/success/error/empty/malformed cases.
- Path/diff/status rendering with Unicode and ASCII/no-color modes.
- Result caps by characters and visual lines.
- Sensitive fixture rejection/redaction, including OSC escapes and fake secrets.
- Historical expand/collapse isolation and max-six live-row invariant.

Stop on any privacy leak, unbounded output, renderer divergence, or height overflow.

### Phase 4 — Hit-map infrastructure and mouse interaction

Goal: add deterministic transcript selection/expansion after blocks and focus are stable.

Expected files:

- Add `internal/cli/tui_mouse.go`.
- Modify `chatblock_render.go`, `tui.go`, `toolpanel.go`, and mouse tests.

Implementation:

1. Record screen-relative ranges for rendered blocks during `View`.
2. Version/invalidate hit maps after resize, scroll, prepend, collapse, content update, and layout changes.
3. Keep separate coordinate spaces for status, viewport, tools, composer, and hint.
4. Translate viewport coordinates using the current viewport offset; reject stale map versions.
5. Implement click select, double-click/activation expand, transcript wheel, tool wheel, composer focus, and outside-zone no-op.
6. Preserve near-top lazy history loading and ensure tool wheel cannot move transcript offset.

Required tests:

- Synthetic `View()` then `tea.MouseMsg` for every zone.
- Resize/scroll/prepend/collapse followed by old-coordinate clicks.
- Clipped top/bottom rows, narrow terminals, zero/negative-safe coordinates, streaming state, and near-top loading.
- Keyboard-only equivalent remains complete.

Stop on stale selection, coordinate-space confusion, or any mouse-only required action.

### Phase 5 — System/slash blocks and final cleanup

Goal: represent local command outcomes consistently without inventing a skill protocol.

Expected files:

- Modify `tui.go`, `chatblock.go`, `chatblock_render.go`, `renderer.go`, and slash tests.

Implementation:

1. Represent successful local slash outcomes as system blocks, with errors visibly distinct.
2. Define which outcomes are session-persisted versus re-derived; default to preserving current session semantics and avoiding duplicate replay.
3. Add `blockSkill` only when a stable upstream event exists; otherwise leave it deferred.
4. Remove obsolete flat-message paths only after all consumers and hydration tests pass.
5. Update help/product documentation for final key/focus behavior through the canonical owning doc, not a duplicate handbook.

Required tests:

- Slash success, usage error, command failure, cancellation, reload, duplicate replay prevention, and redaction.
- Legacy session compatibility and corrupt/partial session recovery.
- Full CLI wiring through the built binary.

## Verification ladder

Run from a native WSL checkout, not the Windows UNC path, because the current environment produced `go: RLock ... go.mod: Incorrect function` during delegated inspection.

Per phase:

```text
go test ./internal/cli/ -count=1
go test ./internal/chat/ -count=1
python3 scripts/check_go_structure.py --all
git diff --check
```

For streaming/concurrency phases:

```text
go test -race ./internal/cli ./internal/chat
```

Before final sign-off:

```text
go test ./... -count=1
go vet ./...
make verify
make structure-check
make build
make secret-scan
```

Real runtime/PTY gate, required before claiming interactive completion:

- Built `mivia` TUI under a real TTY.
- `mivia chat --plain` under a real TTY.
- Piped/non-TTY stdin and EOF.
- `TERM=dumb`, `NO_COLOR`, Unicode-poor/ASCII terminal.
- 20-column narrow terminal, resize during input/stream, and normal terminal cleanup.
- Typing, send, queue, cancel, scrollback, focus transitions, tool selection, mouse click/wheel, expand/collapse, reload, and exit.
- Ctrl-C during model/tool/thinking work and immediately before completion.

Record observations and command results; `NOT_RUN` is not a pass. Do not claim mouse/focus completion from headless tests alone.

## Global stop conditions

Stop implementation and escalate if:

- baseline behavior cannot be reproduced;
- persistence format must change without approval;
- a race, leak, stale event, wrong-pane key, stale hit-map selection, or post-cancel write appears;
- raw thinking/tool secrets or PII appear in output, fixtures, logs, snapshots, or session files;
- plain mode, non-TTY behavior, terminal cleanup, or accessibility regresses;
- structure limits require raising a baseline;
- a dependency outside the existing Charm stack becomes necessary;
- the user-owned dirty worktree cannot be safely preserved.

## Required human review

- Product owner: approve thinking retention (recommended: ephemeral/off), tool expansion caps, and slash persistence semantics before Phase 2/3/5.
- Security/privacy reviewer: review redaction, session retention/deletion, and sensitive tool-output handling.
- Engineering reviewer: review event ownership, cancellation/stale-event invariants, and `-race` evidence.
- Terminal/accessibility reviewer: review keyboard-only behavior, no-color/ASCII output, narrow/resize behavior, and PTY observations.

## External component research

The current Charm stack is the best fit because it is already integrated and composable. The primary references consulted were [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Bubbles](https://github.com/charmbracelet/bubbles), [Lip Gloss](https://github.com/charmbracelet/lipgloss), [Huh](https://github.com/charmbracelet/huh), and the alternative [tview](https://github.com/rivo/tview). The plan intentionally does not introduce tview.
