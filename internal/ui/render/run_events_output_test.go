package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestFormatRunEventsOutput_HappyPath(t *testing.T) {
	th := loadTheme(t)
	raw := `{"run_id":"run-123","events":[
		{"id":"e1","sequence":1,"kind":"run_created","created_at":"2026-08-26T10:00:00Z"},
		{"id":"e2","sequence":2,"kind":"task_completed","task_id":"t1","created_at":"2026-08-26T10:00:05Z"},
		{"id":"e3","sequence":3,"kind":"task_failed","task_id":"t2","created_at":"2026-08-26T10:00:10Z"}
	],"count":3,"truncated":false}`
	summary, lines := FormatRunEventsOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "3 events") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"run_created", "task_completed", "t1", "task_failed", "t2"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
}

func TestFormatRunEventsOutput_Truncated(t *testing.T) {
	th := loadTheme(t)
	raw := `{"run_id":"run-123","events":[{"id":"e1","sequence":1,"kind":"task_completed","task_id":"t1"}],"count":1,"truncated":true}`
	summary, _ := FormatRunEventsOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "truncated") {
		t.Errorf("expected truncated marker in summary: %q", summary)
	}
}

func TestFormatRunEventsOutput_EmptyEvents(t *testing.T) {
	th := loadTheme(t)
	raw := `{"run_id":"run-123","events":[],"count":0,"truncated":false}`
	summary, lines := FormatRunEventsOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(summary, "0 events") {
		t.Errorf("unexpected summary: %q", summary)
	}
	if len(lines) != 1 || !strings.Contains(ansi.Strip(lines[0]), "no events") {
		t.Errorf("expected no-events line, got %v", lines)
	}
}

func TestFormatRunEventsOutput_UnknownRunID(t *testing.T) {
	th := loadTheme(t)
	summary, _ := FormatRunEventsOutput(th, theme.TierTrueColor, `{"error":"unknown run_id"}`, 80)
	if !strings.Contains(ansi.Strip(summary), "unknown run_id") {
		t.Errorf("unexpected summary: %q", summary)
	}
}

func TestFormatRunEventsOutput_UnknownKind(t *testing.T) {
	th := loadTheme(t)
	raw := `{"error":"unknown kind","accepted":["run_created","task_completed"]}`
	summary, lines := FormatRunEventsOutput(th, theme.TierTrueColor, raw, 80)
	if !strings.Contains(ansi.Strip(summary), "unknown kind") {
		t.Errorf("unexpected summary: %q", summary)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "run_created") || !strings.Contains(plain, "task_completed") {
		t.Errorf("expected accepted kinds listed in:\n%s", plain)
	}
}

func TestFormatRunEventsOutput_CapsLargeBatches(t *testing.T) {
	th := loadTheme(t)
	var b strings.Builder
	b.WriteString(`{"run_id":"run-123","count":20,"truncated":false,"events":[`)
	for i := 0; i < 20; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"e","sequence":1,"kind":"task_completed","task_id":"t"}`)
	}
	b.WriteString(`]}`)
	_, lines := FormatRunEventsOutput(th, theme.TierTrueColor, b.String(), 80)
	if len(lines) != maxRunEventRows+1 {
		t.Fatalf("expected %d rows + 1 tail line, got %d", maxRunEventRows, len(lines))
	}
}

func TestFormatRunEventsOutput_FallsBackOnGarbage(t *testing.T) {
	th := loadTheme(t)
	summary, lines := FormatRunEventsOutput(th, theme.TierTrueColor, "not json", 80)
	if summary != "" || len(lines) != 1 || lines[0] != "not json" {
		t.Errorf("expected raw passthrough, got summary=%q lines=%v", summary, lines)
	}
}
