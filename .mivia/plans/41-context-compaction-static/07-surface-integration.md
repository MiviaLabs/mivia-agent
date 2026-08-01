# 41.07 — Surface integration and gated rollout

Status: ready after phase `06` review; phases 06 and 07 are one user-visible landing unit.

Exact scope:

- `internal/chat/session.go`: capture one policy/revision snapshot, prepare
  plain chat, and commit only after successful current-turn publication.
- `internal/agent/loop.go`: prepare only after complete tool exchanges; keep
  loop-local candidate state isolated until commit.
- `internal/subagents/multi_step.go`: inject isolated manager with no store/root
  dispatcher; inherit captured budget and binding generation.
- `internal/subagents/oneshot.go` and `internal/cli/dispatcher.go`: enforce the
  chosen rejection-only policy for irreducible system/objective overflow.

Exact SQLite ownership is fixed: `internal/cli/orchestration_state.go` opens
one `*storage.SQLite` through the existing `openDurableLedgerRepo` path and injects that same pointer into the ledger adapter and
the chat/session context store. `internal/cli/orchestration_state.go` owns the
shutdown ordering and closes it exactly once after ledger and chat stop. Ledger
and chat borrow the pointer, never open a second connection, and never close
it. The tests compare pointer identity and exercise double-close, startup
failure, and shutdown cancellation.

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 07-RED-001 | 1 | RED | `internal/chat/session_test.go` | `TestPlainTurnUsesPreparationTransaction`; depends 06-REVIEW-002; `go test -run '^TestPlainTurnUsesPreparationTransaction$' ./internal/chat`; 120s; `internal/chat/session.go`, `internal/chat/session_test.go`, `internal/contextstate/contracts.go`, `internal/contextmgr/contracts.go` |
| 07-GREEN-001 | 2 | GREEN | `internal/chat/session.go` | `sendPlain`; depends 07-RED-001; `go test -run '^TestPlainTurnUsesPreparationTransaction$' ./internal/chat`; 120s; `internal/chat/session.go`, `internal/chat/session_test.go`, `internal/contextstate/contracts.go`, `internal/contextmgr/contracts.go` |
| 07-RED-002 | 3 | RED | `internal/agent/loop_test.go` | `TestAgentTurnDiscardsFailedPreparation`; depends 07-GREEN-001; `go test -run '^TestAgentTurnDiscardsFailedPreparation$' ./internal/agent`; 120s; `internal/agent/loop.go`, `internal/agent/loop_test.go`, `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 07-GREEN-002 | 4 | GREEN | `internal/agent/loop.go` | `runStep`; depends 07-RED-002; `go test -run '^TestAgentTurnDiscardsFailedPreparation$' ./internal/agent`; 120s; `internal/agent/loop.go`, `internal/agent/loop_test.go`, `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 07-REVIEW-001 | 5 | review | `internal/agent/loop.go` | Root-loop review; depends 07-GREEN-002; `go test -race ./internal/agent`; 180s; `internal/agent/loop.go`, `internal/agent/loop_test.go`, `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 07-RED-003 | 6 | RED | `internal/subagents/multi_step_test.go` | `TestMultiStepHasNoCheckpointCapability`; depends 07-REVIEW-001; `go test -run '^TestMultiStepHasNoCheckpointCapability$' ./internal/subagents`; 120s; `internal/subagents/multi_step.go`, `internal/subagents/multi_step_test.go`, `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 07-GREEN-003 | 7 | GREEN | `internal/subagents/multi_step.go` | `newScopedLoop`; depends 07-RED-003; `go test -run '^TestMultiStepHasNoCheckpointCapability$' ./internal/subagents`; 120s; `internal/subagents/multi_step.go`, `internal/subagents/multi_step_test.go`, `internal/contextmgr/contracts.go`, `internal/contextstate/contracts.go` |
| 07-RED-004 | 8 | RED | `internal/subagents/oneshot_test.go` | `TestOneShotRejectsIrreduciblePrompt`; depends 07-GREEN-003; `go test -run '^TestOneShotRejectsIrreduciblePrompt$' ./internal/subagents`; 60s; `internal/subagents/oneshot.go`, `internal/subagents/oneshot_test.go`, `internal/contextmgr/contracts.go` |
| 07-GREEN-004 | 9 | GREEN | `internal/subagents/oneshot.go` | `Invoke`; depends 07-RED-004; `go test -run '^TestOneShotRejectsIrreduciblePrompt$' ./internal/subagents`; 60s; `internal/subagents/oneshot.go`, `internal/subagents/oneshot_test.go`, `internal/contextmgr/contracts.go` |
| 07-REVIEW-002 | 10 | review | `internal/subagents/multi_step.go` | Nested capability review; depends 07-GREEN-004; `go test -race ./internal/subagents`; 180s; `internal/subagents/multi_step.go`, `internal/subagents/multi_step_test.go`, `internal/subagents/oneshot.go`, `internal/subagents/oneshot_test.go`, `internal/contextmgr/contracts.go` |
| 07-RED-005 | 11 | RED | `internal/cli/dispatcher_opts_test.go` | `TestDispatcherInjectsIsolatedContextManager`; depends 07-REVIEW-002; `go test -run '^TestDispatcherInjectsIsolatedContextManager$' ./internal/cli`; 120s; `internal/cli/dispatcher.go`, `internal/cli/dispatcher_opts_test.go`, `internal/contextmgr/contracts.go` |
| 07-GREEN-005 | 12 | GREEN | `internal/cli/dispatcher.go` | `registerContextManager`; depends 07-RED-005; `go test -run '^TestDispatcherInjectsIsolatedContextManager$' ./internal/cli`; 120s; `internal/cli/dispatcher.go`, `internal/cli/dispatcher_opts_test.go`, `internal/contextmgr/contracts.go` |
| 07-REVIEW-003 | 13 | review | `internal/cli/dispatcher.go` | Wiring review; depends 07-GREEN-005; `go test -race ./internal/cli`; 240s; `internal/cli/dispatcher.go`, `internal/cli/dispatcher_opts_test.go`, `internal/contextmgr/contracts.go`, `internal/subagents/multi_step.go`, `internal/subagents/oneshot.go` |
| 07-RED-006 | 14 | RED | `internal/cli/orchestration_state_test.go` | `TestSharedSQLiteInjectedIntoChatAndLedger`; depends 07-REVIEW-003; `go test -run '^TestSharedSQLiteInjectedIntoChatAndLedger$' ./internal/cli`; 120s; `internal/cli/orchestration_state.go`, `internal/cli/orchestration_state_test.go`, `internal/chat/session.go`, `internal/ledger/storage.go` |
| 07-GREEN-006 | 15 | GREEN | `internal/cli/orchestration_state.go` | `openSharedSQLite`; depends 07-RED-006; `go test -run '^TestSharedSQLiteInjectedIntoChatAndLedger$' ./internal/cli`; 120s; `internal/cli/orchestration_state.go`, `internal/cli/orchestration_state_test.go`, `internal/chat/session.go`, `internal/ledger/storage.go` |
| 07-RED-007 | 16 | RED | `internal/cli/orchestration_state_test.go` | `TestOrchestrationStateClosesSharedSQLiteOnce`; depends 07-GREEN-006; `go test -run '^TestOrchestrationStateClosesSharedSQLiteOnce$' ./internal/cli`; 120s; `internal/cli/orchestration_state.go`, `internal/cli/orchestration_state_test.go`, `internal/cli/open_durable_ledger.go` |
| 07-GREEN-007 | 17 | GREEN | `internal/cli/orchestration_state.go` | `closeSharedSQLite`; depends 07-RED-007; `go test -run '^TestOrchestrationStateClosesSharedSQLiteOnce$' ./internal/cli`; 120s; `internal/cli/orchestration_state.go`, `internal/cli/orchestration_state_test.go`, `internal/cli/open_durable_ledger.go` |
| 07-REVIEW-004 | 18 | review | `internal/cli/orchestration_state.go` | shared SQLite ownership review; depends 07-GREEN-007; `go test -race ./internal/cli`; 240s; `internal/cli/orchestration_state.go`, `internal/cli/orchestration_state_test.go`, `internal/chat/session.go`, `internal/ledger/storage.go` |

Required integration matrix: repeated tool-heavy compaction, plain/agent/nested
parity, model switch, clear/load, cancellation, stale completion, autosave,
provider failure, summary failure, and persistence failure. A failed gate keeps
the feature disabled and retains prune-only behavior.
