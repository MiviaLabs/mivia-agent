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
- `internal/chat/*_test.go`: deterministic barriers, not timing-only races.

Publication rules: a prepared context commits only when session revision,
durable revision, turn ID, model generation, source range, and idempotency key
match its token. Provider failure, summary failure, cancellation, stale turn,
clear, load, switch, or persistence failure discards the preparation and emits
no autosave. A successful provider response with failed durable publication
returns a typed persistence error and leaves the pre-turn state recoverable.

ADLC micro-tasks:

| Wave | Type | File | Task / verification |
|---|---|---|---|
| 1 | RED | `internal/chat/clear_race_test.go` | Barrier tests for load-vs-clear and compact-vs-clear. |
| 2 | GREEN | `internal/chat/persistence.go` | Add load publication fence. |
| 2 | RED | `internal/chat/model_binding_integration_test.go` | Assert load/compact cannot overwrite a newer model generation. |
| 3 | GREEN | `internal/chat/binding.go` | Add binding-generation CAS checks. |
| 3 | RED | `internal/chat/save_manager_test.go` | Delayed old save/autosave cannot overwrite newer revision. |
| 4 | GREEN | `internal/chat/save_manager.go` | Pass revisioned snapshots to storage CAS. |
| 5 | review | `internal/chat/*` | Run `go test -race -count=5 ./internal/chat`; reviewer checks all state transitions. |

Gate: all stale-operation tests pass before any compaction behavior is enabled.
