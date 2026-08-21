package legacytui

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/charmbracelet/bubbles/viewport"
)

func makeSession(n int) *chat.Session {
	sess := &chat.Session{Messages: make([]provider.Message, 0, n)}
	for i := 0; i < n; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		sess.Messages = append(sess.Messages, provider.Message{
			Role:    role,
			Content: fmt.Sprintf("line-%d", i),
		})
	}
	return sess
}

func seedUIFromOffset(m *TUIModel, from int) {
	for i := from; i < len(m.session.Messages); i++ {
		msg := m.session.Messages[i]
		if msg.Role == provider.RoleSystem {
			continue
		}
		m.messages = append(m.messages,
			fmt.Sprintf("── %s ──", msg.Role),
			msg.Content,
		)
	}
	m.viewport.SetContent(m.buildViewportContent())
}

// TestLoadMoreMessages_PreservesOffsetWhenContentFits: when pre-load content
// still fits the viewport (AtTop∧AtBottom), loadMore must not jump to bottom.
func TestLoadMoreMessages_PreservesOffsetWhenContentFits(t *testing.T) {
	sess := makeSession(30)
	// Tall viewport so short content fits both before and after a small prepend.
	m := &TUIModel{
		session:   sess,
		messages:  nil,
		msgOffset: 25,
		width:     80,
		height:    80,
		viewport:  viewport.New(80, 60),
	}
	seedUIFromOffset(m, 25)
	if m.viewport.TotalLineCount() > m.viewport.Height {
		// Deterministic setup: force fit by shrinking content, not Skip.
		m.messages = m.messages[:cli.Min(4, len(m.messages))]
		m.viewport.SetContent(m.buildViewportContent())
	}
	if m.viewport.TotalLineCount() > m.viewport.Height {
		t.Fatalf("setup failed: content still taller than viewport (%d > %d)",
			m.viewport.TotalLineCount(), m.viewport.Height)
	}

	m.viewport.YOffset = 0
	oldOff := m.viewport.YOffset
	oldMsgOff := m.msgOffset
	oldMsgLen := len(m.messages)

	m.loadMoreMessages()

	if m.msgOffset >= oldMsgOff {
		t.Fatalf("msgOffset should decrease after load: was %d now %d", oldMsgOff, m.msgOffset)
	}
	if len(m.messages) <= oldMsgLen {
		t.Fatalf("messages should grow: was %d now %d", oldMsgLen, len(m.messages))
	}
	// Must not jump upward past old top (prepend should shift YOffset down or keep it).
	if m.viewport.YOffset < oldOff {
		t.Fatalf("YOffset decreased unexpectedly: %d < %d", m.viewport.YOffset, oldOff)
	}
	// Critical: when content still fits, YOffset should stay at a stable near-top
	// position - not require AtBottom semantics (old bug used GotoBottom).
	if m.viewport.TotalLineCount() <= m.viewport.Height && m.viewport.YOffset != 0 && !m.viewport.AtTop() {
		// After prepend with fit content, maxOff==0 so YOffset must be 0.
		if m.viewport.YOffset != 0 {
			t.Fatalf("fit content should clamp YOffset to 0, got %d", m.viewport.YOffset)
		}
	}
}

