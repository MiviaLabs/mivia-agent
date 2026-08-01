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

ADLC micro-tasks:

| ID | Wave | Type | File | Test/function, dependency, command, timeout, context |
|---|---|---|---|---|
| 07-RED-001 | 1 | RED | `internal/chat/session_test.go` | `TestPlainTurnUsesPreparationTransaction`; depends 06-REVIEW-002; `go test -run '^TestPlainTurnUsesPreparationTransaction$' ./internal/chat`; 120s; session.go, session_test.go, contextstate/contracts.go, contextmgr/contracts.go |
| 07-GREEN-001 | 2 | GREEN | `internal/chat/session.go` | `sendPlain`; depends 07-RED-001; same command; 120s; session.go, session_test.go, contextstate/contracts.go, contextmgr/contracts.go |
| 07-RED-002 | 2 | RED | `internal/agent/loop_test.go` | `TestAgentTurnDiscardsFailedPreparation`; depends 07-GREEN-001; `go test -run '^TestAgentTurnDiscardsFailedPreparation$' ./internal/agent`; 120s; loop.go, loop_test.go, contextmgr/contracts.go, contextstate/contracts.go |
| 07-GREEN-002 | 3 | GREEN | `internal/agent/loop.go` | `runStep`; depends 07-RED-002; same command; 120s; loop.go, loop_test.go, contextmgr/contracts.go, contextstate/contracts.go |
| 07-REVIEW-001 | 4 | review | `internal/agent/loop.go` | Root-loop review; depends 07-GREEN-002; `go test -race ./internal/agent`; 180s; loop.go, loop_test.go, contextmgr/contracts.go, contextstate/contracts.go |
| 07-RED-003 | 4 | RED | `internal/subagents/multi_step_test.go` | `TestMultiStepHasNoCheckpointCapability`; depends 07-REVIEW-001; `go test -run '^TestMultiStepHasNoCheckpointCapability$' ./internal/subagents`; 120s; multi_step.go, multi_step_test.go, contextmgr/contracts.go, contextstate/contracts.go |
| 07-GREEN-003 | 5 | GREEN | `internal/subagents/multi_step.go` | `newScopedLoop`; depends 07-RED-003; same command; 120s; multi_step.go, multi_step_test.go, contextmgr/contracts.go, contextstate/contracts.go |
| 07-RED-004 | 5 | RED | `internal/subagents/oneshot_test.go` | `TestOneShotRejectsIrreduciblePrompt`; depends 07-GREEN-003; `go test -run '^TestOneShotRejectsIrreduciblePrompt$' ./internal/subagents`; 60s; oneshot.go, oneshot_test.go, contextmgr/contracts.go |
| 07-GREEN-004 | 6 | GREEN | `internal/subagents/oneshot.go` | `Invoke`; depends 07-RED-004; same command; 60s; oneshot.go, oneshot_test.go, contextmgr/contracts.go |
| 07-REVIEW-002 | 7 | review | `internal/subagents/multi_step.go` | Nested capability review; depends 07-GREEN-004; `go test -race ./internal/subagents`; 180s; multi_step.go, multi_step_test.go, oneshot.go, oneshot_test.go, contextmgr/contracts.go |
| 07-RED-005 | 7 | RED | `internal/cli/dispatcher_opts_test.go` | `TestDispatcherInjectsIsolatedContextManager`; depends 07-REVIEW-002; `go test -run '^TestDispatcherInjectsIsolatedContextManager$' ./internal/cli`; 120s; dispatcher.go, dispatcher_opts_test.go, contextmgr/contracts.go |
| 07-GREEN-005 | 8 | GREEN | `internal/cli/dispatcher.go` | `registerContextManager`; depends 07-RED-005; same command; 120s; dispatcher.go, dispatcher_opts_test.go, contextmgr/contracts.go |
| 07-REVIEW-003 | 9 | review | `internal/cli/dispatcher.go` | Wiring review; depends 07-GREEN-005; `go test -race ./internal/cli`; 240s; dispatcher.go, dispatcher_opts_test.go, contextmgr/contracts.go, subagents/multi_step.go, subagents/oneshot.go |

Required integration matrix: repeated tool-heavy compaction, plain/agent/nested
parity, model switch, clear/load, cancellation, stale completion, autosave,
provider failure, summary failure, and persistence failure. A failed gate keeps
the feature disabled and retains prune-only behavior.
