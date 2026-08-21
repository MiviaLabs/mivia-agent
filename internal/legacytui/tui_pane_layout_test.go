package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
)

func TestNewChatPaneLayoutShowsSidebarBesideChat(t *testing.T) {
	layout := NewChatPaneLayout(100, true, false)

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

// TestNewChatPaneLayoutRightSidebarOnly pins the right-only shape: the chat
// pane starts at column 0 and shrinks by the right sidebar plus its divider
// lanes.
func TestNewChatPaneLayoutRightSidebarOnly(t *testing.T) {
	layout := NewChatPaneLayout(100, false, true)

	if !layout.rightSidebarVisible {
		t.Fatal("rightSidebarVisible = false, want true")
	}
	if layout.sidebarVisible {
		t.Fatal("left sidebar visible for a right-only request")
	}
	if layout.chatX != 0 {
		t.Fatalf("chatX = %d, want 0 when the left sidebar is hidden", layout.chatX)
	}
	wantChatWidth := 100 - layout.rightSidebarWidth - SidebarDividerLanes
	if layout.chatWidth != wantChatWidth {
		t.Fatalf("chatWidth = %d, want %d (right sidebar plus divider lanes)", layout.chatWidth, wantChatWidth)
	}
	if got := layout.rightSidebarX(); got != layout.chatX+layout.chatWidth+SidebarDividerLanes {
		t.Fatalf("rightSidebarX = %d, want %d", got, layout.chatX+layout.chatWidth+SidebarDividerLanes)
	}
}

// TestNewChatPaneLayoutBothSidebars pins the left+right shape: the chat pane
// sits between two dividers and keeps minimumChatWidth.
func TestNewChatPaneLayoutBothSidebars(t *testing.T) {
	layout := NewChatPaneLayout(100, true, true)

	if !layout.sidebarVisible || !layout.rightSidebarVisible {
		t.Fatalf("sidebars = left:%v right:%v, want both visible", layout.sidebarVisible, layout.rightSidebarVisible)
	}
	if layout.chatX != layout.sidebarWidth+SidebarDividerLanes {
		t.Fatalf("chatX = %d, want %d", layout.chatX, layout.sidebarWidth+SidebarDividerLanes)
	}
	if layout.chatWidth < minimumChatWidth {
		t.Fatalf("chatWidth = %d, want at least %d", layout.chatWidth, minimumChatWidth)
	}
	if layout.chatX+layout.chatWidth+layout.rightSidebarWidth+SidebarDividerLanes != 100 {
		t.Fatalf("pane widths do not fill the terminal: %d+%d+%d+%d != 100",
			layout.chatX, layout.chatWidth, layout.rightSidebarWidth, SidebarDividerLanes)
	}
	if got := layout.rightSidebarX(); got != layout.chatX+layout.chatWidth+SidebarDividerLanes {
		t.Fatalf("rightSidebarX = %d, want %d", got, layout.chatX+layout.chatWidth+SidebarDividerLanes)
	}
}

// TestNewChatPaneLayoutRightSidebarHiddenWhenItDoesNotFit pins that the right
// sidebar hides (the chat never shrinks below the floor) when the combined
// widths exceed the terminal.
func TestNewChatPaneLayoutRightSidebarHiddenWhenItDoesNotFit(t *testing.T) {
	layout := NewChatPaneLayout(92, true, true)

	if !layout.sidebarVisible {
		t.Fatal("left sidebar must stay visible")
	}
	if layout.rightSidebarVisible {
		t.Fatal("right sidebar must hide when the combined widths do not fit")
	}
	if layout.chatWidth < minimumChatWidth {
		t.Fatalf("chatWidth = %d, want at least %d (the chat must not shrink)", layout.chatWidth, minimumChatWidth)
	}
}

// TestNewChatPaneLayoutRightSidebarAppearsAtBoundary pins the pop-in width:
// both sidebars are visible once the terminal fits left preferred plus right
// minimum plus both divider lane sets plus the chat floor.
func TestNewChatPaneLayoutRightSidebarAppearsAtBoundary(t *testing.T) {
	if layout := NewChatPaneLayout(92, true, true); layout.rightSidebarVisible {
		t.Fatal("right sidebar visible at width 92, want hidden")
	}
	if layout := NewChatPaneLayout(94, true, true); !layout.rightSidebarVisible {
		t.Fatal("right sidebar hidden at width 94, want visible")
	}
}

func TestRenderBaseChatViewSeparatesSidebarFromChatPane(t *testing.T) {
	m := newReadyChatModel(16, 100)
	m.sessionsSidebar = newSessionsSidebar()

	view := cli.StripANSI(m.renderBaseChatView())
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

// TestRenderBaseChatViewBothSidebars pins the left+right render: both
// dividers sit at the expected columns and every line fills the terminal.
func TestRenderBaseChatViewBothSidebars(t *testing.T) {
	m := newReadyChatModel(16, 100)
	m.sessionsSidebar = newSessionsSidebar()
	m.workflowsSidebar = newWorkflowsSidebar()

	view := cli.StripANSI(m.renderBaseChatView())
	pane := NewChatPaneLayout(100, true, true)
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		t.Fatal("empty render")
	}
	if !strings.Contains(view, " Workflows") || !strings.Contains(view, " Sessions") {
		t.Fatalf("render is missing one sidebar header:\n%s", view)
	}
	leftDividerX := pane.sidebarWidth + 1
	rightDividerX := pane.chatX + pane.chatWidth + 1
	for lineNo, line := range lines {
		if len([]rune(line)) != 100 {
			t.Fatalf("line %d has width %d, want 100: %q", lineNo, len([]rune(line)), line)
		}
		runes := []rune(line)
		if runes[leftDividerX] != '│' {
			t.Fatalf("line %d left divider = %q, want │: %q", lineNo, runes[leftDividerX], line)
		}
		if runes[rightDividerX] != '│' {
			t.Fatalf("line %d right divider = %q, want │: %q", lineNo, runes[rightDividerX], line)
		}
	}
}

// TestRenderBaseChatViewRightSidebarOnly pins the right-only render: the
// divider sits after the chat pane and the workflows sidebar fills the tail.
func TestRenderBaseChatViewRightSidebarOnly(t *testing.T) {
	m := newReadyChatModel(16, 100)
	m.workflowsSidebar = newWorkflowsSidebar()

	view := cli.StripANSI(m.renderBaseChatView())
	pane := NewChatPaneLayout(100, false, true)
	if !strings.Contains(view, " Workflows") {
		t.Fatalf("render is missing the workflows header:\n%s", view)
	}
	rightDividerX := pane.chatX + pane.chatWidth + 1
	for lineNo, line := range strings.Split(view, "\n") {
		if len([]rune(line)) != 100 {
			t.Fatalf("line %d has width %d, want 100: %q", lineNo, len([]rune(line)), line)
		}
		runes := []rune(line)
		if runes[rightDividerX] != '│' {
			t.Fatalf("line %d right divider = %q, want │: %q", lineNo, runes[rightDividerX], line)
		}
	}
}

func TestRenderBaseChatViewOmitsDividerWhenSidebarDoesNotFit(t *testing.T) {
	m := newReadyChatModel(16, 60)
	m.sessionsSidebar = newSessionsSidebar()

	view := cli.StripANSI(m.renderBaseChatView())
	if strings.HasPrefix(view, " sessions ") {
		t.Fatalf("narrow view contains the sessions sidebar: %q", view)
	}
}

func TestNewChatPaneLayoutHidesSidebarOnNarrowTerminal(t *testing.T) {
	layout := NewChatPaneLayout(30, true, false)

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

func TestNewChatPaneLayoutPreservesChatWidthNearPreferredSidebarBoundary(t *testing.T) {
	for width := 67; width <= 71; width++ {
		layout := NewChatPaneLayout(width, true, false)
		if !layout.sidebarVisible {
			t.Fatalf("width %d: sidebarVisible = false, want true", width)
		}
		if layout.chatWidth < minimumChatWidth {
			t.Fatalf("width %d: chatWidth = %d, want at least %d", width, layout.chatWidth, minimumChatWidth)
		}
		wantSidebarWidth := minimumSidebarWidth
		if width == 71 {
			wantSidebarWidth = preferredSidebarWidth
		}
		if layout.sidebarWidth != wantSidebarWidth {
			t.Fatalf("width %d: sidebarWidth = %d, want %d", width, layout.sidebarWidth, wantSidebarWidth)
		}
	}
}

// TestChatPaneWidthAccountsForBothSidebars pins that chatPaneWidth matches
// the layout for every sidebar combination.
func TestChatPaneWidthAccountsForBothSidebars(t *testing.T) {
	m := newReadyChatModel(24, 100)
	combos := []struct {
		name  string
		left  bool
		right bool
	}{
		{"none", false, false},
		{"left only", true, false},
		{"right only", false, true},
		{"both", true, true},
	}
	for _, tc := range combos {
		m.sessionsSidebar = nil
		m.workflowsSidebar = nil
		if tc.left {
			m.sessionsSidebar = newSessionsSidebar()
		}
		if tc.right {
			m.workflowsSidebar = newWorkflowsSidebar()
		}
		want := NewChatPaneLayout(m.width, tc.left, tc.right).chatWidth
		if got := m.chatPaneWidth(); got != want {
			t.Errorf("%s: chatPaneWidth = %d, want %d", tc.name, got, want)
		}
	}
}
