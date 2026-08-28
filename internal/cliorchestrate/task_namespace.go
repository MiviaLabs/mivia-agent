package cliorchestrate

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// globalDispatchFallback is the LAST-RESORT namespace source, shared by
// every dispatchTasksTool instance in the process rather than kept per
// instance: a nested dispatch_tasks call (a subagent's own tool
// registry, its own *dispatchTasksTool) runs through the SAME shared
// ledger/coordinator as the outer call that spawned it. Two SEPARATE
// instances each counting from zero would both mint "dispatch:1" as
// their first fallback namespace and collide on that string as an
// idempotency key against the one ledger they share - a real, reproduced
// failure ("idempotency key already used for a different request") in
// nested_dispatch_integration_test.go before this was made package-wide.
var globalDispatchFallback atomic.Uint64

// dispatchNamespace returns the harness-unique prefix one dispatch_tasks
// call mints its tasks' real IDs under - Pool.validate only rejects a
// duplicate raw id within ONE call, with no memory of a prior, separate
// dispatch_tasks invocation, so a model reusing a raw id across two
// dispatches would otherwise silently collide.
//
// Three sources, in priority order:
//
//  1. runtime.TaskIdentityFrom(ctx): a NESTED call (a subagent dispatching
//     further subagents) - contextForTask already stamped this task's own
//     RunID/TaskID, and toolcallctx never propagates into nested execution.
//  2. toolcallctx.ToolCallFromContext(ctx): a top-level, model-made call -
//     the UI derives the same namespaced id from the same ToolCallID (see
//     dispatchTaskIDs, internal/ui/screen/conversation/events.go).
//  3. globalDispatchFallback: neither value is present (a caller outside
//     the SDK dispatch path, or a unit test constructing the tool
//     directly).
//
// This value also doubles as the run's idempotency key in place of a
// model-supplied one (idempotency_key is deliberately not in the model-
// facing schema - a model cannot construct a value stable across a retry
// but distinct from every other call): a provider-level retry of the same
// turn replays the same ToolCallID (or resumes into the same task), so
// the idempotency check still dedupes correctly.
func (t *dispatchTasksTool) dispatchNamespace(ctx context.Context) string {
	if id, ok := runtime.TaskIdentityFrom(ctx); ok {
		return id.RunID + ":" + id.TaskID
	}
	if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok && tc.ID != "" {
		return tc.ID
	}
	return fmt.Sprintf("dispatch:%d", globalDispatchFallback.Add(1))
}

// namespacedTaskID prefixes a raw model-supplied task id with namespace,
// so the harness-real id is unique across every dispatch_tasks call ever
// made, not just within one batch. An empty namespace or empty rawID
// passes rawID through unchanged - the former only in the fallback-less
// unit tests that construct tasks directly without going through
// Execute, the latter because an empty id is a validation error the
// caller already rejects elsewhere.
func namespacedTaskID(namespace, rawID string) string {
	if namespace == "" || rawID == "" {
		return rawID
	}
	return namespace + ":" + rawID
}

// namespacedDependsOn applies namespacedTaskID to every entry of a raw
// depends_on list. A nil list passes through as nil rather than an empty
// slice, so a task with no dependencies keeps comparing equal to its
// zero value in existing tests and JSON omitempty behavior.
func namespacedDependsOn(namespace string, rawDeps []string) []string {
	if len(rawDeps) == 0 {
		return rawDeps
	}
	out := make([]string, len(rawDeps))
	for i, dep := range rawDeps {
		out[i] = namespacedTaskID(namespace, dep)
	}
	return out
}

// stripNamespace removes namespace's prefix from every real task id
// embedded in s, so the model never sees the harness-internal namespaced
// form - only the raw id it wrote itself. s is the tool's own model-
// visible output (a JSON result body, or an error message's text): every
// task id inside it was minted as namespace+":"+rawID (namespacedTaskID),
// so a literal replace of that exact substring recovers the raw id
// everywhere it appears - the run-level result envelope's per-task
// "task_id" fields, a "missing dependency %q" validation error, a
// "task %q panicked" error, a "dependency %s failed" blocked-task error.
// namespace itself may contain colons (the nested-task-identity source in
// dispatchNamespace is "RunID:TaskID"), which is safe: ReplaceAll matches
// the whole literal string, not a delimiter-aware split. An empty
// namespace is a no-op, not a match-everything wildcard.
func stripNamespace(namespace, s string) string {
	if namespace == "" {
		return s
	}
	return strings.ReplaceAll(s, namespace+":", "")
}

// modelVisibleTaskID returns the id the model should see for a task: its
// raw, model-supplied id (subagents.Task.RawID, carried onto
// ledger.TaskSnapshot.RawID) when known, or the real - possibly
// namespaced - id verbatim otherwise. A task built outside dispatch_tasks
// (spawn_agent, which has no live backend left, or a task resumed from a
// ledger snapshot recorded before RawID existed) never had a namespace to
// recover in the first place, so its real id is already what the model
// gave it.
func modelVisibleTaskID(snap ledger.TaskSnapshot) string {
	if snap.RawID != "" {
		return snap.RawID
	}
	return snap.TaskID
}

// taskRawIDByID finds the task in tasks whose real id (as both
// subagents.Result.TaskID and ledger.TaskSnapshot.TaskID report it) is
// taskID, and returns its model-visible id - or taskID itself verbatim
// when no match exists (an id from outside this run, or a run with no
// snapshot data at all, must stay visible rather than disappear).
func taskRawIDByID(tasks []ledger.TaskSnapshot, taskID string) string {
	for _, t := range tasks {
		if t.TaskID == taskID {
			return modelVisibleTaskID(t)
		}
	}
	return taskID
}

// ModelVisibleTaskID is taskRawIDByID's exported form, for callers outside
// this package that report on an already-admitted run's tasks -
// internal/clichat's run_messages tool, which only has a run_id and the
// run's own task list, the same shape join_run/inspect_agents use
// in-package.
func ModelVisibleTaskID(tasks []ledger.TaskSnapshot, taskID string) string {
	return taskRawIDByID(tasks, taskID)
}

// ResolveTaskID is ModelVisibleTaskID's inverse: it maps a raw,
// model-supplied task id back to the task's real (possibly namespaced) id,
// for callers that need to ACT on a task the model named by its raw id -
// internal/clichat's send_to_task tool. rawID passes through unchanged
// when no task in tasks matches it (an unresolvable id still surfaces the
// same not-found/mailbox-miss error the caller already gives, rather than
// a new, different failure shape).
func ResolveTaskID(tasks []ledger.TaskSnapshot, rawID string) string {
	for _, t := range tasks {
		if t.RawID == rawID || t.TaskID == rawID {
			return t.TaskID
		}
	}
	return rawID
}