// TestLoadMoreMessages_IncreasesContentAndKeepsNearTop: tall history so viewport
// cannot show everything; loadMore decreases msgOffset and grows messages while
// keeping the previously visible region (YOffset advances by added visual lines).
func TestLoadMoreMessages_IncreasesContentAndKeepsNearTop(t *testing.T) {
	// Many messages with multi-line content so viewport is scrollable.
	sess := &chat.Session{Messages: make([]provider.Message, 0, 80)}
	for i := 0; i < 80; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		// Multi-line body → cli.VisualLineCount > slot count.
		sess.Messages = append(sess.Messages, provider.Message{
			Role:    role,
			Content: fmt.Sprintf("line-%d\nextra\nmore", i),
		})
	}
	m := &TUIModel{
		session:   sess,
		messages:  nil,
		msgOffset: 40,
		width:     80,
		height:    24,
		viewport:  viewport.New(80, 12),
	}
	seedUIFromOffset(m, 40)
	if m.viewport.TotalLineCount() <= m.viewport.Height {
		t.Fatalf("setup: need scrollable content, total=%d height=%d",
			m.viewport.TotalLineCount(), m.viewport.Height)
	}

	m.viewport.YOffset = 0
	oldMsgOff := m.msgOffset
	oldMsgLen := len(m.messages)
	oldTotal := m.viewport.TotalLineCount()

	m.loadMoreMessages()

	if m.msgOffset >= oldMsgOff {
		t.Fatalf("msgOffset should decrease: was %d now %d", oldMsgOff, m.msgOffset)
	}
	if m.msgOffset != cli.Max(0, oldMsgOff-50) {
		// batchSize is 50; from 40 we load all remaining older → 0
		if oldMsgOff <= 50 && m.msgOffset != 0 {
			t.Fatalf("msgOffset want 0 (loaded all older), got %d", m.msgOffset)
		}
		if oldMsgOff > 50 && m.msgOffset != oldMsgOff-50 {
			t.Fatalf("msgOffset want %d, got %d", oldMsgOff-50, m.msgOffset)
		}
	}
	if len(m.messages) <= oldMsgLen {
		t.Fatalf("messages should grow: was %d now %d", oldMsgLen, len(m.messages))
	}
	if m.viewport.TotalLineCount() <= oldTotal {
		t.Fatalf("viewport total lines should grow: was %d now %d",
			oldTotal, m.viewport.TotalLineCount())
	}
	// Near-top preservation: after prepend at YOffset=0, new offset equals
	// added visual lines (clamped), so previously top-visible content stays put.
	if m.viewport.YOffset <= 0 && m.viewport.TotalLineCount() > m.viewport.Height {
		t.Fatalf("YOffset should advance after prepend at top, got %d (total=%d)",
			m.viewport.YOffset, m.viewport.TotalLineCount())
	}
}

// TestHomeKeyBehavior exercises the Home path without KeyMsg: GotoTop then
// load older batches while still at top (same loop as tui.go home handler).
func TestHomeKeyBehavior(t *testing.T) {
	sess := makeSession(120)
	m := &TUIModel{
		session:   sess,
		messages:  nil,
		msgOffset: 90,
		width:     80,
		height:    30,
		viewport:  viewport.New(80, 15),
	}
	seedUIFromOffset(m, 90)
	// Simulate user scrolled down then pressed Home.
	m.viewport.GotoBottom()
	if m.viewport.YOffset == 0 && m.viewport.TotalLineCount() > m.viewport.Height {
		t.Fatal("setup: expected non-zero YOffset at bottom")
	}

	m.viewport.GotoTop()
	if m.viewport.YOffset != 0 {
		t.Fatalf("GotoTop YOffset want 0, got %d", m.viewport.YOffset)
	}

	// Same as home key: load up to 3 batches while msgOffset > 0.
	loads := 0
	for i := 0; i < 3 && m.msgOffset > 0; i++ {
		before := m.msgOffset
		beforeLen := len(m.messages)
		m.loadMoreMessages()
		if m.msgOffset == before {
			break
		}
		if len(m.messages) <= beforeLen {
			t.Fatalf("load %d: messages did not grow", i)
		}
		loads++
		m.viewport.GotoTop()
		if m.viewport.YOffset != 0 {
			t.Fatalf("after load %d GotoTop YOffset=%d", i, m.viewport.YOffset)
		}
	}
	if loads == 0 {
		t.Fatal("expected at least one history load from Home path")
	}
	if m.msgOffset >= 90 {
		t.Fatalf("msgOffset should have decreased from 90, got %d", m.msgOffset)
	}
}

func TestTryLoadHistoryNearTop_Matrix(t *testing.T) {
	cases := []struct {
		name      string
		msgOffset int
		yOffset   int
		want      bool
	}{
		{"history_at_top", 5, 0, true},
		{"history_y1", 5, 1, true},
		{"history_y2", 10, 2, true},
		{"history_y3_boundary", 10, 3, false}, // yOffset < 3
		{"history_far", 10, 20, false},
		{"no_history_at_top", 0, 0, false},
		{"no_history_near", 0, 1, false},
		{"negative_offset_guard", -1, 0, false}, // msgOffset > 0 required
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tryLoadHistoryNearTop(tc.msgOffset, tc.yOffset)
			if got != tc.want {
				t.Fatalf("tryLoadHistoryNearTop(%d, %d)=%v want %v",
					tc.msgOffset, tc.yOffset, got, tc.want)
			}
		})
	}
}

