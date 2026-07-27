package cli

import (
	"strings"
	"testing"
)

// TestRenderThinkingBlock_Windowed verifies thinking with >6 lines shows
// only a window of lines with scroll indicators. scrollOffset=0 = bottom.
func TestRenderThinkingBlock_Windowed(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	lines := renderThinkingBlock(text, false, 0)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (header + content + indicator), got %d: %v", len(lines), lines)
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "↑ ...") {
		t.Fatalf("expected '↑ ...' scroll-indicator when at bottom (can scroll up), got %q", plain)
	}
	if strings.Contains(plain, "↓ ...") {
		t.Fatalf("should NOT have '↓ ...' at bottom, got %q", plain)
	}
	if !strings.Contains(plain, "▾ thinking") {
		t.Fatalf("expected thinking header, got %q", plain)
	}
	// At bottom, should show most recent 6 lines (line5-line10)
	if strings.Contains(plain, "line1\n") || strings.Contains(plain, "line1\r") {
		t.Fatalf("should not show line1 at bottom scroll, got %q", plain)
	}
	if !strings.Contains(plain, "line5") || !strings.Contains(plain, "line10") {
		t.Fatalf("should show lines 5-10 at bottom scroll, got %q", plain)
	}
}

// TestRenderThinkingBlock_ScrolledUp verifies thinking scroll offset shows older lines.
func TestRenderThinkingBlock_ScrolledUp(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	lines := renderThinkingBlock(text, false, 2)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "↑ ...") {
		t.Fatalf("expected '↑ ...' when not at bottom, got %q", plain)
	}
	if !strings.Contains(plain, "↓ ...") {
		t.Fatalf("expected '↓ ...' when not at top, got %q", plain)
	}
	if !strings.Contains(plain, "line3") {
		t.Fatalf("expected line3 in scrolled view, got %q", plain)
	}
	if !strings.Contains(plain, "line8") {
		t.Fatalf("expected line8 in scrolled view, got %q", plain)
	}
}

// TestRenderThinkingBlock_AllLines verifies thinking with ≤6 lines shows all, no indicators.
func TestRenderThinkingBlock_AllLines(t *testing.T) {
	text := "line1\nline2\nline3"
	lines := renderThinkingBlock(text, false, 0)
	plain := stripANSI(strings.Join(lines, "\n"))
	if strings.Contains(plain, "↑ ...") || strings.Contains(plain, "↓ ...") {
		t.Fatalf("should not have scroll indicators for small content, got %q", plain)
	}
	if !strings.Contains(plain, "line1") || !strings.Contains(plain, "line3") {
		t.Fatalf("expected all lines, got %q", plain)
	}
}

// TestRenderThinkingBlock_Collapsed verifies collapsed thinking shows only header.
func TestRenderThinkingBlock_Collapsed(t *testing.T) {
	text := "line1\nline2\nline3"
	lines := renderThinkingBlock(text, true, 0)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line for collapsed, got %d: %v", len(lines), lines)
	}
	plain := stripANSI(lines[0])
	if strings.Contains(plain, "secret") {
		t.Fatalf("collapsed thinking leaked body: %q", plain)
	}
}

// TestRenderThinkingBlock_Empty verifies empty thinking shows only header.
func TestRenderThinkingBlock_Empty(t *testing.T) {
	lines := renderThinkingBlock("", false, 0)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line for empty, got %d: %v", len(lines), lines)
	}
}

// TestRenderThinkingBlock_ScrollAtTop verifies scrollOffset at top (scrollOffset=maxOffset).
func TestRenderThinkingBlock_ScrollAtTop(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8"
	// maxOffset = 8 - 6 = 2. scrollOffset=2 means at top (oldest lines).
	lines := renderThinkingBlock(text, false, 2)
	plain := stripANSI(strings.Join(lines, "\n"))
	if strings.Contains(plain, "↑ ...") {
		t.Fatalf("should NOT have '↑ ...' at top, got %q", plain)
	}
	if !strings.Contains(plain, "↓ ...") {
		t.Fatalf("expected '↓ ...' at top (can scroll down), got %q", plain)
	}
	if !strings.Contains(plain, "line1") {
		t.Fatalf("expected line1 at top, got %q", plain)
	}
}

// TestRenderThinkingBlock_ScrollMiddle verifies scrollOffset in middle.
func TestRenderThinkingBlock_ScrollMiddle(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	lines := renderThinkingBlock(text, false, 2)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "↑ ...") {
		t.Fatalf("expected '↑ ...' in middle, got %q", plain)
	}
	if !strings.Contains(plain, "↓ ...") {
		t.Fatalf("expected '↓ ...' in middle, got %q", plain)
	}
}

// TestRenderChatBlocks_ThinkingUsesScrollOffset verifies RenderChatBlocks
// passes the block's ScrollOffset to renderThinkingBlock.
func TestRenderChatBlocks_ThinkingUsesScrollOffset(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	blocks := []ChatBlock{
		{ID: "t1", Kind: ChatBlockThinking, Text: text, Collapsed: false, ScrollOffset: 2},
	}
	rendered := RenderChatBlocks(blocks, "model", 80)
	plain := stripANSI(strings.Join(rendered.Lines, "\n"))
	if !strings.Contains(plain, "line3") || !strings.Contains(plain, "line8") {
		t.Fatalf("expected scrolled content (offset=2), got %q", plain)
	}
}
