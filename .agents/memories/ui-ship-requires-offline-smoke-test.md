---
id: ui_ship_requires_offline_smoke_test
title: UI-ship phases must include an offline smoke test, not just unit tests
content: When a phase ships user-visible behavior on the new UI (cmd/mivia-ui --demo=false real mode, the conversation screen, the Approver panel, settings ports), the implementation must include an offline-runnable smoke test that mirrors what manual acceptance does, in addition to the focused unit tests.
importance: high
tags: [ui, adlc, smoke-test, manual-acceptance, regression]
updated: 2026-09-04
---

Phase 3 (`de1d2e70 feat(cli): wire real conversation mode into cmd/mivia-ui`)
shipped with all focused unit tests green. Manual acceptance on a
workstation then surfaced two real defects that the unit tests had
missed:

- **Doubled assistant message**: a single user input produced two
  `KindTurnStart` events and two identical `KindTextEnd` blocks in
  the rendered transcript. The unit test
  `TestSend_FullTurn_EmitsTurnStartThenEnd` only checked
  `first.Kind == KindTurnStart` and `last.Kind == KindTurnEnd` —
  it never asserted that the per-kind count was exactly one, so
  duplicates slipped through.
- **Trailing empty-text error**: a malformed event reached the
  stream renderer and surfaced as a bare "  error" line. The
  renderer's `renderError` had no defensive guard for empty-text
  bodies (the same shape `TextEndBody` already guards at line 35).

The bug report from the workstation operator looked like:
```
> hi
Hi there! 👋 How can I help you today?
I'm set up to work in this workspace …
  notice  prompt cache: 2176/2231 tokens cached (97%)
  notice  estimate 1920 vs actual 2231 (ratio 1.16)
> hi                                  <- duplicate turn.start
Hi there! 👋 …                       <- duplicate text.end
I'm set up to work in this workspace …
  error                                <- trailing empty-text error
```

**Why the unit tests missed it:**

1. The test surface for `Send` used a `scriptedCompleter` with one
   canned response. Real provider behaviour (streaming deltas + final
   text.end + per-turn accounting) was not exercised.
2. The test only checked first/last kinds, not per-kind counts.
3. The renderer was tested with a recorded fixture, not a realistic
   scripted event stream that mirrors what `--demo=false` produces
   under a real completer.
4. The build-then-run gate (`go run ./cmd/mivia-ui --demo=false`)
   required live provider credentials, which the offline agent
   cannot satisfy — so the agent shipped without ever exercising
   the path the user sees.

**Lesson (encoded as rule amendment below):**

- For phases that ship user-visible behavior on the new UI, the
  implementation MUST add at least one offline-runnable smoke test
  that exercises the full event pipeline end-to-end. The smoke
  test must NOT require live provider credentials. The test must
  drive a realistic event sequence (turn.start → text.delta → text.end
  → notices → turn.end) and assert exact per-kind counts.
- The unit tests `TestSend_FullTurn_ExactlyOneOfEach` and
  `TestSend_FullTurn_TextEndContentExact` (added in this commit in
  `internal/uiadapter/conversation_test.go`) pin the per-kind counts
  on the channel side. The renderer test
  `TestRenderSmoke_RealisticOneUserInput` (added in
  `internal/ui/stream/stream_test.go`) pins the same on the
  renderer side.
- The renderer's `renderError` now early-returns on empty-text
  non-fatal `KindError` (mirror of `TextEndBody`'s guard at line
  35). This is a defensive guard, not a fix: any producer that
  emits an empty-text error is wrong, but the renderer suppresses
  the rendering rather than mis-formatting it.

**What the planner must do next time a UI-ship phase runs:**

1. Plan the smoke test in the `## Tests` section BEFORE Step 5.
   The smoke test must be offline-runnable (no live credentials).
2. Plan an end-to-end assertion on per-kind event counts, not
   just first/last. State the exact count expectations.
3. Plan a renderer-side smoke test if the plan touches a renderer,
   mirroring the channel-side test. A regression that duplicates
   at the renderer but not at the channel must surface too.
4. Plan a defensive guard on any empty-text body in the renderer
   (mirroring `TextEndBody`).

**What the reviewer must check:**

1. The smoke test is in the diff and runs in CI under
   `go test ./internal/uiadapter/...` (or whichever package the
   smoke test landed in).
2. The smoke test fails when given a doubled-event sequence.
3. The defensive guards on empty-text bodies are present and tested.

The rule amendment in `.agents/rules/05-adlc-agentic-development-lifecycle.md`
and the DC-9/DC-16 probe additions in
`.agents/quality/defect-taxonomy.md` encode this lesson as durable
policy, not just memory.