# Bug audit: missing chat messages and silent turn stops

**Status:** ✅ IMPLEMENTED 2026-07-31 — all eight findings fixed.

| # | Fixed in |
|---|---|
| B1 composer keystrokes scroll the transcript and latch follow-mode off | `f44e71f` — `handleChatKey` gates the viewport on routed focus |
| B2 empty model response ends the turn as a silent success | `f44e71f`, with `b83e75b` falling back to the last non-empty text |
| B3 force-send while streaming discards the in-flight answer | `f44e71f` |
| B4 non-stream fallback drops the writer, so the answer is never displayed | `f44e71f` |
| B5 cancelled/errored turns never persist their assistant text | `f44e71f` |
| B6 `layout()` sized the viewport taller than `View()`, clipping the tail | `e335cbe` — `composerPadRows` is now a shared constant subtracted by both paths |
| B7 `--plain` glued every message into one blob | `8d2d9f9` |
| B8 stale run re-announced on every startup | `4f272d1` |

**Known gap: B6 shipped without a regression test.** The fix is correct — both
height computations now subtract the same `composerPadRows` — but nothing asserts
they agree, so re-inlining a literal in either path silently reinstates the clip.
It is exactly the defect shape that looks intermittent, because the frame
self-heals on the next render that does not call `layout()`, and a turn's final
render is `finishStream`. The guard to write asserts the two heights are EQUAL
rather than pinning either to a number, so it survives either side changing shape.
A trial version measured the figures §B6 predicted — `layoutH=17, viewH=15` at
termH=24 — and failed correctly when the subtraction was removed.

Original status: investigation complete, no code changed.Original status: investigation complete, no code changed.
Evidence session: `.mivia/sessions/__last___turn_20260730T172927.402` (deepseek / deepseek-v4-flash, 387 records, 77,149 tokens).
Method: session forensics + live reproduction against the real API + four parallel adversarial code audits (agent loop/provider, TUI render path, event bus/persistence, orchestration ledger). Every finding below was independently re-verified against the source.

## Summary

The two reported symptoms have **five independent root causes** across three layers. It is not "just the TUI", and it is not one bug.

| # | Symptom | Layer | Severity |
|---|---------|-------|----------|
| B1 | Composer keystrokes scroll the transcript and kill follow-mode permanently | TUI input | critical |
| B2 | Empty model response ends the turn as a silent success | agent loop | critical |
| B3 | Force-send while streaming discards the in-flight answer | TUI turn lifecycle | critical |
| B4 | Non-stream fallback drops the writer, so the answer is never displayed | provider | high |
| B5 | Cancelled/errored turns never persist their assistant text | agent loop + session | high |

Supporting/secondary: B6 viewport height mismatch clips the last 2 rows; B7 `--plain` glues all messages into one blob; B8 stale run re-announced forever.

## Evidence

Session forensics established what is and is not lost:

- Persistence of *completed* turns is **correct**. Each assistant message is stored separately with its `tool_calls`. `meta.json`'s `message_count: 387` matches the record count exactly (1 system + 10 user + 178 assistant + 198 tool) — not evidence of loss.
- `__last__*` vs `__last___turn_*` duplication is by design (exit snapshot vs rolling per-turn snapshot). Every save rewrites the full transcript stage-all-then-swap, so **a turn cannot split across two directories**. Verified identical snapshot pairs on disk. Refuted as a cause.
- Two distinct loss shapes are present:
  - Turn ends right after a `tool` result with **no following assistant record**: rec 372 → 373 (tool) → 374 `user "why u stoped?"`. Same at 379/380 → 381.
  - **Consecutive `user` records with zero assistant records between them**: 17:29:55 → 17:31:21, and 17:32:27 → 17:43:56 → 17:44:38 → 17:44:50 → 17:47:06.
- The "proof" message the user could not see **is on disk** (rec 220, 17:31:37, `"Both fixes work. Here's the proof:"`). It was committed and rendered but never visible.

