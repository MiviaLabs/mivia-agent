package transcript

import (
	"strings"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// SetSize records the terminal size and the rows other components claim,
// then evicts down to the new budget. It returns the text of everything
// the resize evicted, oldest first, for the caller to commit.
//
// A shrink evicts; a grow does not un-evict, because committed content is
// immutable. The return value is not optional: evicting silently would
// drop the evicted rows instead of printing them to scrollback.
//
// Every caller must also call this whenever the RESERVED rows change, not
// only on a terminal resize. Arming the status line or an approval prompt
// claims rows the transcript was budgeting for.
func (m *Model) SetSize(width, height, reservedRows int) string {
	m.width, m.height, m.reserved = width, height, reservedRows
	for i := range m.blocks {
		m.fitBlock(i)
	}
	return m.evict()
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
	m.fitBlock(len(m.blocks) - 1)
	return m.evict()
}

// fitBlock collapses a block that cannot fit the budget on its own.
//
// Without this a block taller than the whole window would evict itself
// the moment it was pushed or grew. For a tool call still in flight that
// is worse than it sounds: the block would freeze into scrollback reading
// "running", and the tool.end event would then find no live block and
// push a SECOND header for the same call.
//
// Collapsing bounds it to its header row instead, which fits any budget
// of one row or more. Prose has no header to collapse into, so it is left
// alone and the budget absorbs it.
func (m *Model) fitBlock(i int) {
	if m.budget() <= 0 {
		// Unmeasured window. Every block evicts on push anyway, and
		// collapsing them all first would commit headers with their bodies
		// hidden - content the user would never see in any window.
		return
	}
	b := &m.blocks[i]
	if !b.Collapsible || b.Collapsed {
		return
	}
	if b.Height(m.width) > m.budget() {
		b.Collapsed = true
	}
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
		h := m.blocks[i].Height(m.width)
		if used+h > m.budget() {
			keep = i + 1
			break
		}
		used += h
		keep = i
	}
	// The newest block needs no special case here. fitBlock has already
	// collapsed it to its single header row if it could not fit, so in a
	// measured window it always survives, and a block still in flight can
	// never commit itself mid-flight. Prose is the deliberate exception:
	// it cannot collapse, and it has nothing to interact with, so letting
	// it evict straight to scrollback loses nothing and gains native
	// selection.
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
// Both clamps are load-bearing. The lower one catches focus on an
// evicted block. The upper one catches the case where EVERY block was
// evicted: the live window is then empty, and there is no index to hold,
// so focus returns to the composer.
func (m *Model) reindexFocus(n int) {
	if m.focus < 0 {
		return
	}
	m.focus -= n
	if m.focus < 0 {
		m.focus = 0
	}
	if m.focus >= len(m.blocks) {
		m.focus = len(m.blocks) - 1 // -1 when nothing is live: the composer
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
	m.fitBlock(i)
	return m.evict(), true
}

// Retained returns the blocks that left the live window but are still
// held for the pager, oldest first.
func (m Model) Retained() []Block { return cloneBlocks(m.ring) }

// Live returns the blocks currently in the live window, oldest first.
func (m Model) Live() []Block { return cloneBlocks(m.blocks) }

// cloneBlocks deep-copies, including each Body.
//
// Copying the Block struct alone is not enough: Body is a slice, so the
// copies share one backing array, and a caller writing to a returned row
// would write through into the live window or the retained ring.
func cloneBlocks(in []Block) []Block {
	out := make([]Block, len(in))
	copy(out, in)
	for i := range out {
		out[i].Body = append([]string(nil), in[i].Body...)
	}
	return out
}
