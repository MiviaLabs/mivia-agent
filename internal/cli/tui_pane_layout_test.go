package cli

import (
	"strings"
	"testing"
)

func TestNewChatPaneLayoutShowsSidebarBesideChat(t *testing.T) {
	layout := newChatPaneLayout(100, true)

	if !layout.sidebarVisible {
		t.Fatal("sidebarVisible = false, want true")
	}
	if layout.sidebarWidth <= 0 {
		t.Fatalf("sidebarWidth = %d, want a positive width", layout.sidebarWidth)
	}
	wantChatX := layout.sidebarWidth + 1 + sidebarDividerWidth + 1
	if layout.chatX != wantChatX {
		t.Fatalf("chatX = %d, want sidebar and divider lanes = %d", layout.chatX, wantChatX)
	}
	if layout.chatX+layout.chatWidth != 100 {
		t.Fatalf("chat right edge = %d, want 100", layout.chatX+layout.chatWidth)
	}
}

func TestRenderBaseChatViewSeparatesSidebarFromChatPane(t *testing.T) {
	m := newReadyChatModel(16, 100)
	m.sessionsSidebar = newSessionsSidebar()

	view := stripANSI(m.renderBaseChatView())
	for lineNo, line := range strings.Split(view, "\n") {
		if len([]rune(line)) != 100 {
			t.Fatalf("line %d has width %d, want 100: %q", lineNo, len([]rune(line)), line)
		}
		runes := []rune(line)
		dividerX := preferredSidebarWidth + 1
		if runes[dividerX] != '│' {
			t.Fatalf("line %d divider = %q, want │: %q", lineNo, runes[dividerX], line)
		}
		if runes[dividerX-1] != ' ' || runes[dividerX+sidebarDividerWidth] != ' ' {
			t.Fatalf("line %d divider lanes = %q and %q, want spaces: %q", lineNo, runes[dividerX-1], runes[dividerX+sidebarDividerWidth], line)
		}
	}
}

func TestRenderBaseChatViewOmitsDividerWhenSidebarDoesNotFit(t *testing.T) {
	m := newReadyChatModel(16, 60)
	m.sessionsSidebar = newSessionsSidebar()

	view := stripANSI(m.renderBaseChatView())
	if strings.HasPrefix(view, " sessions ") {
		t.Fatalf("narrow view contains the sessions sidebar: %q", view)
	}
}

func TestNewChatPaneLayoutHidesSidebarOnNarrowTerminal(t *testing.T) {
	layout := newChatPaneLayout(30, true)

	if layout.sidebarVisible {
		t.Fatal("sidebarVisible = true on a narrow terminal, want false")
	}
	if layout.sidebarWidth != 0 {
		t.Fatalf("sidebarWidth = %d, want 0", layout.sidebarWidth)
	}
	if layout.chatX != 0 {
		t.Fatalf("chatX = %d, want 0", layout.chatX)
	}
	if layout.chatWidth != 30 {
		t.Fatalf("chatWidth = %d, want 30", layout.chatWidth)
	}
}