Live reproduction (`mivia chat --plain -p …`, real API, 5-tool turn): six distinct assistant messages persisted correctly; display concatenated all interstitial narration into one blob *after* all tool output, with no separators — `"...what's already here.The workspace contains only the .mivia/ directory."`

---

## B1 — Composer keystrokes scroll the transcript and permanently disengage follow-mode

CONFIRMED · critical · **most likely cause of the specific message the user missed**

`internal/cli/tui_message.go:110-119` feeds every `tea.KeyMsg` to the transcript viewport as well as the textarea. `handleChatKey` never sets `skipViewport` (`tui_keys.go:302` and every sibling return hardcode `false`), so there is no focus gate.

`bubbles@v1.0.0` `viewport.DefaultKeyMap` binds **bare printable runes** (`viewport/keymap.go:28-56`):

```
pgdown, space, f   →  page down        u, ctrl+u  →  half page up
pgup, b            →  page up          d, ctrl+d  →  half page down
up, k              →  line up          down, j    →  line down
left, h / right, l →  horizontal
```

The viewport has no concept of focus, so **typing into the composer scrolls the transcript**. Any upward movement trips `tui_message.go:116-118` → `noteUserScrolledUp()` → `m.followOutput = false`. `shouldFollowOutput` (`tool_verbs.go:305`) only re-arms follow when the viewport is already `AtBottom()`, so after one `u`, `b`, or `k` **follow stays off for the rest of the session** unless the user presses `end` or wheels back to the bottom.

Every subsequent render preserves the stale `YOffset` (`tui_follow.go:10`), so the final answer is appended to `m.blocks`, rendered into the content string, and sits below the visible window.

Empirically confirmed with a throwaway test (composer focused): keys that move the viewport while typing are `["b" "k" "u"]`; typing `"no u fucking idiot"` yields `followOutput=false`, `YOffset` 19/50, and the final-answer marker is **absent from both `viewport.View()` and the full `View()`**.

This matches the session exactly. Rec 213 at 17:31:21 is `"no u fucking idiot!!! u must run YOU!!! ... i rebuilded and restarted harnes u fucking idiot"` — five `u`s and a `k`, typed while the previous turn was still running. The next turn's final answer is rec 220 at 17:31:37, and rec 221 at 17:32:27 is the user reporting they cannot see it. Committed, rendered, off-screen.

Not covered by tests: `tui_scroll_acceptance_test.go`, `tui_follow_test.go`, and `tui_scroll_program_test.go` all drive scroll via `tea.KeyPgUp`, mouse wheel, or direct `noteUserScrolledUp()` — never plain rune keys.

**Fix.** Strip typable bindings from the viewport KeyMap in `newTUIModel` (`tui.go:161`) and `layout()` (`tui_layout.go:26`), keeping the non-typable ones existing tests rely on:

```go
km := viewport.DefaultKeyMap()
km.PageUp       = key.NewBinding(key.WithKeys("pgup"))
km.PageDown     = key.NewBinding(key.WithKeys("pgdown"))
km.HalfPageUp   = key.NewBinding(key.WithKeys("ctrl+u"))
km.HalfPageDown = key.NewBinding(key.WithKeys("ctrl+d"))
km.Up           = key.NewBinding(key.WithKeys("up"))
km.Down         = key.NewBinding(key.WithKeys("down"))
km.Left, km.Right = key.Binding{}, key.Binding{}
m.viewport.KeyMap = km
```

Belt and braces: also skip `m.viewport.Update` for `tea.KeyMsg` when `m.focus == focusComposer`.

---

## B2 — An empty model response ends the turn as a silent success

CONFIRMED · critical · **cause of "it constantly stops"**

`internal/agent/loop.go:257-276`. When `len(resp.ToolCalls) == 0` and `resp.Content == ""`:

- nothing is appended to `l.Messages` (the `:261` guard is correct and must stay — a bare `{"role":"assistant"}` would make the API reject the whole next request),
- `emit(EventAssistant)` is skipped (`:272`),
- nothing was written to `FinalWriter` (the stream produced no deltas),
- `err == nil`.

