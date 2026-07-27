package cli

import (
	"strings"
	"testing"
	"time"
)

func TestResidualS4_HistoryToolLineIncludesElapsed(t *testing.T) {
	m := headlessTUI(80, false, 0)
	start := time.Now().Add(-1500 * time.Millisecond)
	m.appendOneToolBlock(toolRow{Name: "search_replace", Start: start, End: start.Add(1500 * time.Millisecond), Done: true, Result: "updated x"})
	if len(m.blocks) != 1 || !strings.Contains(stripANSI(m.blocks[0].Rendered), "1.5s") {
		t.Fatalf("history block=%+v, want elapsed", m.blocks)
	}
}

func TestResidualS6_TabVisitsLiveToolStrip(t *testing.T) {
	m := headlessTUI(80, false, 0)
	m.blocks = []ChatBlock{{ID: "b1", Kind: ChatBlockAssistant, Text: "answer"}}
	m.toolRows = []toolRow{{ToolCallID: "c1", Name: "read_file", Start: time.Now()}}
	m.setFocus(focusScrollback)
	if !m.cycleChatFocus(false) || !m.toolPanel.Focused || m.toolPanel.Selected != 0 {
		t.Fatalf("tab did not focus live strip: focus=%v panel=%+v", m.focus, m.toolPanel)
	}
}

func TestResidualS6_TabFromComposerWithoutHistoryVisitsLiveToolStrip(t *testing.T) {
	m := headlessTUI(80, false, 0)
	m.toolRows = []toolRow{{ToolCallID: "c1", Name: "read_file", Start: time.Now()}}
	if !m.cycleChatFocus(false) || !m.toolPanel.Focused || m.toolPanel.Selected != 0 {
		t.Fatalf("composer tab did not focus live strip: focus=%v panel=%+v", m.focus, m.toolPanel)
	}
}
