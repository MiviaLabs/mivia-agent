package transcript

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Focus movement and collapse control over the LIVE WINDOW only.
//
// Scope is the whole design of this file. An evicted block belongs to
// terminal scrollback: it is frozen text the terminal owns, and no key
// here can reach it. wireframes-panes.md section 15 is written against
// the live window for that reason.
//
// focus is -1 when the composer holds it. That is the resting state: a
// user who never presses Tab types, and every key goes to the composer.

// Focused reports whether a block currently holds the focus.
func (m Model) Focused() bool { return m.focus >= 0 && m.focus < len(m.blocks) }

// FocusIndex returns the focused block's index, or -1 for the composer.
func (m Model) FocusIndex() int { return m.focus }

// Focus movement is VERTICAL, and the composer is the bottom row.
//
//	Shift-Tab  moves UP:   composer -> newest block -> ... -> oldest
//	Tab        moves DOWN:  oldest -> ... -> newest -> composer
//
// That mapping is the reason neither direction wraps. Tab and Shift-Tab
// already read as "down" and "up" in every form on every platform, and a
// wrap would jump the focus the full height of the screen with no
// visible cause. Instead each end is a wall, except the composer end,
// which is where the user started and always wants to reach again.

// FocusNext moves the focus one block DOWN, towards the newest. From the
// newest block it returns to the composer, which is directly below it.
// From the composer it does nothing: there is nothing below.
func (m Model) FocusNext() Model {
	if len(m.blocks) == 0 || m.focus < 0 {
		return m
	}
	if m.focus >= len(m.blocks)-1 {
		m.focus = -1 // past the newest block is the composer
		return m.syncFocus().ScrollToBottom()
	}
	m.focus++
	return m.syncFocus().ScrollToFocus()
}

// FocusPrev moves the focus one block UP, towards the oldest. From the
// composer it enters at the NEWEST block, the one directly above it.
// It stops at the oldest block: above that is scrollback, which is
// frozen and cannot take the focus.
func (m Model) FocusPrev() Model {
	if len(m.blocks) == 0 {
		return m
	}
	if m.focus < 0 {
		m.focus = len(m.blocks) - 1
		return m.syncFocus().ScrollToFocus()
	}
	if m.focus > 0 {
		m.focus--
	}
	return m.syncFocus().ScrollToFocus()
}

// ClearFocus returns the focus to the composer.
func (m Model) ClearFocus() Model {
	m.focus = -1
	return m.syncFocus()
}

// syncFocus copies the focus index onto the blocks, which is what Render
// reads. Holding the index once, and deriving the per-block flag from
// it, keeps two blocks from both claiming the focus.
func (m Model) syncFocus() Model {
	if m.focus < -1 {
		m.focus = -1
	}
	if m.focus >= len(m.blocks) {
		m.focus = len(m.blocks) - 1
	}
	blocks := make([]Block, len(m.blocks))
	copy(blocks, m.blocks)
	for i := range blocks {
		blocks[i].Focused = i == m.focus
	}
	m.blocks = blocks
	return m
}

// ToggleFocused collapses or expands the focused block. It reports
// false when nothing is focused, or when the focused block cannot
// collapse, so the caller can pass the key on instead of swallowing it.
//
// A toggle changes the block's height, so the viewport is re-anchored on
// the focused block rather than on a row number. Without that, expanding
// a block scrolls the thing the user just acted on off the screen.
func (m Model) ToggleFocused() (Model, bool) {
	if !m.Focused() {
		return m, false
	}
	if !m.blocks[m.focus].Collapsible {
		return m, false
	}
	blocks := make([]Block, len(m.blocks))
	copy(blocks, m.blocks)
	blocks[m.focus].Collapsed = !blocks[m.focus].Collapsed
	m.blocks = blocks
	return m.ScrollToFocus(), true
}

// SetAllCollapsed collapses or expands every collapsible block.
//
// The conversation's total height changes by a large factor, so the
// viewport re-anchors on the focused block when there is one. Expanding
// everything and then leaving the reader at the same row number would
// put them somewhere they did not ask to be.
func (m Model) SetAllCollapsed(collapsed bool) Model {
	blocks := make([]Block, len(m.blocks))
	copy(blocks, m.blocks)
	for i := range blocks {
		if blocks[i].Collapsible {
			blocks[i].Collapsed = collapsed
		}
	}
	m.blocks = blocks
	if m.Focused() {
		return m.ScrollToFocus()
	}
	m.clampOffset()
	return m
}

// ScrollToFocus brings the focused block fully into view, scrolling as
// little as possible. A block taller than the viewport is aligned to its
// top, because its header carries the identity.
func (m Model) ScrollToFocus() Model {
	if !m.Focused() || m.height <= 0 {
		m.clampOffset()
		return m
	}
	top := 0
	for i := 0; i < m.focus; i++ {
		top += m.blocks[i].Height(m.width)
	}
	bottom := top + m.blocks[m.focus].Height(m.width)

	switch {
	case top < m.offset:
		m.offset = top
	case bottom > m.offset+m.height:
		m.offset = bottom - m.height
		if m.offset > top {
			m.offset = top // taller than the screen: show its head
		}
	}
	m.follow = m.offset >= m.maxOffset()
	m.clampOffset()
	return m
}

// reindexFocus keeps focus on a surviving block after n blocks were
// dropped from the start. Focus never silently disappears: it moves to
// the oldest survivor, or back to the composer when nothing is left.
func (m *Model) reindexFocus(n int) {
	if m.focus < 0 {
		return
	}
	m.focus -= n
	if m.focus < 0 {
		m.focus = 0
	}
	if m.focus >= len(m.blocks) {
		m.focus = len(m.blocks) - 1
	}
	for i := range m.blocks {
		m.blocks[i].Focused = i == m.focus
	}
}

// FocusedText is the focused block's plain text, for the clipboard. It
// returns the body whether the block is collapsed or not: the user asked
// for the block's content, and a collapse marker is a view state, not
// part of what they meant to copy.
func (m Model) FocusedText() (string, bool) {
	if !m.Focused() {
		return "", false
	}
	b := m.blocks[m.focus]
	rows := make([]string, 0, len(b.Body)+1)
	if !b.Prose {
		// The collapse marker is dropped, not just trimmed. It is view
		// state, so keeping it would make the copied text differ
		// depending on whether the block happened to be open.
		header := strings.TrimSpace(strings.TrimPrefix(b.headerPlain(), b.collapseMarker()))
		rows = append(rows, header)
	}
	rows = append(rows, b.Body...)
	return strings.Join(rows, "\n"), true
}

// ToggleReasoning hides or shows reasoning blocks in the live window.
//
// Reasoning is collapsed by default in the wireframes, so this toggles
// the whole class at once rather than block by block. It changes only
// live blocks: what already reached scrollback is frozen.
func (m Model) ToggleReasoning() Model {
	m.hideReasoning = !m.hideReasoning
	blocks := make([]Block, len(m.blocks))
	copy(blocks, m.blocks)
	for i := range blocks {
		if blocks[i].Kind == uievent.KindReasoning && blocks[i].Collapsible {
			blocks[i].Collapsed = m.hideReasoning
		}
	}
	m.blocks = blocks
	return m
}

// ReasoningHidden reports the current state of the reasoning toggle.
func (m Model) ReasoningHidden() bool { return m.hideReasoning }

// SetHideReasoning explicitly sets whether reasoning blocks are hidden.
func (m Model) SetHideReasoning(hide bool) Model {
	if m.hideReasoning == hide {
		return m
	}
	return m.ToggleReasoning()
}