`runStep` returns `("", true, nil)`. The turn reports **clean success having produced nothing**. `sendAgent` copies unchanged `loop.Messages` and returns nil; `finishStream` commits an empty `streamBuf` and appends `─ done · Ns ─`.

Trigger: `.mivia/mivia.toml` sets `[chat] max_tokens = 4096`, applied to **every step** (`session.go:294` → `openai_compat.go:118`). deepseek-v4-flash emits `reasoning_content`; at a 77k context the model spends the entire output budget on reasoning, upstream returns `finish_reason: "length"` with `content: ""` and no tool calls. `readTurnStream` treats that as a legitimate completion (`openai_compat_stream.go:101`, deliberately — pinned by `TestChatTurnEmptyCompletionDoesNotResend`), so it reaches the loop and stops silently. Reproduces both mid-turn (rec 373) and at turn start (zero assistant records from 17:44:38 on).

`FinishReason` is decoded on **every** path (`openai_compat.go:205`, `openai_compat_stream.go:52,143-145`) and **read nowhere in the codebase** — `length` is indistinguishable from `stop`, and `content_filter` is reported as a normal completed turn. `reasoning_content` is likewise accumulated end-to-end and discarded, so a turn where the model did thousands of tokens of work shows a bare "done".

The `lastText` fallback added in `b83e75b` does not help: it only changes `Run`'s **return value**, which every interactive caller discards (`tui_start.go:59`, `chat.go:39`, `chat.go:94`, `chat_repl.go:154`). It does not append to `l.Messages`, does not emit, and does not write to `FinalWriter`. It fixes only the sub-agent consumer that motivated it.

**Fix.** In the `len(resp.ToolCalls) == 0` branch, when `strings.TrimSpace(resp.Content) == ""`, do not report a clean completion. Keep history clean, but return a user-visible error carrying the diagnosis:

```go
fmt.Errorf("model returned no content (finish_reason=%q, reasoning=%dB, step=%d)",
    resp.FinishReason, len(resp.ReasoningContent), step)
```

Better: retry the step once with a nudge and only error on a second empty response. Separately, branch on `FinishReason`: `"length"` → surface "response truncated (max_tokens=N)" and drop any tool calls from that turn (a `length`-truncated turn that already started emitting `tool_calls` yields half-written argument JSON, which `readTurnStream` accepts because the completion signal *is* present); `"content_filter"` → surface it.

Also fix the `TrimSpace` vs `!= ""` mismatch: `:261` gates on `strings.TrimSpace(...) != ""` while `:269`, `:272`, and `:275` use `resp.Content != ""`. With `resp.Content == "\n"` the message is not appended to history, is emitted as a blank assistant event, and is returned as *non-empty* — so the fallback never fires and the turn completes blank with no trace for the next turn. Compute `trimmed` once and use it for all four decisions.

---

## B3 — Force-send while streaming discards the in-flight answer

CONFIRMED · critical

`internal/cli/tui_start.go:11-47` is reachable with `m.waiting == true`: pressing Enter on an empty composer with a queued item calls `sendNextQueued()` (`tui_keys.go:164-169` → `tui.go:449-461`), which calls `startAI` with **no `m.waiting` guard**. `startAI` then:

1. `m.cancel()` (`:14`) — kills the running turn.
2. `oldBridge.Close()`, `m.bridge = newStreamBridge()` (`:16-18`) — the old worker's later `bridge.Finish(err)` lands on a bridge nobody drains, so `finishStream` **never runs** for that turn.
3. `m.streamBuf.Reset()`, `m.thinkingBuf.Reset()`, `m.toolRows = nil` (`:28-33`) — with no `flushThinkingToHistory()`, no `forceCommitRemainingTools()`, and no `ChatBlockAssistant` appended.
4. `m.appendBlock(ChatBlockUser)` (`:46`).

Observable result: **two `ChatBlockUser` blocks in a row, no assistant block, no `(cancelled)` footer, no tool rows** — the model's visible answer simply vanishes.

