package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestFormatToolOutputWithContext_RoutesDispatchTasksToItsFormatter pins the
// switch wiring, not just the leaf formatter: dispatch_tasks output is a
// bare JSON array, which also satisfies isJSONPayload - without isDispatchTool
// placed ahead of that generic case, dispatch_tasks would silently fall
// through to the raw key:value JSON dump this change was meant to replace.
func TestFormatToolOutputWithContext_RoutesDispatchTasksToItsFormatter(t *testing.T) {
	th := loadTheme(t)
	raw := `[{"task_id":"t1","status":"completed","agent":"reviewer","elapsed":"1s","synopsis":"ok"}]`
	summary, body, _ := FormatToolOutputWithContext(th, theme.TierTrueColor, "dispatch_tasks", nil, raw, true, 80)
	if !strings.Contains(summary, "1 tasks") {
		t.Fatalf("expected FormatToolOutputWithContext to route dispatch_tasks through FormatDispatchTasksOutput, got summary %q", summary)
	}
	plain := ansi.Strip(strings.Join(body, "\n"))
	if !strings.Contains(plain, "t1") || strings.Contains(plain, `"task_id"`) {
		t.Errorf("expected the per-task rendering, not a raw JSON dump, got:\n%s", plain)
	}
}

func TestFormatDispatchTasksOutput_MixedStatuses(t *testing.T) {
	th := loadTheme(t)
	raw := `[
		{"task_id":"audit-clean","status":"completed","agent":"reviewer","elapsed":"12s","synopsis":"no findings"},
		{"task_id":"audit-hostile","status":"failed","agent":"builder","elapsed":"31s","error":"openai: provider error (HTTP 402)","error_ref":"ref:error:bf2fe65ad3daccebef8"}
	]`
	summary, body := FormatDispatchTasksOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "2 tasks") || !strings.Contains(summary, "1 completed") || !strings.Contains(summary, "1 failed") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(body, "\n"))
	for _, want := range []string{"audit-clean", "audit-hostile", "reviewer", "builder", "no findings", "ref:error:bf2fe65a"} {
		if !strings.Contains(plain, want) {
			t.Errorf("body missing %q, got:\n%s", want, plain)
		}
	}
	// A full-length hash must never leak - only the shortened 8-char digest.
	if strings.Contains(plain, "bf2fe65ad3daccebef8") {
		t.Errorf("full ref hash leaked into body:\n%s", plain)
	}
}

