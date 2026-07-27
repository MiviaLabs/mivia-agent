package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
)

// TestFollowPreservesOffsetWhenContentGrows — Phase D residual: when the user
// scrolled up (followOutput=false), growing live content must not yank YOffset.
func TestFollowPreservesOffsetWhenContentGrows(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.width = 80
	m.height = 40
	m.viewport = viewport.New(80, 8)
	// Seed tall history so scroll positions are meaningful.
	for i := 0; i < 40; i++ {
		m.appendBlock(ChatBlock{
			Kind: ChatBlockSystem,
			Text: "line " + strings.Repeat("x", 20) + " " + itoa(i),
		})
	}
	m.layout()
	m.renderVP()
	m.viewport.GotoBottom()
	// Simulate user reading mid-history.
	m.noteUserScrolledUp()
	m.viewport.YOffset = 3
	if m.followOutput {
		t.Fatal("followOutput must be false after noteUserScrolledUp")
	}
	saved := m.viewport.YOffset

	// Grow content via stream + tools (renderStreamVP path).
	m.streamBuf.WriteString("streaming answer chunk one\n")
	m.streamBuf.WriteString("streaming answer chunk two\n")
	m.renderStreamVP()
	if m.viewport.YOffset != saved {
		t.Fatalf("YOffset yanked %d → %d while not following", saved, m.viewport.YOffset)
	}
	if m.followOutput {
		t.Fatal("follow must stay false after stream growth")
	}

	// Tool growth still must not yank.
	m.toolRows = append(m.toolRows, toolRow{
		Name: "read_file", Detail: `{"path":"a.go"}`, Status: "running", Start: time.Now(),
	})
	m.renderStreamVP()
	if m.viewport.YOffset != saved {
		t.Fatalf("tool panel growth yanked YOffset %d → %d", saved, m.viewport.YOffset)
	}

	// Jump to latest restores follow and bottom.
	m.jumpToLatest()
	if !m.followOutput {
		t.Fatal("jumpToLatest must set followOutput")
	}
	if !m.viewport.AtBottom() {
		t.Fatal("jumpToLatest must place viewport at bottom")
	}
}

// TestNoteUserScrolledUpThenPollDoesNotYank — after unfollowing, drain/render
// of more content must preserve the sticky-unfollowed state.
func TestNoteUserScrolledUpThenPollDoesNotYank(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.width = 80
	// Short terminal so history overflows the viewport (AtBottom not always true).
	m.height = 14
	m.viewport = viewport.New(80, 5)
	for i := 0; i < 60; i++ {
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: "hist " + itoa(i)})
	}
	m.layout()
	// Force a small viewport even if layout grew it slightly.
	m.viewport.Height = 5
	m.renderVP()
	m.viewport.GotoBottom()
	if !m.viewport.AtBottom() {
		t.Fatal("precondition: at bottom before scroll-up")
	}
	m.noteUserScrolledUp()
	// Mid-scroll: not at bottom (maxOffset is large with 60 lines / height 5).
	m.viewport.YOffset = 3
	if m.viewport.AtBottom() {
		t.Fatalf("precondition: YOffset=3 must not be AtBottom (total=%d h=%d)",
			m.viewport.TotalLineCount(), m.viewport.Height)
	}
	saved := m.viewport.YOffset

	m.updateFromDrain(bridgeDrain{
		Tools: []bridgeToolEvt{
			{Start: true, ToolCallID: "c1", Name: "list_dir", Detail: `{"path":"."}`, At: time.Now()},
		},
	})
	// updateFromDrain may call renderStreamVP when tools arrive.
	if m.followOutput {
		t.Fatal("tool drain must not re-enable follow after user scrolled up")
	}
	if m.viewport.AtBottom() {
		t.Fatalf("viewport must not jump to bottom after tool drain; YOffset=%d saved=%d", m.viewport.YOffset, saved)
	}
	// applyFollowScroll must restore savedOffset when unfollowed (not drift).
	if m.viewport.YOffset != saved {
		t.Fatalf("YOffset not preserved after tool drain: got %d want %d", m.viewport.YOffset, saved)
	}
}

// TestJumpToLatestKeyPath — end key re-enables follow (handleChatKey).
func TestJumpToLatestKeyPath(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.followOutput = false
	m.focus = focusScrollback
	m.viewport = viewport.New(80, 10)
	m.viewport.SetContent(strings.Repeat("line\n", 40))
	m.viewport.YOffset = 5

	skipTA, _, _ := m.handleChatKey("end", false)
	if !skipTA {
		t.Fatal("end in scrollback should consume key")
	}
	if !m.followOutput {
		t.Fatal("end must re-enable followOutput")
	}
	if !m.viewport.AtBottom() {
		t.Fatal("end must GotoBottom")
	}
}
