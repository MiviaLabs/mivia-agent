package uiadapter_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// TestRenderSmoke_ToolOutcomeGlyphs is the offline smoke test ADLC
// mandates for a UI-ship phase that changes a renderer
// (.agents/rules/05-adlc-agentic-development-lifecycle.md, "Smoke tests
// for UI-ship phases"). It drives a realistic pending -> start -> end
// event sequence for one successful and one failed tool call through
// uiadapter.TranslateEvent and the real transcript renderer - no live
// credentials, no mocked block construction - and asserts the rendered
// column-1 outcome glyph C3 specifies: "+" exactly once for the settled
// success, "x" exactly once for the settled failure.
func TestRenderSmoke_ToolOutcomeGlyphs(t *testing.T) {
	seq := []agent.Event{
		{Kind: agent.EventToolPending, ToolCallID: "ok-1", Name: "read_file"},
		{Kind: agent.EventToolStart, ToolCallID: "ok-1", Name: "read_file"},
		{Kind: agent.EventToolEnd, ToolCallID: "ok-1", Name: "read_file", Detail: "completed", Output: "package main\n"},
		{Kind: agent.EventToolPending, ToolCallID: "bad-1", Name: "run_command"},
		{Kind: agent.EventToolStart, ToolCallID: "bad-1", Name: "run_command"},
		{Kind: agent.EventToolEnd, ToolCallID: "bad-1", Name: "run_command", Detail: "failed: exit 1"},
	}

	m := transcript.New(loadTestTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	for _, ev := range seq {
		for _, out := range uiadapter.TranslateEvent(ev) {
			m, _ = m.HandleEvent(out)
		}
	}

	rows, _ := m.ExpandedRows(80)
	plusRows, xRows := 0, 0
	for _, row := range rows {
		trimmed := strings.TrimLeft(ansi.Strip(row), " ")
		switch {
		case strings.HasPrefix(trimmed, "+ read_file"):
			plusRows++
		case strings.HasPrefix(trimmed, "x run_command"):
			xRows++
		}
	}
	if plusRows != 1 {
		t.Errorf("rows starting with the \"+\" outcome glyph = %d, want exactly 1 (the settled success)", plusRows)
	}
	if xRows != 1 {
		t.Errorf("rows starting with the \"x\" outcome glyph = %d, want exactly 1 (the settled failure)", xRows)
	}

	full := ansi.Strip(strings.Join(rows, "\n"))
	if strings.Contains(full, "read_file") && strings.Contains(full, " ok") {
		t.Errorf("a settled success must drop the redundant \"ok\" state word (C3):\n%s", full)
	}
	if !strings.Contains(full, "failed") {
		t.Errorf("a settled failure must keep its state word:\n%s", full)
	}
}