The Ctrl+C path does this correctly (`tui_keys.go:83-90`: `m.updateFromDrain(br.Drain())` then `m.finishStream(context.Canceled)`), which is how we know this is an omission rather than a design choice. `TestCancelKeepsQueue` (`tui_chat_ux_test.go:274-284`) deliberately avoids `sendNextQueued` "which would call startAI", so no test covers it.

**Fix.** At the top of `startAI`, if `m.waiting`, run the Ctrl+C sequence against the outgoing bridge before resetting: `m.updateFromDrain(oldBridge.Drain()); _ = m.finishStream(context.Canceled)`.

Related: `session.go:262-322` — `startAI` cancels the old worker and immediately launches the new one, which takes `s.mu` and does `s.turnID++` (`:263`). If it wins the race against the cancelled worker's writeback (`:319`), `myTurn != s.turnID` and the **whole** cancelled turn is discarded — user message, assistant `tool_calls` messages, and all tool results. Nondeterministic: the old worker returns fast when blocked on the HTTP read, slowly when inside a tool. Either serialize force-send behind `workerWG` (as `waitAgentThenQuitCmd` does at `tui_keys.go:128-142`), or make the stale-turn branch merge the suffix instead of dropping it.

---

## B4 — The non-stream fallback drops the writer, so the answer is never displayed

CONFIRMED · high

`internal/provider/openai_compat_stream.go:41-46`:

```go
if !received {
    req.Stream = false
    req.StreamWriter = nil      // the TUI's only output channel, dropped
    return c.ChatTurn(ctx, req)
}
```

The content comes back in `resp.Content`. But the loop's `stream` flag was computed from `opts.FinalWriter != nil` (`loop.go:232`) and is still `true`, so the rewrite at `loop.go:269` (`if !stream && opts.FinalWriter != nil`) is skipped. The only other outlet is a non-interim `EventAssistant`, and `tui_events.go:215` drops those by design:

```go
// Final answer streams via FinalWriter → streamBuf; do not PushInterim finals
// or we would duplicate the assistant block at turn end.
if e.Content != "" && e.Detail == "interim" {
```

Net effect: a **complete answer is appended to history and persisted, and the user sees only `─ done ─`.** This is exactly what the model itself observed at 17:44:10 (rec 375): the proof message is in the session log but not showing.

`TestChatTurnSilentStreamStillFallsBack` (`stream_defects_test.go:171`) asserts the fallback *fires* (request count 2) but never asserts the content reaches the writer.

**Fix.** Preserve the writer across the fallback:

```go
w := req.StreamWriter
req.Stream = false
req.StreamWriter = nil
out, err := c.ChatTurn(ctx, req)
if err == nil && w != nil && out.Content != "" {
    _, _ = io.WriteString(w, out.Content)
}
return out, err
```

---

## B5 — Cancelled or errored turns never persist their assistant text

CONFIRMED · high

`internal/agent/loop.go:249-255`. `runStep` streams content to `FinalWriter` as it arrives, but appends the assistant message to `l.Messages` only *after* `ChatTurn` returns cleanly:

```go
resp, err := l.Completer.ChatTurn(heartbeat, req)
heartbeatCancel()
if err != nil {
    return "", false, err        // partial resp.Content dropped
}
```

Everything already on screen is gone from `l.Messages`. `loop.Run` appended the **user** message unconditionally at `:170`, so an interrupted turn's history is `[…, user]` and nothing else. Then `session.go:324`:

```go
if err != nil {
    return reply, err            // SaveAfterTurn skipped
}
s.SaveAfterTurn()
```

The user message still reaches disk via the next turn's save or the 60s `periodicSaveMsg` tick (`tui_message.go:62-69`) — which is **precisely why the evidence file shows `user 17:44:38 / user 17:44:50 / user 17:47:06` with no assistant record between them.** The model's next request also lacks that text, so it repeats itself.

This also makes UI and store disagree: Ctrl+C commits `streamBuf` into a durable `ChatBlockAssistant` (`tui_layout.go:121-125`), but the worker never gets that text into `Session.Messages`. On restart, `hydrateHistory` rebuilds the transcript from disk and the message the user just read is gone. The session log literally records "i rebuilded and restarted harnes".

