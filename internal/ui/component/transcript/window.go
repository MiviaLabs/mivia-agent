package transcript

import (
	"strings"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// SetSize records the terminal size and the rows other components claim,
// then evicts down to the new budget. A shrink evicts; a grow does not
// un-evict, because committed content is immutable.
func (m *Model) SetSize(width, height, reservedRows int) {
	m.width, m.height, m.reserved = width, height, reservedRows
}

// budget is the row count the live window may occupy.
//
// Until the first WindowSizeMsg the height is 0, so the budget is 0 and
// every finalized block evicts immediately. That is exactly the previous
// commit-on-finalize behaviour: the degradation is to a known-good path,
// not to a crash.
func (m Model) budget() int {
	b := m.height - m.reserved
	if b < 0 {
		return 0
	}
	return b
}

// push appends a finalized block and returns the text of everything the
// push evicted, oldest first, or "" when nothing was evicted.
func (m *Model) push(b Block) string {
	b.Collapsible = !b.Prose
	b.Collapsed = b.Collapsible && defaultCollapsed(b.Body)
	m.blocks = append(m.blocks, b)
	return m.evict()
}

// evict removes oldest blocks until the live window fits the budget, and
// returns their rendered text joined by newlines.
//
// The join matters: tea.Batch documents "no ordering guarantees", so
// emitting one print Cmd per evicted block would scramble scrollback.
// One string, already in order, cannot.
func (m *Model) evict() string {
	keep := len(m.blocks)
	used := 0
	// Walk newest-first, accumulating height. The first block that does
	// not fit, and everything older, is evicted.
	for i := len(m.blocks) - 1; i >= 0; i-- {
		h := m.blocks[i].Height()
		if used+h > m.budget() {
			keep = i + 1
			break
		}
		used += h
		keep = i
	}
	if keep == 0 {
		return ""
	}

	evicted := m.blocks[:keep]
	rendered := make([]string, 0, len(evicted))
	for _, b := range evicted {
		rendered = append(rendered, b.Render(m.Theme, m.Tier, m.width))
	}

	// Retain what left the live window, bounded, so the pager can still
	// reach it. The terminal owns the copy the user sees.
	m.ring = append(m.ring, evicted...)
	if over := len(m.ring) - uikitconfig.MaxTranscriptLines; over > 0 {
		m.ring = m.ring[over:]
	}

	m.blocks = append([]Block(nil), m.blocks[keep:]...)
	m.reindexFocus(keep)
	return strings.Join(rendered, "\n")
}

// reindexFocus keeps focus on a surviving block after n blocks were
// evicted from the front. Focus never silently disappears: it moves to
// the oldest surviving block instead.
//
// No upper clamp is needed. Focus is always within the pre-eviction
// slice, so focus <= L-1; after subtracting the n evicted blocks it is
// at most (L-1)-n, which is exactly the last index of the surviving
// slice. Code that sets focus from outside eviction - focus movement in
// a later wave - bounds it at its own call site.
func (m *Model) reindexFocus(n int) {
	if m.focus < 0 {
		return
	}
	m.focus -= n
	if m.focus < 0 {
		m.focus = 0
	}
}

// findLive returns the index of the live block for a tool call, or -1.
// Only the live window is searched: a block already in scrollback is
// frozen and cannot be updated.
func (m Model) findLive(callID string) int {
	if callID == "" {
		return -1
	}
	for i, b := range m.blocks {
		if b.CallID == callID {
			return i
		}
	}
	return -1
}

// updateLive mutates the live block for a tool call and re-evicts,
// because the change may have altered its height. It reports false when
// the block is no longer live, so the caller can push a fresh one.
func (m *Model) updateLive(callID string, fn func(*Block)) (string, bool) {
	i := m.findLive(callID)
	if i < 0 {
		return "", false
	}
	fn(&m.blocks[i])
	// A block that grew past the collapse threshold closes itself, the
	// same rule it would have got on first render.
	if m.blocks[i].Collapsible && defaultCollapsed(m.blocks[i].Body) {
		m.blocks[i].Collapsed = true
	}
	return m.evict(), true
}

// Retained returns the blocks that left the live window but are still
// held for the pager, oldest first.
func (m Model) Retained() []Block {
	out := make([]Block, len(m.ring))
	copy(out, m.ring)
	return out
}

// Live returns the blocks currently in the live window, oldest first.
func (m Model) Live() []Block {
	out := make([]Block, len(m.blocks))
	copy(out, m.blocks)
	return out
}
