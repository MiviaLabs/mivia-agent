---
id: task_identity_two_forms
title: A task id has two forms; ledger indexes key by the FULL namespaced one
content: dispatch_tasks tasks carry a full namespaced TaskID (ledger, events, message index) and a stripped model-visible RawID; any producer lookup keyed by the stripped form compiles and silently misses - key lookups by the snapshot row's full TaskID, or read attachments off the snapshot row directly.
importance: high
tags: [orchestration, dispatch_tasks, ledger, identity, dc-11, conformance]
updated: 2026-09-04
---

# A task id has two identity forms, and indexes key by the full one

## The trap

`dispatch_tasks` mints tasks with a namespaced id (`<call_id>:<raw_id>`,
`internal/cliorchestrate/task_namespace.go`). Everything the coordinator
records - task snapshots, lifecycle events, the task-message index built by
`TaskMessageIndex` - keys by that FULL id. But the model-visible result rows
report the stripped `RawID` (`modelVisibleTaskID`). A lookup that takes its
key from a produced row instead of the snapshot compiles fine and returns
nothing, only for dispatch-created tasks, only on the paths nobody watches.

The recovered result path shipped this bug twice in one week:

- `tool_calls_ref` was looked up via `toolCallsRefFor(tasks, out[i].TaskID)`
  and silently emitted nothing (caught in plan review, fixed 0ff96f18).
- Task messages were looked up via `msgIndex[out[i].TaskID]` and recovered
  results silently dropped their findings/questions (fixed b346f9d7,
  class DC-11).

The live paths never hit it because `subagents.Result.TaskID` is the full
pre-strip id.

## What to do

1. Attachments (tool_calls_ref + messages) are written by exactly ONE
   function: `attachTaskRecord(dst, snap, msgs)` in
   `internal/cliorchestrate/dispatch_encode.go`. Route every producer
   through it; never hand-assemble those fields.
2. The message index is opaque (`TaskMessages`): the only read is
   `ForSnapshot(snap)`, which keys internally by the snapshot's full
   `TaskID` - a wrong-form key is unwritable, not merely tested.
3. Any NEW producer of a model-visible task result must join the
   conformance table in
   `internal/cliorchestrate/task_result_producer_conformance_test.go`
   (`TestTaskResultProducerConformance`) - it feeds every producer one
   namespaced task with recorded messages and refs; joinSalvageEnvelope
   was the straggler it caught. Pre-commit runs it for any staged
   `internal/cliorchestrate/` change.
4. Recorded trigger (do NOT build speculatively): escalate to distinct
   FullTaskID/RawTaskID types only if a wrong-form bug ever ships OUTSIDE
   internal/cliorchestrate, where this package boundary cannot reach. A
   repo-wide typed-ID refactor was reviewed 2026-08-29 and rejected -
   the model-visible TaskID holds either form by design, so casts would
   hollow out the guarantee (`ledger.TaskID` already exists unused as
   evidence of a stalled prior attempt).