// TestAppendMsg_TruncateAdvancesMsgOffset calls real appendMsg past the
// block cap (maxBlocks) and asserts msgOffset advances by dropped blocks.
func TestAppendMsg_TruncateAdvancesMsgOffset(t *testing.T) {
	const maxBlocks = 1000 // must match appendBlock const
	sess := &chat.Session{Messages: make([]provider.Message, 5000)}
	m := &TUIModel{
		session:   sess,
		blocks:    nil,
		msgOffset: 100,
	}
	// Fill to exactly the cap without truncation.
	for i := 0; i < maxBlocks; i++ {
		m.appendMsg(fmt.Sprintf("line-%d", i))
	}
	if len(m.blocks) != maxBlocks {
		t.Fatalf("prefill len=%d want %d", len(m.blocks), maxBlocks)
	}
	if m.msgOffset != 100 {
		t.Fatalf("prefill should not advance msgOffset, got %d", m.msgOffset)
	}

	// One past cap → drop 1, msgOffset += 1
	m.appendMsg("overflow-1")
	if len(m.blocks) != maxBlocks {
		t.Fatalf("after overflow len=%d want %d", len(m.blocks), maxBlocks)
	}
	if m.msgOffset != 101 {
		t.Fatalf("msgOffset after 1 drop want 101, got %d", m.msgOffset)
	}

	// More overflows
	for i := 0; i < 10; i++ {
		m.appendMsg(fmt.Sprintf("overflow-extra-%d", i))
	}
	if len(m.blocks) != maxBlocks {
		t.Fatalf("len after more overflows=%d want %d", len(m.blocks), maxBlocks)
	}
	if m.msgOffset != 111 {
		t.Fatalf("msgOffset after 11 drops want 111, got %d", m.msgOffset)
	}
	// Cap at session length.
	m.msgOffset = len(m.session.Messages) - 1
	m.appendMsg("cap-check")
	if m.msgOffset != len(m.session.Messages) {
		t.Fatalf("msgOffset should clamp to session len %d, got %d",
			len(m.session.Messages), m.msgOffset)
	}
}

func TestAppendMsg_NoOffsetAdvanceWhenOffsetZero(t *testing.T) {
	const maxBlocks = 1000
	m := &TUIModel{
		session:   &chat.Session{Messages: make([]provider.Message, 100)},
		blocks:    nil,
		msgOffset: 0, // all history already loaded - do not invent window
	}
	for i := 0; i < maxBlocks+5; i++ {
		m.appendMsg("x")
	}
	if m.msgOffset != 0 {
		t.Fatalf("msgOffset must stay 0 when no history window, got %d", m.msgOffset)
	}
	if len(m.blocks) != maxBlocks {
		t.Fatalf("blocks=%d want %d", len(m.blocks), maxBlocks)
	}
}

func TestVisualLineCountSlots(t *testing.T) {
	n := cli.VisualLineCount([]string{"a", "b\nc", "d\ne\nf"})
	if n != 1+2+3 {
		t.Fatalf("got %d", n)
	}
	if cli.VisualLineCount(nil) != 0 {
		t.Fatal("nil want 0")
	}
	if cli.VisualLineCount([]string{""}) != 1 {
		t.Fatal("empty string is one visual line")
	}
}

func TestClampViewHeightLogic(t *testing.T) {
	h := 12
	fixed := 1 + 3 + 1 // status input hint
	tool := 4
	minVp := 2
	remain := h - fixed
	if remain-tool < minVp {
		tool = remain - minVp
	}
	vp := remain - tool
	if fixed+tool+vp > h {
		t.Fatalf("overflow budget %d+%d+%d > %d", fixed, tool, vp, h)
	}
}
