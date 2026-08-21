package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Block scroll: a selected thinking block whose content overflows its
// rendered window scrolls with j/k (scrollback focus) and with the mouse
// wheel over its hit-map zone. Only expanded, overflowing blocks scroll;
// everything else falls through so j/k and the wheel keep their normal
// meaning (INV-TUI-16).

// thinkingScrollModel builds a committed thinking block in a chat model,
// selected under scrollback focus. The caller sets Collapsed/text.
func thinkingScrollModel(t *testing.T, text string, collapsed bool) *TUIModel {
	t.Helper()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.blocks = []cli.ChatBlock{
		{ID: "th-1", Kind: cli.ChatBlockThinking, Text: text, Collapsed: collapsed},
	}
	m.renderVP()
	m.focus = cli.FocusScrollback
	m.selectedBlockID = "th-1"
	return m
}

func TestBlockScrollKeysScrollSelectedThinkingBlock(t *testing.T) {
	m := thinkingScrollModel(t, strings.Repeat("line of reasoning\n", 20), false)
	if m.blocks[0].ScrollOffset != 0 {
		t.Fatalf("start offset=%d want 0", m.blocks[0].ScrollOffset)
	}
	m.handleChatKey("j", false)
	if got := m.blocks[0].ScrollOffset; got != 1 {
		t.Fatalf("j must scroll the selected thinking block down by 1, offset=%d", got)
	}
	m.handleChatKey("k", false)
	if got := m.blocks[0].ScrollOffset; got != 0 {
		t.Fatalf("k must scroll the selected thinking block up by 1, offset=%d", got)
	}
}

func TestBlockScrollKeysClampToWindow(t *testing.T) {
	// Exactly 20 split lines (no trailing newline: a trailing "\n" would add
	// a 21st empty element to strings.Split, shifting maxOffset to 15).
	m := thinkingScrollModel(t, strings.Repeat("line of reasoning\n", 19)+"line of reasoning", false)
	// 20 lines, maxThinkingLines=6 → maxOffset = 14.
	for i := 0; i < 14; i++ {
		m.handleChatKey("j", false)
	}
	if got := m.blocks[0].ScrollOffset; got != 14 {
		t.Fatalf("j must reach n-maxThinkingLines=14, got %d", got)
	}
	// At the max offset the offset cannot grow further; the key is declined
	// (no change) and focus falls back to the composer.
	m.handleChatKey("j", false)
	if got := m.blocks[0].ScrollOffset; got != 14 {
		t.Fatalf("j must clamp at 14, got %d", got)
	}
	// Scroll back down to 0.
	m.focus = cli.FocusScrollback
	for i := 0; i < 14; i++ {
		m.handleChatKey("k", false)
	}
	if got := m.blocks[0].ScrollOffset; got != 0 {
		t.Fatalf("k must reach 0, got %d", got)
	}
	// At offset 0 the offset cannot go negative.
	m.focus = cli.FocusScrollback
	m.handleChatKey("k", false)
	if got := m.blocks[0].ScrollOffset; got != 0 {
		t.Fatalf("k must clamp at 0, got %d", got)
	}
}

func TestBlockScrollKeysFallThroughWhenBlockCannotScroll(t *testing.T) {
	cases := []struct {
		name  string
		block cli.ChatBlock
	}{
		{"collapsed thinking", cli.ChatBlock{ID: "th-1", Kind: cli.ChatBlockThinking, Text: strings.Repeat("line\n", 20), Collapsed: true}},
		{"non-overflowing thinking", cli.ChatBlock{ID: "th-1", Kind: cli.ChatBlockThinking, Text: "short\nreasoning", Collapsed: false}},
		{"tool block", cli.ChatBlock{ID: "t1", Kind: cli.ChatBlockTool, ToolName: "read_file", Text: strings.Repeat("tool output\n", 20), Collapsed: false}},
		{"user conversation", cli.ChatBlock{ID: "u1", Kind: cli.ChatBlockUser, Text: strings.Repeat("question\n", 20)}},
		{"assistant conversation", cli.ChatBlock{ID: "a1", Kind: cli.ChatBlockAssistant, Text: strings.Repeat("answer\n", 20)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newSmokeModel(t)
			m.mode = modeChat
			m.width = 80
			m.height = 40
			m.blocks = []cli.ChatBlock{tc.block}
			m.renderVP()
			m.focus = cli.FocusScrollback
			m.selectedBlockID = tc.block.ID

			handled, _ := m.handleBlockActionKey("j")
			if handled {
				t.Fatal("j must not be consumed when the selected block cannot scroll")
			}
			if m.blocks[0].ScrollOffset != 0 {
				t.Fatalf("block ScrollOffset must stay 0, got %d", m.blocks[0].ScrollOffset)
			}
			// Conversation blocks never hide.
			if tc.block.Kind == cli.ChatBlockUser || tc.block.Kind == cli.ChatBlockAssistant {
				if m.blocks[0].Collapsed {
					t.Fatal("conversation block must never collapse")
				}
			}
		})
	}
}

