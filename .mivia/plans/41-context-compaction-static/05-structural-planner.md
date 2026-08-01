# 41.05 — Structural planner (no behavior flip)

Status: blocked pending `04`.

Goal: implement a pure planner that can be tested independently but cannot alter
production behavior until phase 06 is complete.

Exact scope:

- `internal/contextmgr/planner.go` and `_test.go`: threshold/target math,
  retention, source-range assignment, idempotency key, and mandatory overflow.
- `internal/provider/context.go` and `_test.go`: preserve estimator and pairing
  helpers; planner consumes validated shapes rather than repairing silently.

Supported message shapes must be explicit: one system prompt, user messages,
assistant content, assistant tool calls, matching tool results, and bounded
recent tail. Reject duplicate tool-call IDs, multiple results for one ID,
orphan results, unterminated calls, id-less tool results, malformed assistant
messages, and any unsupported role. Never silently repair planner input.

ADLC micro-tasks:

| Wave | Type | File | Task / verification |
|---|---|---|---|
| 1 | RED | `internal/contextmgr/planner_test.go` | Threshold, exact target, pairing-shape, idempotence, and overflow tests. |
| 2 | GREEN | `internal/contextmgr/planner.go` | Implement pure plan function and immutable candidate. |
| 2 | RED | `internal/provider/context_test.go` | Regression tests for pairing-safe estimator compatibility. |
| 3 | GREEN | `internal/provider/context.go` | Expose only required validation/estimation helpers. |
| 4 | review | contextmgr/provider tests | Confirm no provider call, persistence write, or behavior flip. |

Gate: planner tests pass; current runtime still uses old pruning because the
feature gate remains disabled.