**Fix.** On error in `runStep`, append the partial when non-empty before returning (requires `ChatTurn` to return the partially-assembled `resp` alongside `err`, or read the bridge's revoked buffer). Move `s.SaveAfterTurn()` above the `if err != nil` return in `sendAgent`.

---

## B6 — `layout()` computes a viewport 2 rows taller than `View()`, clipping the last 2 rows

CONFIRMED · medium

`tui_layout.go:19` uses `avail := m.height - statusH - composerH - hintH`. `tui_view.go:115` uses `remain := max(minVp, termH-...-padRows)` with `padRows = 2`, accounting for the composer's `Padding(1, 0)` (`tui_view.go:51`). `layout()` does not.

So `layout()` sets `Height = termH - inputH - 4`, then `View()` overwrites it with `termH - inputH - 6` (`tui_view.go:36`). `renderVP` runs during `Update`, where `applyFollowScroll` → `GotoBottom()` sets `YOffset = TotalLineCount - Height_big`. `View()` then shrinks `Height` by 2 without recomputing `YOffset`, so `viewport.View()` renders `lines[YOffset : YOffset+Height_small]` and the **final 2 rows are never painted**.

Confirmed at termH 20/24/30/40/50: `layoutH=17, viewH=15, YOffset=48, total=65`; trailing content `["", "  ─ done · 2ms ─"]` is not rendered — the turn footer is *always* clipped. The `↓` indicator also lights up despite `followOutput == true`, because `View()` sees `!AtBottom()`. Self-heals on the next render that does not call `layout()`, which is why it looks intermittent — but `finishStream` (`tui_layout.go:138`) is the last render of a turn, so it stays missing until the user interacts.

**Fix.** Have `layout()` call `chatViewLayout(...)` so there is one source of truth, or at minimum subtract the same `padRows`.

---

## B7 — `--plain` / `-p` glues every message into one blob

CONFIRMED · medium · reproduced live

`MarkdownWriter` is line-buffered: it emits only on `\n` (`markdown.go:56-79`). Assistant messages do not end in newlines, so each one stays in `mw.buf` and the next message's deltas are appended to the same partial line. Proven in isolation — two writes produce nothing visible until `Flush`, then `"First message about listing the dir.Second message about writing a file."`

In one-shot mode `chat.go:29-30` accumulates into a `strings.Builder` printed once at `:49`, so **all** narration lands after all tool output. `classicStreamWriter.RevokeStream` is a no-op (`classic_agent_ui.go:52`) — the comment claims classic terminals cannot erase written text, but in one-shot mode the text is still in a buffer and *could* be. Nothing forces a boundary flush.

The TUI is unaffected: it uses whole-string `RenderMarkdown`, which flushes (`markdown.go:113-120`).

**Fix.** Flush the markdown writer and emit a paragraph break at each message boundary — i.e. where `revokeStreamWriter` is already called (`loop.go:281`) and at turn end.

---

## B8 — Stale run re-announced on every startup

CONFIRMED · low

`internal/ledger/storage_recovery.go:20-47`. The doc comment says `Recover` "marks any run with a non-terminal status as interrupted"; it writes **nothing** — it is read-only with respect to run status, and only clears claims on already-terminal runs.

`run-1` has been stuck at 4 `queued` tasks since 2026-07-28 19:15 and prints `info: recovered interrupted run run-1 (run-1)` on every single CLI start, forever. Of 92 runs in the store it is the only genuinely stuck one (derived from task events: 214 completed, 14 timed_out, 4 queued, 1 failed). `/resume` therefore offers a two-day-dead run.

**Fix.** Persist an `interrupted`/`abandoned` transition in `Recover`, or reap non-terminal runs older than a threshold. Correct the doc comment either way.

---

## Refuted — do not re-chase

- **`max_steps`.** `.mivia/mivia.toml` sets `max_steps = 1000` (under `[chat]`, despite sitting inside the TOOLS comment block); the longest observed turn was ~75 steps. Hitting the cap is not silent either — `loop.go:178-180` returns an error rendered in red.
- **Error swallowing between loop and UI.** None. `Run` → `sendAgent` → `bridge.Finish(err)` → `finishStream`/`appendTurnFooter` renders `error: …`. Every silent stop has `err == nil`, which is why B2/B4 are the live suspects.
- **Non-blocking channel drops.** `UIAdapter.HandleEvent` (`ui_adapter.go:71-75`) does drop non-critical events on a full 512 buffer, but `applyEvent` handles only `KindStep`/`KindSubagentHeartbeat`/`KindError`/`KindTurnEnd` and explicitly ignores `KindAssistant` and tool kinds — the bridge owns content. Drops cost progress text, never a message. Latent only. `streamBridge.signal()` is a coalescing 1-slot notify whose payload lives in a `strings.Builder` drained wholesale, plus `pollCmd` has an unconditional 80ms fallback tick.
- **Post-`Finish` write drop / the 512KB truncating ring.** The `(b.done && b.turnID > 0)` guards (`tui_stream.go:78,122,146,179`) are unreachable: `SetTurnID`/`FenceTurn` have zero production callers, so `turnID` is always 0. Dead guard, not a drop. (Worth either wiring or deleting.)
- **Unflushed final buffer.** `finishStream` commits `streamBuf` unconditionally, and `drainBridgeAndMaybeFinish` calls `updateFromDrain` first; `Drain` reads `pending` and `done` under one lock.
- **`ResetStream` clobbering the answer.** `updateFromDrain` applies `d.ResetStream` before appending `d.Stream`.
- **Block overwrite / index reuse.** `appendBlock` always appends; `maxBlocks = 1000` truncation drops the *oldest*. No last-block mutation on the assistant path.
- **`internal/events/bus.go`.** `Publish` deep-copies the handler slice under `RLock` before dispatch, so the unsubscribe-during-fan-out race is handled. No buffering, no drops. (Note it is synchronous on the agent worker, so a stalled UI blocks up to `criticalSendTimeout` = 5s per TurnEnd/Error.)
- **Session directory layout.** See Evidence above — refuted.
- **Context pruning.** `pruneHistory` would shrink and persist history, but `DefaultMaxContextTokens = 1_000_000` (`session.go:83`) vs a real ~128k window means it never fires. Worth a separate ticket for model-aware budgeting.
- **`hydrateHistory`'s 100-message window.** A lazy window, not loss: `loadMoreMessages` (`tui.go:363-398`) prepends on scroll-to-top.
- **Subagent timeouts.** Every site guards on `timeout > 0`, so an unset `default_timeout_seconds` means unlimited, not instant. The 14 `timed_out` tasks came from runs with an explicit timeout.
- **`hang_regression_integration_test.go`.** Guards tool-level hangs only (`DefaultToolTimeout = 60s`). There is no watchdog on the model call — only the request context deadline.
- **Short-interim drop.** `minInterimRunes = 8` (`tool_verbs.go:288`) silently discards interim speech under 8 runes, but this is deliberate and pinned by tests.

Also noted, not defects: `DefaultRequestTimeout = 300s` (`session.go:85`) is unreachable because `http.Client.Timeout = 180s` (`openai_compat.go:60-64`) spans headers *and* body — set the client timeout to 0 and rely on the per-request context deadline. An undecodable SSE chunk is silently discarded (`openai_compat_stream.go:132-135`), so provider shape-drift degrades into B2's silent empty turn; count skipped lines and error instead. `beginNewSession` (`welcome.go:218-231`) reuses `SaveManager.turnSaveName`, so a second conversation in the same process overwrites the first's `__last___turn_*` snapshot. `streamBridge.PushThinking` and `forceSendQueued` are dead code.

## Suggested fix order

1. **B1** — one KeyMap change, and it is almost certainly the message the user actually missed.
2. **B2** (with the `FinishReason` branch) — kills the dominant "it stops" symptom and makes everything else diagnosable.
3. **B3** — restores the answer on force-send.
4. **B4** — removes the second "answer exists but is invisible" path.
5. **B5** — makes cancelled turns durable and stops the model repeating itself.
6. **B6**, **B7**, **B8** — hygiene.
