package clichat

// tool_render_helpers_coverage_test.go covers the secondary helpers in
// tool_render.go: extractDelegateArgs, extractDispatchTasksArgs,
// extractJSONStatusOutput, countJSONArray, firstLineOnly, clipOneLine,
// and summarizeDispatchTasks. Each helper is exercised against inputs
// that drive every branch documented in its implementation; outputs are
// asserted loosely because downstream callers (the renderer) format
// the final summary, not the extractor.

import (
	"strings"
	"testing"
)

func TestExtractDelegateArgs(t *testing.T) {
	// Single-task form: "task" field set, multi_step absent.
	task, multi := extractDelegateArgs(`{"task":"build feature"}`)
	if task != "build feature" || multi {
		t.Fatalf("extractDelegateArgs(single) = (%q, %v)", task, multi)
	}
	// Multi-task form: requires multi_step:true (per the JSON tag).
	task, multi = extractDelegateArgs(`{"task":"first","multi_step":true}`)
	if task != "first" || !multi {
		t.Fatalf("extractDelegateArgs(multi_step:true) = (%q, %v)", task, multi)
	}
	// Bad JSON: zero values.
	task, multi = extractDelegateArgs("not json")
	if task != "" || multi {
		t.Fatalf("extractDelegateArgs(garbage) = (%q, %v)", task, multi)
	}
}

func TestExtractDispatchTasksArgs(t *testing.T) {
	// Five tasks with prompts: n=5, first prompt is the first prompt.
	n, first := extractDispatchTasksArgs(`{"tasks":[{"prompt":"alpha"},{"prompt":"beta"},{"prompt":"gamma"},{"prompt":"delta"},{"prompt":"epsilon"}]}`)
	if n != 5 || first != "alpha" {
		t.Fatalf("extractDispatchTasksArgs(5 prompts) = (%d, %q)", n, first)
	}
	// Tasks with only IDs: n counts, first = first ID.
	n, first = extractDispatchTasksArgs(`{"tasks":[{"id":"a"},{"id":"b"}]}`)
	if n != 2 || first != "a" {
		t.Fatalf("extractDispatchTasksArgs(2 ids) = (%d, %q)", n, first)
	}
	// Empty.
	n, first = extractDispatchTasksArgs("")
	if n != 0 || first != "" {
		t.Fatalf("extractDispatchTasksArgs(empty) = (%d, %q)", n, first)
	}
}

func TestExtractJSONStatusOutput(t *testing.T) {
	status, output := extractJSONStatusOutput(`{"status":"failed","output":"boom"}`)
	if status != "failed" || output != "boom" {
		t.Fatalf("extractJSONStatusOutput = (%q, %q)", status, output)
	}
	status, _ = extractJSONStatusOutput(`{"status":"ok"}`)
	if status != "ok" {
		t.Fatalf("extractJSONStatusOutput ok = %q", status)
	}
}

func TestCountJSONArray(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want int
	}{
		{`["a","b","c"]`, 3},
		{`["one"]`, 1},
		{`[]`, 0},
		{`"single string"`, 0},
		{"", 0},
	} {
		if got := countJSONArray(tc.s); got != tc.want {
			t.Errorf("countJSONArray(%q) = %d, want %d", tc.s, got, tc.want)
		}
	}
}

func TestFirstLineOnlyAndClipOneLine(t *testing.T) {
	if got := firstLineOnly("a\nb\nc"); got != "a" {
		t.Fatalf("firstLineOnly = %q", got)
	}
	if got := firstLineOnly(""); got != "" {
		t.Fatalf("firstLineOnly(empty) = %q", got)
	}
	// clipOneLine truncates with ellipsis when input exceeds max.
	if got := clipOneLine("hello world", 5); got != "he..." {
		t.Fatalf("clipOneLine(5) = %q, want %q", got, "he...")
	}
	// Input shorter than max is returned verbatim.
	if got := clipOneLine("short", 100); got != "short" {
		t.Fatalf("clipOneLine(short) = %q", got)
	}
}

func TestSummarizeDispatchTasks(t *testing.T) {
	// No tasks, no result: "batch" sentinel.
	if got := summarizeDispatchTasks("", ""); got != "batch" {
		t.Fatalf("summarizeDispatchTasks(empty) = %q, want %q", got, "batch")
	}
	// Five tasks with prompts: "%d tasks · %s".
	got := summarizeDispatchTasks(`{"tasks":[{"prompt":"alpha"},{"prompt":"beta"},{"prompt":"gamma"},{"prompt":"delta"},{"prompt":"epsilon"}]}`, "")
	if !strings.HasPrefix(got, "5 tasks") {
		t.Fatalf("summarizeDispatchTasks(5 prompts) = %q, want prefix %q", got, "5 tasks")
	}
	// One task with prompt: "%d task · %s" (singular).
	got = summarizeDispatchTasks(`{"tasks":[{"prompt":"solo"}]}`, "")
	if !strings.HasPrefix(got, "1 task") {
		t.Fatalf("summarizeDispatchTasks(1) = %q, want prefix %q", got, "1 task")
	}
	// No tasks, non-empty result that has no JSON: clip the first line.
	got = summarizeDispatchTasks("", "free-form result line")
	if got == "" {
		t.Fatal("summarizeDispatchTasks(free result) returned empty")
	}
}
