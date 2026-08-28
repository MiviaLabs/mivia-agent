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

// commonTaskIDNamespace finds the namespace prefix every task in tasks
// shares, for tools that report on an already-admitted run - join_run,
// inspect_agents, run_messages - and never had a namespace value of their
// own the way dispatch_tasks' own Execute does (they only ever see a
// run_id, not the dispatch call that originally minted the run's tasks).
//
// Every task from ONE dispatch_tasks call shares the SAME namespace
// (namespacedTaskID: namespace+":"+rawID), so the longest common prefix
// across every task in the run recovers it - found byte-wise (not by
// splitting on the first or last colon) specifically so a raw model id
// that itself contains a colon does not get misread as part of the
// namespace boundary. Trimmed back to the last colon within that shared
// prefix, so a namespace ending mid-word from an accidental partial match
// never strips too much.
//
// Returns "" - a no-op for stripNamespace - when there are fewer than two
// tasks (one task alone cannot distinguish "namespace:id" from a raw id
// that happens to contain a colon) or when the tasks do not share a
// prefix at all (a run whose tasks came from more than one dispatch call
// under different namespaces - rare, and safer to leave unstripped than
// to guess wrong across mismatched namespaces).
func commonTaskIDNamespace(tasks []ledger.TaskSnapshot) string {
	if len(tasks) < 2 {
		return ""
	}
	prefix := tasks[0].TaskID
	for _, t := range tasks[1:] {
		prefix = commonStringPrefix(prefix, t.TaskID)
		if prefix == "" {
			return ""
		}
	}
	idx := strings.LastIndex(prefix, ":")
	if idx < 0 {
		return ""
	}
	return prefix[:idx]
}

// StripTaskIDNamespace is the exported form of stripNamespace/
// commonTaskIDNamespace for callers outside this package that report on an
// already-admitted run without ever seeing the dispatch call that minted
// it - internal/clichat's run_messages tool, which only has a run_id and
// the run's own task list, the same shape join_run/inspect_agents use
// in-package. See commonTaskIDNamespace's doc comment for exactly which
// runs this can and cannot recover the namespace for.
func StripTaskIDNamespace(tasks []ledger.TaskSnapshot, s string) string {
	return stripNamespace(commonTaskIDNamespace(tasks), s)
}

func commonStringPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}
