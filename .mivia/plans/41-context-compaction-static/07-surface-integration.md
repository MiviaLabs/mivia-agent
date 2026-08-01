# 41.07 — Surface integration and gated rollout

Status: blocked pending `06`; phases 06 and 07 are one user-visible landing unit.

Exact scope:

- `internal/chat/session.go`: capture one policy/revision snapshot, prepare
  plain chat, and commit only after successful current-turn publication.
- `internal/agent/loop.go`: prepare only after complete tool exchanges; keep
  loop-local candidate state isolated until commit.
- `internal/subagents/multi_step.go`: inject isolated manager with no store/root
  dispatcher; inherit captured budget and binding generation.
- `internal/subagents/oneshot.go` and `internal/cli/dispatcher.go`: enforce the
  chosen rejection-only policy for irreducible system/objective overflow.
- `internal/chat/persistence.go`: load checkpoint/source metadata and preserve
  legacy sessions through the import boundary.

ADLC micro-tasks:

| Wave | Type | File | Task / verification |
|---|---|---|---|
| 1 | RED | `internal/chat/session_test.go` | Plain-turn prepare/commit/discard and model-budget tests. |
| 2 | GREEN | `internal/chat/session.go` | Wire plain chat through gated manager. |
| 2 | RED | `internal/agent/loop_test.go` | Complete-tool-exchange, failed-provider, and stale-commit tests. |
| 3 | GREEN | `internal/agent/loop.go` | Wire agent loop through gated manager. |
| 3 | RED | `internal/subagents/*_test.go` | Multi-step isolation and one-shot rejection/authorization tests. |
| 4 | GREEN | `internal/subagents/multi_step.go` | Inject isolated manager; no persistence capability. |
| 4 | GREEN | `internal/subagents/oneshot.go` | Apply shared validation and explicit rejection policy. |
| 5 | review | affected packages | Run focused tests and `go test -race ./internal/chat ./internal/agent ./internal/subagents`. |

Required integration matrix: repeated tool-heavy compaction, plain/agent/nested
parity, model switch, clear/load, cancellation, stale completion, autosave,
provider failure, summary failure, and persistence failure. A failed gate keeps
the feature disabled and retains prune-only behavior.
