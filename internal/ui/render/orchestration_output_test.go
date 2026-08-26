package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestFormatOrchestrationRunOutput_InspectAgentsShape(t *testing.T) {
	th := loadTheme(t)
	raw := `{"run_id":"run-1","display_name":"review-wave","status":"running","tasks":[
		{"task_id":"t1","display_name":"reviewer","status":"completed","output_ref":"ref:output:aaaaaaaaaaaaaaaa"},
		{"task_id":"t2","display_name":"builder","status":"blocked"}
	],"parks":[{"task_id":"t2","question":"which branch?"}]}`
	summary, lines := FormatOrchestrationRunOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "review-wave") || !strings.Contains(summary, "running") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"t1", "t2", "waiting on an answer", "which branch?"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
}

func TestFormatOrchestrationRunOutput_SpawnAgentWithTaskResultsAndRunError(t *testing.T) {
	th := loadTheme(t)
	raw := `{"run_id":"run-2","display_name":"fix-wave","status":"failed","run_error":"task join failed","tasks":[{"task_id":"t1","status":"failed"}],"task_results":[{"task_id":"t1","status":"failed","synopsis":"build broke","error_ref":"ref:error:bbbbbbbbbbbbbbbb"}]}`
	summary, lines := FormatOrchestrationRunOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "fix-wave") || !strings.Contains(summary, "failed") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"task join failed", "build broke", "ref:error:bbbbbbbb"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
}

func TestFormatOrchestrationRunOutput_UnknownRunID(t *testing.T) {
	th := loadTheme(t)
	summary, lines := FormatOrchestrationRunOutput(th, theme.TierTrueColor, `{"error":"unknown run_id"}`, 80)
	if !strings.Contains(ansi.Strip(summary), "unknown run_id") {
		t.Errorf("unexpected summary: %q", summary)
	}
	if len(lines) != 0 {
		t.Errorf("expected no body lines, got %v", lines)
	}
}

func TestFormatOrchestrationRunOutput_FallsBackOnGarbage(t *testing.T) {
	th := loadTheme(t)
	summary, lines := FormatOrchestrationRunOutput(th, theme.TierTrueColor, "not json", 80)
	if summary != "" || len(lines) != 1 || lines[0] != "not json" {
		t.Errorf("expected raw passthrough, got summary=%q lines=%v", summary, lines)
	}
}
