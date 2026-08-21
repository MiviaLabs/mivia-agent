package cli

import (
	"strings"
	"testing"
	"time"
)

// User bubble layout contract:
//   line 1: time only
//   line 2+: message body
//   full-width dark-gray background, no left rail
//   one blank line between successive chat blocks

func TestUserLayout_TimeOnOwnLineThenBody(t *testing.T) {
	// Body first; trailing dim meta [ H:MMPM ] (no seconds).
	sent := time.Date(2026, 7, 27, 15, 4, 5, 0, time.Local)
	lines := UserBubble.Render("hello world", 40, sent)

	var content []string
	for _, ln := range lines {
		p := strings.TrimSpace(stripANSI(ln))
		if p != "" {
			content = append(content, p)
		}
	}
	if len(content) < 2 {
		t.Fatalf("want body + time meta, got %v", content)
	}
	if !strings.Contains(content[0], "hello world") {
		t.Fatalf("first content line want body, got %q", content[0])
	}
	last := content[len(content)-1]
	if !strings.Contains(last, "PM") && !strings.Contains(last, "AM") {
		t.Fatalf("last line want time meta, got %q", last)
	}
	if strings.Contains(last, ":05") {
		t.Fatalf("seconds must not appear: %q", last)
	}
}

func TestUserLayout_NoLeftRail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	sent := time.Date(2026, 7, 27, 12, 0, 0, 0, time.Local)
	r := RenderChatBlocks([]ChatBlock{{
		ID: "u", Kind: ChatBlockUser, Text: "no rail please", SentAt: sent,
	}}, "m", 40, true)
	for i, ln := range r.Lines {
		p := stripANSI(ln)
		if strings.HasPrefix(strings.TrimLeft(p, " "), "#") ||
			strings.HasPrefix(strings.TrimLeft(p, " "), "▌") ||
			strings.HasPrefix(p, "#") ||
			strings.HasPrefix(p, "▌") {
			// Allow only if the content itself starts that way - body doesn't.
			if strings.Contains(p, "no rail") || strings.Contains(p, "12:00:00") || strings.TrimSpace(p) == "" {
				if strings.HasPrefix(p, "#") || strings.HasPrefix(p, "▌") {
					t.Fatalf("user line %d has left rail: %q", i, p)
				}
			}
		}
	}
	// railForBlock user must be off
	rail := railForBlock(ChatBlockUser, false, ChromeRenderOpts())
	if rail.Width != 0 {
		t.Fatalf("user rail width=%d want 0", rail.Width)
	}
	if UserBubble.Style.LeftRail != nil && UserBubble.Style.LeftRail.Width > 0 {
		t.Fatal("UserBubble.Style.LeftRail must be nil/off")
	}
}

func TestUserLayout_DistinctBackgroundOnAllLines(t *testing.T) {
	sent := time.Date(2026, 7, 27, 15, 4, 5, 0, time.Local)
	lines := UserBubble.Render("bg body", 36, sent)
	if !UserBubble.Style.HasBackground() {
		t.Fatal("UserBubble must have Background")
	}
	// Every line is background-filled (ANSI SGR present when color on)
	for i, ln := range lines {
		if !strings.Contains(ln, "\033[") && visibleWidth(ln) > 0 {
			// Plain terminals may strip; still require full-width row
			if visibleWidth(ln) < 36 {
				t.Fatalf("line %d not full-width bg bar: vis=%d %q", i, visibleWidth(ln), stripANSI(ln))
			}
		}
		if visibleWidth(ln) != 36 {
			t.Fatalf("line %d width=%d want 36 (full bg bar)", i, visibleWidth(ln))
		}
	}
}

func TestInterBlock_BlankLineBetweenBubbleGroups(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	blocks := []ChatBlock{
		{ID: "u", Kind: ChatBlockUser, Text: "user says hi", SentAt: time.Now()},
		{ID: "a", Kind: ChatBlockAssistant, Text: "assistant replies"},
		{ID: "t", Kind: ChatBlockTool, ToolName: "read_file", Text: "ok", Collapsed: true},
	}
	r := RenderChatBlocks(blocks, "m", 50, true)
	// Find last non-empty of each block range - next line should be blank gap
	// before next block content. Simpler: consecutive content groups separated
	// by at least one fully blank line.
	plainLines := make([]string, len(r.Lines))
	for i, ln := range r.Lines {
		plainLines[i] = stripANSI(ln)
	}
	joined := strings.Join(plainLines, "\n")
	if !strings.Contains(joined, "user says hi") || !strings.Contains(joined, "assistant replies") {
		t.Fatalf("missing bodies: %q", joined)
	}
	// Between each pair of block bodies there must be at least one blank line.
	userIdx, asstIdx, toolIdx := -1, -1, -1
	for i, p := range plainLines {
		if strings.Contains(p, "user says hi") {
			userIdx = i
		}
		if strings.Contains(p, "assistant replies") {
			asstIdx = i
		}
		if strings.Contains(p, "read_file") {
			toolIdx = i
		}
	}
	if userIdx < 0 || asstIdx < 0 || toolIdx < 0 {
		t.Fatalf("missing block markers u=%d a=%d t=%d lines=%v", userIdx, asstIdx, toolIdx, plainLines)
	}
	if !hasBlankBetween(plainLines, userIdx, asstIdx) {
		t.Fatalf("no blank between user and assistant: %v", plainLines[userIdx:asstIdx+1])
	}
	if !hasBlankBetween(plainLines, asstIdx, toolIdx) {
		t.Fatalf("no blank between assistant and tool: %v", plainLines[asstIdx:toolIdx+1])
	}
}

func hasBlankBetween(lines []string, a, b int) bool {
	if a > b {
		a, b = b, a
	}
	for i := a + 1; i < b; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			return true
		}
	}
	return false
}

func TestWantsBottomLane_SpeechOnly(t *testing.T) {
	if !wantsBottomLane(ChatBlock{Kind: ChatBlockUser}, groupMember{}) {
		t.Fatal("user wants bottom lane")
	}
	if !wantsBottomLane(ChatBlock{Kind: ChatBlockAssistant}, groupMember{}) {
		t.Fatal("assistant wants bottom lane")
	}
	if wantsBottomLane(ChatBlock{Kind: ChatBlockTool}, groupMember{}) {
		t.Fatal("standalone tool: no bottom lane")
	}
	if wantsBottomLane(ChatBlock{Kind: ChatBlockTool}, groupMember{InGroup: true, ToolIndex: 0}) {
		t.Fatal("grouped tool: no bottom lane")
	}
	if wantsBottomLane(ChatBlock{Kind: ChatBlockThinking}, groupMember{}) {
		t.Fatal("thinking: no bottom lane")
	}
}
