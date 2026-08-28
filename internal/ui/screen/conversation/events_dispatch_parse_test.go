package conversation

import "testing"

// TestDispatchTaskIDsAndNamesGuards pins the early-exit and fallback arms of
// the sidebar row extractor: a non-dispatch tool, a missing task list, and
// an empty task list all yield no rows, and a task the model left unnamed
// falls back to its positional placeholder while named ones get namespaced.
func TestDispatchTaskIDsAndNamesGuards(t *testing.T) {
	if ids, names := dispatchTaskIDsAndNames("call-1", "read_file", map[string]any{"tasks": []any{}}); ids != nil || names != nil {
		t.Errorf("non-dispatch tool = (%v, %v), want (nil, nil)", ids, names)
	}
	if ids, names := dispatchTaskIDsAndNames("call-1", "dispatch_tasks", map[string]any{}); ids != nil || names != nil {
		t.Errorf("missing tasks key = (%v, %v), want (nil, nil)", ids, names)
	}
	if ids, names := dispatchTaskIDsAndNames("call-1", "dispatch_tasks", map[string]any{"tasks": []any{}}); ids != nil || names != nil {
		t.Errorf("empty tasks = (%v, %v), want (nil, nil)", ids, names)
	}

	ids, names := dispatchTaskIDsAndNames("call-1", "dispatch_tasks", map[string]any{"tasks": []any{
		map[string]any{"prompt": "no id here"},
		map[string]any{"id": "a", "prompt": "named"},
	}})
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want 2 entries", ids)
	}
	if ids[0] != "task-1" {
		t.Errorf("unnamed task placeholder = %q, want task-1", ids[0])
	}
	if ids[1] != "call-1:a" {
		t.Errorf("named task id = %q, want call-1:a", ids[1])
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want none without display names", names)
	}
}

// TestNamespacedTaskIDEmptyArms pins the passthrough: an empty namespace or
// an empty raw id returns the raw id unchanged instead of a dangling ":".
func TestNamespacedTaskIDEmptyArms(t *testing.T) {
	if got := namespacedTaskID("", "a"); got != "a" {
		t.Errorf("namespacedTaskID(empty ns) = %q, want a", got)
	}
	if got := namespacedTaskID("call-1", ""); got != "" {
		t.Errorf("namespacedTaskID(empty id) = %q, want empty", got)
	}
	if got := namespacedTaskID("call-1", "a"); got != "call-1:a" {
		t.Errorf("namespacedTaskID = %q, want call-1:a", got)
	}
}

// TestParseDispatchTaskStatusesShapes pins all three accepted result shapes
// and the empty-out exit: the bare array, the wrapped tasks envelope, the
// task_results envelope (wait="none"/"task"), the id-fallback for rows
// without task_id, and rows that carry no status at all.
func TestParseDispatchTaskStatusesShapes(t *testing.T) {
	if got := parseDispatchTaskStatuses(""); got != nil {
		t.Errorf("empty result = %v, want nil", got)
	}

	got := parseDispatchTaskStatuses(`{"task_results":[{"task_id":"ta-1","status":"completed"}]}`)
	if got["ta-1"] != "completed" {
		t.Errorf("task_results envelope = %v, want ta-1 completed", got)
	}

	got = parseDispatchTaskStatuses(`[{"id":"ta-9","status":"running"}]`)
	if got["ta-9"] != "running" {
		t.Errorf("bare array with id only = %v, want ta-9 running", got)
	}

	if got := parseDispatchTaskStatuses(`[{"task_id":"x"},{"id":"y"}]`); got != nil {
		t.Errorf("statuses all empty = %v, want nil", got)
	}
}
