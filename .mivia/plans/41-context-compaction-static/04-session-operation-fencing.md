# 41.04 — Session operation fencing

Status: ready after phase `03` review.

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

Publication rules: a prepared context commits only when an operation epoch,
session revision,
durable revision, turn ID, model generation, source range, and idempotency key
match its token. Provider failure, summary failure, cancellation, stale turn,
clear, load, switch, or persistence failure discards the preparation and emits
no autosave. A successful provider response with failed durable publication
returns a typed persistence error and leaves the pre-turn state recoverable.
The same epoch/revision fence covers prompt-budget changes, agent-surface
changes, and legacy autosave. JSONL is read/import/export compatibility only;
context-enabled turns cannot fall back to a raw JSONL write.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 04-BOOT-001 | 1 | bootstrap | `internal/chat/fencing.go` | compile-safe operation-token seam only; depends 03-REVIEW-003; `go test ./internal/chat`; 60s; `internal/chat/fencing.go`, `internal/chat/session.go`, `internal/contextstate/contracts.go` |
| 04-RED-001 | 1 | RED | `internal/chat/clear_race_test.go` | `TestLoadCannotResurrectAfterClear`; depends 04-BOOT-001; `go test -run '^TestLoadCannotResurrectAfterClear$' ./internal/chat`; 120s; `internal/chat/clear_race_test.go`, `internal/chat/persistence.go`, `internal/chat/session.go`, `internal/contextstate/contracts.go` |
| 04-GREEN-001 | 2 | GREEN | `internal/chat/persistence.go` | `publishLoadedSession`; depends 04-RED-001; `go test -run '^TestLoadCannotResurrectAfterClear$' ./internal/chat`; 120s; `internal/chat/persistence.go`, `internal/chat/clear_race_test.go`, `internal/chat/session.go`, `internal/contextstate/contracts.go` |
| 04-RED-002 | 3 | RED | `internal/chat/model_binding_integration_test.go` | `TestLoadCannotOverwriteModelSwitch`; depends 04-GREEN-001; `go test -run '^TestLoadCannotOverwriteModelSwitch$' ./internal/chat`; 120s; `internal/chat/model_binding_integration_test.go`, `internal/chat/persistence.go`, `internal/chat/binding.go`, `internal/contextstate/contracts.go` |
| 04-GREEN-002 | 4 | GREEN | `internal/chat/binding.go` | `captureBindingRevision`; depends 04-RED-002; `go test -run '^TestLoadCannotOverwriteModelSwitch$' ./internal/chat`; 120s; `internal/chat/binding.go`, `internal/chat/model_binding_integration_test.go`, `internal/chat/persistence.go`, `internal/contextstate/contracts.go` |
| 04-REVIEW-001 | 5 | review | `internal/chat/binding.go` | Binding/load review; depends 04-GREEN-002; `go test ./internal/chat`; 120s; `internal/chat/binding.go`, `internal/chat/persistence.go`, `internal/chat/model_binding_integration_test.go`, `internal/contextstate/contracts.go` |
| 04-RED-003 | 6 | RED | `internal/chat/save_manager_test.go` | `TestOlderAutosaveCannotOverwriteNewerRevision`; depends 04-REVIEW-001; `go test -run '^TestOlderAutosaveCannotOverwriteNewerRevision$' ./internal/chat`; 120s; `internal/chat/save_manager_test.go`, `internal/chat/save_manager.go`, `internal/chat/persistence.go`, `internal/contextstate/contracts.go` |
| 04-GREEN-003 | 7 | GREEN | `internal/chat/save_manager.go` | `SaveAfterTurnWithRevision`; depends 04-RED-003; `go test -run '^TestOlderAutosaveCannotOverwriteNewerRevision$' ./internal/chat`; 120s; `internal/chat/save_manager.go`, `internal/chat/save_manager_test.go`, `internal/chat/persistence.go`, `internal/contextstate/contracts.go` |
| 04-RED-004 | 8 | RED | `internal/chat/session_test.go` | `TestPreparedTurnDiscardedAfterClear`; depends 04-GREEN-003; `go test -run '^TestPreparedTurnDiscardedAfterClear$' ./internal/chat`; 120s; `internal/chat/session_test.go`, `internal/chat/session.go`, `internal/chat/save_manager.go`, `internal/contextstate/contracts.go` |
| 04-GREEN-004 | 9 | GREEN | `internal/chat/session.go` | `commitPreparedTurn`; depends 04-RED-004; `go test -run '^TestPreparedTurnDiscardedAfterClear$' ./internal/chat`; 120s; `internal/chat/session.go`, `internal/chat/session_test.go`, `internal/chat/save_manager.go`, `internal/contextstate/contracts.go` |
| 04-REVIEW-002 | 10 | review | `internal/chat/session.go` | Full state-transition review; depends 04-GREEN-004; `go test -race -count=5 ./internal/chat`; 240s; `internal/chat/session.go`, `internal/chat/persistence.go`, `internal/chat/save_manager.go`, `internal/chat/binding.go`, `internal/chat/session_test.go` |

Gate: all stale-operation tests pass before any compaction behavior is enabled.
