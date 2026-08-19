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
		return m.syncFocus()
	}
	m.focus++
	return m.syncFocus()
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
		return m.syncFocus()
	}
	if m.focus > 0 {
		m.focus--
	}
	return m.syncFocus()
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
// false when nothing is focused, or when the focused block has no body
// to hide, so the caller can pass the key on instead of swallowing it.
//
// A toggle can change the block's height, so the window re-evicts. The
// evicted text is returned for the caller to commit, exactly as a push
// does: expanding a block near the top of a full window pushes older
// blocks into scrollback.
func (m Model) ToggleFocused() (Model, string, bool) {
	if !m.Focused() {
		return m, "", false
	}
	if !m.blocks[m.focus].Collapsible {
		return m, "", false
	}
	blocks := make([]Block, len(m.blocks))
	copy(blocks, m.blocks)
	blocks[m.focus].Collapsed = !blocks[m.focus].Collapsed
	m.blocks = blocks
	return m, m.evict(), true
}

// SetAllCollapsed collapses or expands every collapsible live block.
//
// Expanding is the dangerous direction. Every block grows at once, so
// the total can far exceed the budget, and eviction then commits the
// oldest blocks to scrollback. That is correct - the content is not lost
// - but it is why this returns the evicted text rather than discarding
// it.
func (m Model) SetAllCollapsed(collapsed bool) (Model, string) {
	blocks := make([]Block, len(m.blocks))
	copy(blocks, m.blocks)
	for i := range blocks {
		if blocks[i].Collapsible {
			blocks[i].Collapsed = collapsed
		}
	}
	m.blocks = blocks
	if !collapsed {
		// fitBlock re-collapses anything that cannot fit on its own, so
		// "expand all" never produces a block taller than the window.
		for i := range m.blocks {
			m.fitBlock(i)
		}
	}
	return m, m.evict()
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
