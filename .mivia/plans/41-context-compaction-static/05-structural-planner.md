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

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 05-RED-001 | 1 | RED | `internal/contextmgr/planner_test.go` | `TestPlanThresholdAndTarget`; depends 04-GREEN-004; `go test -run '^TestPlanThresholdAndTarget$' ./internal/contextmgr`; 60s; planner.go, planner_test.go, contracts.go |
| 05-GREEN-001 | 2 | GREEN | `internal/contextmgr/planner.go` | `Plan`; depends 05-RED-001; same command; 60s; planner.go, planner_test.go, contracts.go |
| 05-RED-002 | 2 | RED | `internal/contextmgr/planner_test.go` | `TestPlanRejectsInvalidToolShapes`; depends 05-GREEN-001; `go test -run '^TestPlanRejectsInvalidToolShapes$' ./internal/contextmgr`; 60s; planner.go, planner_test.go, contracts.go |
| 05-GREEN-002 | 3 | GREEN | `internal/contextmgr/planner.go` | `validateMessageShape`; depends 05-RED-002; same command; 60s; planner.go, planner_test.go, contracts.go |
| 05-RED-003 | 3 | RED | `internal/provider/context_test.go` | `TestEstimatorRetainsPairingCompatibility`; depends 05-GREEN-002; `go test -run '^TestEstimatorRetainsPairingCompatibility$' ./internal/provider`; 60s; context.go, context_test.go, planner.go |
| 05-GREEN-003 | 4 | GREEN | `internal/provider/context.go` | `ValidateToolPairing`; depends 05-RED-003; same command; 60s; context.go, context_test.go, planner.go |
| 05-REVIEW-001 | 5 | review | `internal/contextmgr/planner.go` | Pure/no-side-effect review; depends 05-GREEN-003; `go test ./internal/contextmgr ./internal/provider`; 120s; planner.go, planner_test.go, context.go, context_test.go |

Gate: planner tests pass; current runtime still uses old pruning because the
feature gate remains disabled.