func TestFormatDispatchTasksOutput_CapsLargeBatches(t *testing.T) {
	th := loadTheme(t)
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 10; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"task_id":"t` + string(rune('0'+i)) + `","status":"completed"}`)
	}
	b.WriteString("]")
	_, body := FormatDispatchTasksOutput(th, theme.TierTrueColor, b.String(), 80)
	if len(body) != maxDispatchTaskRows+1 {
		t.Fatalf("expected %d rows + 1 tail line, got %d: %v", maxDispatchTaskRows, len(body), body)
	}
	plain := ansi.Strip(body[len(body)-1])
	if !strings.Contains(plain, "4 more tasks") {
		t.Errorf("expected tail to report 4 more tasks, got %q", plain)
	}
}

// TestFormatDispatchTasksOutput_WrappedEnvelope guards dispatch_tasks'
// async result shape (wait="none"/"task": {"run_id":...,"task_results":[...]}),
// not just the bare array wait="run" returns. Before this, the wrapped
// envelope failed FormatDispatchTasksOutput's array-only unmarshal and fell
// through to a raw JSON dump in the live transcript.
func TestFormatDispatchTasksOutput_WrappedEnvelope(t *testing.T) {
	th := loadTheme(t)
	raw := `{"run_id":"run-abc123","status":"completed","task_results":[{"task_id":"task-async","status":"completed","agent":"worker","synopsis":"async done"}]}`
	summary, body := FormatDispatchTasksOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "run-abc123") && !strings.Contains(summary, "completed") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(body, "\n"))
	if !strings.Contains(plain, "task-async") || !strings.Contains(plain, "worker") || !strings.Contains(plain, "async done") {
		t.Errorf("body missing expected task fields, got:\n%s", plain)
	}
}

func TestFormatDispatchTasksOutput_FallsBackOnNonArray(t *testing.T) {
	th := loadTheme(t)
	summary, body := FormatDispatchTasksOutput(th, theme.TierTrueColor, "not json", 80)
	if summary != "" {
		t.Errorf("expected empty summary on parse failure, got %q", summary)
	}
	if len(body) != 1 || body[0] != "not json" {
		t.Errorf("expected raw passthrough, got %v", body)
	}
}

func TestFormatLedgerOutput_HasMoreFooter(t *testing.T) {
	th := loadTheme(t)
	raw := `{"status":"ok","ref":"ref:output:a12da74ee9bb6edd901a08ce98646ba2ec40c0c","kind":"output","bytes":8192,"offset":0,"limit":4096,"returned_bytes":4096,"has_more":true,"next_offset":4096,"content":"line one"}`
	_, lines := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "offset=4096") {
		t.Errorf("expected a continuation hint naming the next offset, got:\n%s", plain)
	}
}

func TestFormatLedgerOutput_NoFooterWhenComplete(t *testing.T) {
	th := loadTheme(t)
	raw := `{"status":"ok","ref":"ref:output:a12da74ee9bb6edd901a08ce98646ba2ec40c","kind":"output","bytes":8,"offset":0,"limit":4096,"has_more":false,"content":"complete"}`
	_, lines := FormatLedgerOutput(th, theme.TierTrueColor, raw, 80)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Contains(plain, "more remains") {
		t.Errorf("did not expect a continuation footer for a complete read, got:\n%s", plain)
	}
}

func TestFormatLedgerDetail_ContinuationLabel(t *testing.T) {
	args := map[string]any{"ref": "ref:output:a12da74ee9bb6edd901a08ce98646ba2ec40c", "offset": float64(4096)}
	detail := formatLedgerDetail("read_output", args)
	if !strings.Contains(detail, "continuing read") || !strings.Contains(detail, "offset 4096") {
		t.Errorf("expected a human continuation label, got %q", detail)
	}
	if strings.Contains(detail, "a12da74ee9bb6edd901a08ce98646ba2ec40c") {
		t.Errorf("expected the shortened ref, not the full digest, got %q", detail)
	}
}

func TestFormatLedgerDetail_FirstPageStaysShort(t *testing.T) {
	args := map[string]any{"ref": "ref:output:a12da74ee9bb6edd901a08ce98646ba2ec40c"}
	detail := formatLedgerDetail("read_output", args)
	if strings.Contains(detail, "continuing read") {
		t.Errorf("did not expect continuation wording on a first page, got %q", detail)
	}
}

func TestColorizeUnifiedDiff_RealHunkGetsColored(t *testing.T) {
	th := loadTheme(t)
	diff := strings.Join([]string{
		"diff --git a/main.go b/main.go",
		"index 1111111..2222222 100644",
		"--- a/main.go",
		"+++ b/main.go",
		"@@ -1,3 +1,3 @@",
		" package main",
		"-func old() {}",
		"+func new() {}",
	}, "\n")
	lines, _ := FormatCommandOutput(th, theme.TierTrueColor, diff, true, 80)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "func old()") || !strings.Contains(plain, "func new()") {
		t.Errorf("diff content missing from output:\n%s", plain)
	}
	// The added/removed lines must actually carry color (not equal to the
	// plain, unstyled text) when a real terminal tier is in play.
	var addStyled, delStyled bool
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l), "+func new() {}") && l != "+func new() {}" {
			addStyled = true
		}
		if strings.Contains(ansi.Strip(l), "-func old() {}") && l != "-func old() {}" {
			delStyled = true
		}
	}
	if !addStyled || !delStyled {
		t.Errorf("expected added/removed diff lines to carry ANSI styling, got:\n%v", lines)
	}
}

// A '+'/'-' prefix with no real hunk header must NEVER be colored: this is
// the false-positive case the review flagged (git log --stat output, a
// markdown list, an ordinary numeric delta all start with '+'/'-' without
// being a diff).
func TestColorizeUnifiedDiff_NoHunkHeaderNeverColors(t *testing.T) {
	th := loadTheme(t)
	notADiff := strings.Join([]string{
		"main.go | 3 +--",
		"utils.go | 1 +",
		"- a markdown bullet",
		"+ another bullet",
		"2 files changed, 2 insertions(+), 2 deletions(-)",
	}, "\n")
	lines, _ := FormatCommandOutput(th, theme.TierTrueColor, notADiff, true, 80)
	plain := strings.Join(lines, "\n")
	if ansi.Strip(plain) != plain {
		t.Errorf("non-diff output must not carry any ANSI styling, got:\n%q", plain)
	}
}

// A hunk-header-shaped SUBSTRING appearing inside unrelated output (a log
// message, a docs excerpt explaining diff syntax) must not flip the whole
// output into diff-coloring mode: only a hunk header directly preceded by a
// real file header (diff --git / +++) counts as an actual diff.
func TestColorizeUnifiedDiff_HunkHeaderInUnrelatedTextNeverColors(t *testing.T) {
	th := loadTheme(t)
	notADiff := strings.Join([]string{
		"Example hunk header syntax:",
		"@@ -1,1 +1,1 @@",
		"+ this is a checklist item, not a diff",
		"- so is this one",
	}, "\n")
	lines, _ := FormatCommandOutput(th, theme.TierTrueColor, notADiff, true, 80)
	plain := strings.Join(lines, "\n")
	if ansi.Strip(plain) != plain {
		t.Errorf("a bare hunk-header substring with no preceding file header must not trigger coloring, got:\n%q", plain)
	}
}

// A real diff content line whose own first characters happen to collide
// with the file-header prefix ("-- "/"++ ") must still be colored as
// add/remove content, not misread as a file header, because it appears
// INSIDE a hunk (after the hunk header), where a file header never occurs.
func TestColorizeUnifiedDiff_ContentLineCollidingWithHeaderPrefixStaysColored(t *testing.T) {
	th := loadTheme(t)
	diff := strings.Join([]string{
		"diff --git a/notes.md b/notes.md",
		"--- a/notes.md",
		"+++ b/notes.md",
		"@@ -1,2 +1,2 @@",
		"-- old bullet point",
		"++ new bullet point",
	}, "\n")
	lines, _ := FormatCommandOutput(th, theme.TierTrueColor, diff, true, 80)
	var delLine, addLine string
	for _, l := range lines {
		switch {
		case strings.Contains(ansi.Strip(l), "old bullet point"):
			delLine = l
		case strings.Contains(ansi.Strip(l), "new bullet point"):
			addLine = l
		}
	}
	if delLine == ansi.Strip(delLine) {
		t.Errorf("removed content line colliding with the file-header prefix must still be colored, got %q", delLine)
	}
	if addLine == ansi.Strip(addLine) {
		t.Errorf("added content line colliding with the file-header prefix must still be colored, got %q", addLine)
	}
}
