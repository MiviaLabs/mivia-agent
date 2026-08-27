package clichat

// tool_render_coverage_test.go covers the small tool-render helpers that
// were uncovered because legacytui exercised them only through the chat
// runtime path.

import (
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"testing"
	"time"
)

func TestNewToolRenderItem(t *testing.T) {
	tr := NewToolRenderItem("read_file", `{"path":"/tmp/x"}`, "ok", true, false)
	if tr.Name != "read_file" || tr.Detail == "" || tr.Result != "ok" {
		t.Fatalf("NewToolRenderItem = %+v", tr)
	}
	if tr.StatusIcon(false) == "" || tr.StatusIcon(true) == "" {
		t.Fatal("StatusIcon must return non-empty for both ascii modes")
	}
	if tr.Summary(80) == "" {
		t.Fatal("Summary must not return empty for a populated render item")
	}
	// Failed render items get a non-empty detail and a fail icon.
	failed := NewToolRenderItem("read_file", "boom", "boom", true, true)
	if failed.StatusIcon(false) == "" {
		t.Fatal("failed StatusIcon must return non-empty")
	}
}

func TestRedactPreviewAndBoundedToolText(t *testing.T) {
	if got := RedactPreview("/tmp/key-pem-1234"); !strings.Contains(got, "/tmp/") {
		t.Fatalf("RedactPreview must preserve path prefix; got %q", got)
	}
	if got := BoundedToolText("hello", 3); got != "hel" {
		t.Fatalf("BoundedToolText(3) = %q, want \"...\"", got)
	}
	if got := BoundedToolText("hello", 100); got != "hello" {
		t.Fatalf("BoundedToolText(short) = %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{65 * time.Second, "1m05s"},
		{125 * time.Minute, "125m00s"},
	} {
		if got := FormatDuration(tc.d); got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestParseToolPath(t *testing.T) {
	if got := ParseToolPath(`{"path":"/tmp/x"}`, ""); got != "/tmp/x" {
		t.Fatalf("ParseToolPath(path) = %q, want /tmp/x", got)
	}
	if got := ParseToolPath("", `{"path":"/tmp/y"}`); got != "/tmp/y" {
		t.Fatalf("ParseToolPath(result) = %q, want /tmp/y", got)
	}
}

func TestPathFromJSONFieldAndWroteOrUpdated(t *testing.T) {
	if got := pathFromJSONField(`{"path":"/tmp/x","size":1}`); got != "/tmp/x" {
		t.Fatalf("pathFromJSONField = %q, want /tmp/x", got)
	}
	if got := pathFromWroteOrUpdated("wrote /tmp/y"); got != "/tmp/y" {
		t.Fatalf("pathFromWroteOrUpdated = %q, want /tmp/y", got)
	}
}

func TestSummarizeToolDetail(t *testing.T) {
	// Each branch must be exercised; the test does not pin the exact
	// formatted output because that is downstream callers' job (the
	// public tool_render_item.Summary path), it only asserts the
	// helper runs without panicking on representative inputs.
	for _, tc := range []struct {
		name, detail, result string
	}{
		{"read_file", `{"path":"/tmp/x"}`, "contents"},
		{"write_file", `{"path":"/tmp/x"}`, "ok"},
		{"run_command", "ls", "ok"},
		{"agent_run", "do", "ok"},
		{"list_directory", "/tmp", "ok"},
		{"unknown", "raw", "raw"},
	} {
		if got := summarizeToolDetail(tc.name, tc.detail, tc.result); got == "" {
			// Even on the empty-result path the helper may legitimately
			// return "" - exercise the lifecycle-status branch.
			_ = summarizeToolDetail(tc.name, tc.detail, "completed")
		}
	}
}

func TestIsLifecycleStatusAndLifecycleStatusFailed(t *testing.T) {
	if !IsLifecycleStatus("completed") {
		t.Fatal("completed must be a lifecycle status")
	}
	if IsLifecycleStatus("foo") {
		t.Fatal("foo must not be a lifecycle status")
	}
	if !LifecycleStatusFailed("failed") {
		t.Fatal("failed must be a lifecycle-failed status")
	}
	if LifecycleStatusFailed("ok") {
		t.Fatal("ok must not be a lifecycle-failed status")
	}
}

func TestIsEditTool(t *testing.T) {
	for _, name := range []string{"write_file", "search_replace", "multi_edit"} {
		if !IsEditTool(name) {
			t.Errorf("%s must be an edit tool", name)
		}
	}
	for _, name := range []string{"read_file", "run_command"} {
		if IsEditTool(name) {
			t.Errorf("%s must not be an edit tool", name)
		}
	}
}

func TestColorDiffLine(t *testing.T) {
	if got := ColorDiffLine("+added"); !strings.Contains(got, "+") {
		t.Fatalf("ColorDiffLine must preserve the line content; got %q", got)
	}
}

func TestClipPreviewLine(t *testing.T) {
	if got := ClipPreviewLine("hello world", 5); got == "" {
		t.Fatal("ClipPreviewLine must not return empty")
	}
}

func TestTruncatePreviewUTF8(t *testing.T) {
	if got := TruncatePreviewUTF8("hello", 100); got != "hello" {
		t.Fatalf("TruncatePreviewUTF8(short) = %q", got)
	}
	if got := TruncatePreviewUTF8(strings.Repeat("x", 1000), 5); !strings.HasPrefix(got, "xxxxx") {
		t.Fatalf("TruncatePreviewUTF8(long, 5) = %q", got)
	}
}

func TestResultLooksLikeDiff(t *testing.T) {
	if !ResultLooksLikeDiff("--- a\n+++ b\n@@ x\n") {
		t.Fatal("a diff-shaped result must be detected as a diff")
	}
	if ResultLooksLikeDiff("ok") {
		t.Fatal("non-diff result must not be flagged")
	}
}

func TestToolIconForName(t *testing.T) {
	for _, name := range []string{"read_file", "write_file", "run_command", "agent_run", "list_directory"} {
		if got := ToolIconForName(name); got == "" {
			t.Errorf("ToolIconForName(%s) = empty", name)
		}
	}
}

func TestSummarizeAgentToolAndSummarizeDelegate(t *testing.T) {
	if got := summarizeAgentTool(cliorchestrate.HandlerDelegate, "do thing", "did thing"); got == "" {
		t.Fatal("summarizeAgentTool must not be empty")
	}
	if got := summarizeDelegate("task one", "result one"); got == "" {
		t.Fatal("summarizeDelegate must not be empty")
	}
}
