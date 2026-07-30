# System-Level Progress Transparency Plan

## Status

Implemented on 2026-07-30. Focused, package, race, build, vet, invariant,
and documentation gates passed; the TUI smoke journey also passed.

## Goal

Make an active model call visibly update the TUI within two seconds without
requiring model-generated progress text, while preserving the existing
meaning of a stalled warning.

## Validated Current Behavior

- `internal/agent/loop.go:emitModelThinkingHeartbeat` emits an `EventStep`
  every 10 seconds while `Completer.ChatTurn` is active.
- Tool batches already emit `EventStep` progress every two seconds.
- `stepDetail` is forwarded through the stream bridge and displayed in the
  composer footer.
- The sticky status header is implemented in
  `internal/cli/brand.go:renderWorkChrome`, not in `chatblock_status.go`.
  It already renders `stepDetail` for tool and streaming phases, but
  deliberately suppresses it for `phaseThinking`.
- `updateFromDrain` only enters its stalled check after prior transcript,
  thinking, or tool data exists. It therefore cannot provide an initial
  fallback during a completely silent model call.

## Scope and Constraints

1. Reuse the existing heartbeat goroutine, stream bridge, `stepDetail`, and
   `stepDetailAt` state. Do not add a service or a second progress pipeline.
2. Keep progress host-generated. Do not depend on model prose.
3. Preserve `stalledWarning` as a warning for missing progress or errors, not
   a normal working indicator.
4. Preserve a single physical status-bar line at narrow terminal widths.
5. Do not alter tool execution, request timeouts, queue semantics, or idle
   rendering.

## Change A - Model-thinking heartbeat cadence

**Production file:** `internal/agent/loop.go`

Change `emitModelThinkingHeartbeat` from a 10-second ticker to a 2-second
ticker. Keep its existing context cancellation and `EventStep` delivery
semantics unchanged.

The event remains host-generated and continues to identify model thinking.
The exact user-facing detail may be made concise if needed to avoid repeating
the header's phase and elapsed-time fields.

**Test file:** `internal/agent/loop_test.go`

Add a focused test that starts the heartbeat with a cancellable context,
observes an `EventStep` before the legacy 10-second cadence, then cancels and
proves the test does not retain the producer. The test must not rely on a
sleep close to the two-second boundary; use a bounded observation window with
enough scheduler tolerance.

## Change B - Show thinking progress in the sticky header

**Production file:** `internal/cli/brand.go`

Update `renderWorkChrome` so a non-empty `stepDetail` is eligible for display
in `phaseThinking` as well as the phases that already render it.

Keep the existing phase, elapsed time, tool count, and queue count ordering.
The renderer must prefer dropping or truncating `stepDetail` over exceeding
the terminal width; it must never introduce a newline. Avoid showing the
same elapsed-time information twice when the detail is the periodic
model-thinking heartbeat.

Tool progress needs no new rendering path: it is already delivered to this
header through `stepDetail`. Preserve its existing live `k/n` behavior.

**Test file:** `internal/cli/brand_test.go`

Add focused assertions that:

- a thinking-phase header includes supplied progress detail at a normal width;
- long detail at narrow widths still produces one physical line with no
  control/newline characters; and
- existing tool-progress and idle-header behavior remain unchanged.

## Explicitly Deferred: Stall Threshold and Fallback

Do not change `internal/cli/tui_layout.go` in this slice.

Lowering `stallQuiet` to eight seconds and setting `stepDetail` to
`"working"` would not solve the initial silent-call path, because that path
returns before the stall check. It would also conflict with the composer,
which gives `stalledWarning` precedence and displays `"⚠ stalled"` over any
fallback detail. With the two-second model heartbeat, a healthy request has
regular progress already; a separate stalled-state design requires a
reproducible missed-heartbeat case and independent acceptance criteria.

## Dependency and TDD Plan

1. RED: add the heartbeat cadence/cancellation test in `internal/agent/loop_test.go`.
2. GREEN: change the cadence in `internal/agent/loop.go`; run the focused
   agent test.
3. RED: add thinking-detail and narrow-width header tests in
   `internal/cli/brand_test.go`.
4. GREEN: update `internal/cli/brand.go`; run the focused CLI tests.
5. Run the package suites, race coverage for the affected packages, and a
   manual TUI smoke check with a deliberately slow model call.

## Acceptance Criteria

1. During a model call that lasts longer than two seconds, the sticky header
   visibly receives host-generated progress without model text.
2. During tool batches, live `k/n` progress continues to appear.
3. A healthy request with heartbeats never gains a stalled warning merely
   because it is taking time.
4. Idle chrome is unchanged.
5. The header remains one physical line at supported narrow widths.
6. Cancelling a model call keeps the existing context-cancellation ownership
   of the heartbeat path; this slice must not delay turn completion.

## Verification Plan

```text
go test -run 'Test.*Model.*Heartbeat' ./internal/agent
go test -run 'TestRender(StatusBar|WorkChrome)' ./internal/cli
go test ./internal/agent ./internal/cli
go test -race ./internal/agent ./internal/cli
make verify
```

The final implementation report must state which of these checks actually ran
and whether the slow-call visual smoke test was performed.

## Rollback Criterion

Revert this slice if the faster heartbeat causes measurable event-loop churn,
the status header wraps or obscures essential phase/tool state, cancellation
leaves stale progress visible, or race testing exposes a synchronization
defect.
