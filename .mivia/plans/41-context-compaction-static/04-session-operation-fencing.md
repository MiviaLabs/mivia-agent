# 41.04 — Session operation fencing

Status: blocked pending `03`.

Goal: prevent stale asynchronous work from publishing after clear, load, model
switch, newer turns, or autosave.

Exact scope:

- `internal/chat/persistence.go`: capture operation token before I/O and require
  session revision plus binding generation at publish.
- `internal/chat/session.go`: make clear, load, model switch, turn commit, and
  autosave advance/invalidate the correct revision domain.
- `internal/chat/save_manager.go`: pass the captured revision to durable CAS;
  stale autosaves are discarded with an explicit result.
- `internal/chat/clear_race_test.go`, `internal/chat/model_binding_integration_test.go`,
  `internal/chat/save_manager_test.go`, and `internal/chat/session_test.go`:
  deterministic barriers, not timing-only races.

Publication rules: a prepared context commits only when session revision,
durable revision, turn ID, model generation, source range, and idempotency key
match its token. Provider failure, summary failure, cancellation, stale turn,
clear, load, switch, or persistence failure discards the preparation and emits
no autosave. A successful provider response with failed durable publication
returns a typed persistence error and leaves the pre-turn state recoverable.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 04-RED-001 | 1 | RED | `internal/chat/clear_race_test.go` | `TestLoadCannotResurrectAfterClear`; depends 03-REVIEW-001; `go test -run '^TestLoadCannotResurrectAfterClear$' ./internal/chat`; 120s; clear_race_test.go, persistence.go, session.go, contextstate/contracts.go |
| 04-GREEN-001 | 2 | GREEN | `internal/chat/persistence.go` | `publishLoadedSession`; depends 04-RED-001; same command; 120s; persistence.go, clear_race_test.go, session.go, contextstate/contracts.go |
| 04-RED-002 | 2 | RED | `internal/chat/model_binding_integration_test.go` | `TestLoadCannotOverwriteModelSwitch`; depends 04-GREEN-001; `go test -run '^TestLoadCannotOverwriteModelSwitch$' ./internal/chat`; 120s; model_binding_integration_test.go, persistence.go, binding.go, contextstate/contracts.go |
| 04-GREEN-002 | 3 | GREEN | `internal/chat/binding.go` | `captureBindingRevision`; depends 04-RED-002; same command; 120s; binding.go, model_binding_integration_test.go, persistence.go, contextstate/contracts.go |
| 04-REVIEW-001 | 4 | review | `internal/chat/binding.go` | Binding/load review; depends 04-GREEN-002; `go test ./internal/chat`; 120s; binding.go, persistence.go, model_binding_integration_test.go, contextstate/contracts.go |
| 04-RED-003 | 4 | RED | `internal/chat/save_manager_test.go` | `TestOlderAutosaveCannotOverwriteNewerRevision`; depends 04-REVIEW-001; `go test -run '^TestOlderAutosaveCannotOverwriteNewerRevision$' ./internal/chat`; 120s; save_manager_test.go, save_manager.go, persistence.go, contextstate/contracts.go |
| 04-GREEN-003 | 5 | GREEN | `internal/chat/save_manager.go` | `SaveAfterTurnWithRevision`; depends 04-RED-003; same command; 120s; save_manager.go, save_manager_test.go, persistence.go, contextstate/contracts.go |
| 04-RED-004 | 5 | RED | `internal/chat/session_test.go` | `TestPreparedTurnDiscardedAfterClear`; depends 04-GREEN-003; `go test -run '^TestPreparedTurnDiscardedAfterClear$' ./internal/chat`; 120s; session_test.go, session.go, save_manager.go, contextstate/contracts.go |
| 04-GREEN-004 | 6 | GREEN | `internal/chat/session.go` | `commitPreparedTurn`; depends 04-RED-004; same command; 120s; session.go, session_test.go, save_manager.go, contextstate/contracts.go |
| 04-REVIEW-002 | 7 | review | `internal/chat/session.go` | Full state-transition review; depends 04-GREEN-004; `go test -race -count=5 ./internal/chat`; 240s; session.go, persistence.go, save_manager.go, binding.go, session_test.go |

Gate: all stale-operation tests pass before any compaction behavior is enabled.
