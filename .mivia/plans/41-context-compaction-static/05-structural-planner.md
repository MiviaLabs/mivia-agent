# 41.05 — Structural planner (no behavior flip)

Status: ready after phase `04` review.

Goal: implement a pure planner that can be tested independently but cannot alter
production behavior until phase 06 is complete.

Planner accounting uses one conservative request-cost function shared by
planner, plain chat, root loops, and one-shot handlers. It includes message
framing, role/name/tool IDs, tool-call arguments, registered tool schemas, and
the output reserve; exact-boundary cost is accepted and one token over is
rejected. An oversized current objective is a local deterministic error.

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

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 05-RED-001 | 1 | RED | `internal/contextmgr/planner_test.go` | `TestPlanThresholdAndTarget`; depends 04-REVIEW-002; `go test -run '^TestPlanThresholdAndTarget$' ./internal/contextmgr`; 60s; `internal/contextmgr/planner.go`, `internal/contextmgr/planner_test.go`, `internal/contextmgr/contracts.go` |
| 05-GREEN-001 | 2 | GREEN | `internal/contextmgr/planner.go` | `Plan`; depends 05-RED-001; `go test -run '^TestPlannerPreservesStructure$' ./internal/contextmgr`; 60s; `internal/contextmgr/planner.go`, `internal/contextmgr/planner_test.go`, `internal/contextmgr/contracts.go` |
| 05-RED-002 | 3 | RED | `internal/contextmgr/planner_test.go` | `TestPlanRejectsInvalidToolShapes`; depends 05-GREEN-001; `go test -run '^TestPlanRejectsInvalidToolShapes$' ./internal/contextmgr`; 60s; `internal/contextmgr/planner.go`, `internal/contextmgr/planner_test.go`, `internal/contextmgr/contracts.go` |
| 05-GREEN-002 | 4 | GREEN | `internal/contextmgr/planner.go` | `validateMessageShape`; depends 05-RED-002; `go test -run '^TestPlanRejectsInvalidToolShapes$' ./internal/contextmgr`; 60s; `internal/contextmgr/planner.go`, `internal/contextmgr/planner_test.go`, `internal/contextmgr/contracts.go` |
| 05-RED-003 | 5 | RED | `internal/provider/context_test.go` | `TestEstimatorRetainsPairingCompatibility`; depends 05-GREEN-002; `go test -run '^TestEstimatorRetainsPairingCompatibility$' ./internal/provider`; 60s; `internal/provider/context.go`, `internal/provider/context_test.go`, `internal/contextmgr/planner.go` |
| 05-GREEN-003 | 6 | GREEN | `internal/provider/context.go` | `ValidateToolPairing`; depends 05-RED-003; `go test -run '^TestEstimatorRetainsPairingCompatibility$' ./internal/provider`; 60s; `internal/provider/context.go`, `internal/provider/context_test.go`, `internal/contextmgr/planner.go` |
| 05-REVIEW-001 | 7 | review | `internal/contextmgr/planner.go` | Pure/no-side-effect review; depends 05-GREEN-003; `go test ./internal/contextmgr ./internal/provider`; 120s; `internal/contextmgr/planner.go`, `internal/contextmgr/planner_test.go`, `internal/provider/context.go`, `internal/provider/context_test.go` |

Gate: planner tests pass; current runtime still uses old pruning because the
feature gate remains disabled.