func TestBlockScrollKeysFallThroughRoutesNormally(t *testing.T) {
	// INV-TUI-16: a bare letter acts on blocks only under scrollback focus,
	// and only when the selected block can scroll. When it cannot, j/k is
	// not consumed by the block-action path: the key keeps its normal
	// printable routing (focus returns to the composer) and the viewport is
	// gated out on that same keypress.
	m := thinkingScrollModel(t, "short\nreasoning", false) // non-overflowing
	preOff := m.viewport.YOffset
	skipTextarea, skipViewport, _ := m.handleChatKey("j", false)
	if m.focus != cli.FocusComposer {
		t.Fatalf("fall-through j must route to the composer, focus=%v", m.focus)
	}
	if !skipViewport {
		t.Fatal("fall-through j must gate the viewport out (printable routing)")
	}
	if skipTextarea {
		t.Fatal("fall-through j must reach the composer")
	}
	if m.viewport.YOffset != preOff {
		t.Fatalf("fall-through j must not scroll the viewport: %d → %d", preOff, m.viewport.YOffset)
	}
	if m.blocks[0].ScrollOffset != 0 {
		t.Fatalf("fall-through j must not scroll the block, offset=%d", m.blocks[0].ScrollOffset)
	}
}

func TestBlockScrollKeysKeepWorkGroupFirst(t *testing.T) {
	// A work:<id> selection keeps the group-window scroll: the work-group
	// path runs before the thinking-block path in handleBlockActionKey.
	m := newReadyChatModel(40, 90)
	m.blocks = manyToolBlocks(60)
	key := cli.FindWorkGroups(m.blocks)[0].Key
	m.workGroupCollapsed = map[string]bool{key: false}
	m.renderVP()
	m.focus = cli.FocusScrollback
	m.selectedBlockID = key

	m.handleChatKey("j", false)
	if m.workGroupScroll[key] == 0 {
		t.Fatal("j must still scroll the selected work group window")
	}
}

// thinkingBlockHitY returns a screen Y inside the thinking block's hit-map
// zone after the hit map has been rebuilt for the current viewport state.
func thinkingBlockHitY(t *testing.T, m *TUIModel) int {
	t.Helper()
	rng, ok := m.chatBlockRanges["th-1"]
	if !ok {
		t.Fatalf("thinking block missing from chatBlockRanges: %v", m.chatBlockRanges)
	}
	y := rng[0] + 1 - m.viewport.YOffset
	if y < 1 {
		y = 1
	}
	return y
}

func TestMouseWheelScrollsSelectedExpandedThinkingBlock(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.blocks = []cli.ChatBlock{
		{ID: "th-1", Kind: cli.ChatBlockThinking, Text: strings.Repeat("line of reasoning\n", 20), Collapsed: false},
	}
	m.renderVP()
	m.View() // rebuild the hit map
	m.selectedBlockID = "th-1"
	m.focus = cli.FocusScrollback

	m.Update(tea.MouseMsg{X: 1, Y: thinkingBlockHitY(t, m), Type: tea.MouseWheelDown})
	if got := m.blocks[0].ScrollOffset; got != 1 {
		t.Fatalf("wheel down must scroll the selected expanded block, offset=%d", got)
	}
	// The wheel scroll invalidated the hit map; rebuild it before the next
	// event (the real app re-renders each frame).
	m.View()
	m.Update(tea.MouseMsg{X: 1, Y: thinkingBlockHitY(t, m), Type: tea.MouseWheelUp})
	if got := m.blocks[0].ScrollOffset; got != 0 {
		t.Fatalf("wheel up must scroll the block back, offset=%d", got)
	}
}

func TestMouseWheelOverCollapsedThinkingBlockFallsThrough(t *testing.T) {
	// A collapsed block is a one-line summary: the wheel must not scroll it.
	// adjustThinkingScroll declines, so the event falls through to the
	// transcript wheel path and scrolls the viewport instead.
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	for i := 0; i < 30; i++ {
		m.blocks = append(m.blocks, cli.ChatBlock{ID: "b-" + itoa(i), Kind: cli.ChatBlockAssistant, Text: "history line " + itoa(i)})
	}
	m.blocks = append(m.blocks, cli.ChatBlock{ID: "th-1", Kind: cli.ChatBlockThinking, Text: strings.Repeat("line of reasoning\n", 20), Collapsed: true})
	for i := 30; i < 60; i++ {
		m.blocks = append(m.blocks, cli.ChatBlock{ID: "b-" + itoa(i), Kind: cli.ChatBlockAssistant, Text: "history line " + itoa(i)})
	}
	m.renderVP()

	// Bring the collapsed block onto the visible window without sitting at
	// the very top, so wheel-up has room to move.
	rng, ok := m.chatBlockRanges["th-1"]
	if !ok {
		t.Fatalf("collapsed thinking block missing from ranges: %v", m.chatBlockRanges)
	}
	maxOff := m.viewport.TotalLineCount() - m.viewport.Height
	if maxOff < 0 {
		maxOff = 0
	}
	want := rng[0] - m.viewport.Height/2
	if want < 0 {
		want = 0
	}
	if want > maxOff {
		want = maxOff
	}
	if want < 1 {
		t.Fatalf("fixture must place the block mid-screen with room to scroll up (rng=%v want=%d maxOff=%d)", rng, want, maxOff)
	}
	m.viewport.YOffset = want
	m.followOutput = false
	m.View() // rebuild the hit map at the chosen YOffset

	preOff := m.viewport.YOffset
	skipViewport := false
	m.handleHitMapMouse(tea.MouseMsg{X: 1, Y: thinkingBlockHitY(t, m), Type: tea.MouseWheelUp}, &skipViewport)
	if m.blocks[0].ScrollOffset != 0 {
		t.Fatal("wheel over a collapsed block must not scroll the block")
	}
	if m.viewport.YOffset >= preOff {
		t.Fatalf("wheel up must fall through to the transcript scroll, %d → %d", preOff, m.viewport.YOffset)
	}
}
