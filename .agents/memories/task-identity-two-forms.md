---
id: task_identity_two_forms
title: A task id has two forms; ledger indexes key by the FULL namespaced one
content: dispatch_tasks tasks carry a full namespaced TaskID (ledger, events, message index) and a stripped model-visible RawID; any producer lookup keyed by the stripped form compiles and silently misses - key lookups by the snapshot row's full TaskID, or read attachments off the snapshot row directly.
importance: high
tags: [orchestration, dispatch_tasks, ledger, identity, dc-11, conformance]
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

1. When you have the snapshot row in hand, read attachments off it directly
   (`task.ToolCallsRef`), never through an id-keyed lookup.
2. When you must use an index, key it with the snapshot's full `TaskID`
   (rows from `persistedTaskResults` are index-aligned with the snapshot).
3. Any NEW producer of a model-visible task result must join the
   conformance table in
   `internal/cliorchestrate/task_result_producer_conformance_test.go`
   (`TestTaskResultProducerConformance`) - it feeds every producer one
   namespaced task with recorded messages and refs and fails any producer
   that keys a lookup by the wrong identity form. Pre-commit runs it for
   any staged `internal/cliorchestrate/` change.
