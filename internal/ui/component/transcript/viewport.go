package transcript

import (
	"fmt"
	"slices"
	"strings"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// The cockpit owns the whole drawing surface, so the transcript owns the
// whole conversation. Nothing is handed to the terminal.
//
// This replaces the live-window architecture, which existed only because
// the inline renderer had to keep View() shorter than the terminal and
// therefore committed finished blocks to native scrollback. In the
// cockpit there is no scrollback to commit to: the alternate screen has
// none. Blocks stay here, and the viewport draws the slice that fits.
//
// Cost is a function of the WINDOW, not of the session. heights() is
// arithmetic over every block, but only the blocks that intersect the
// viewport are ever styled, and styling is what costs.

// SetSize records the drawing surface. height is the row count the
// transcript itself may draw, with the composer and status row already
// subtracted by the caller.
//
// A width change also rebuilds every user-turn block: its rows are
// width-styled (selection background to the edge, marker, indent), so
// rows built at the old width either overflow the new one or stop
// short of it - a broken fill, not a reflow. Plain prose needs nothing
// here; it wraps at render time.
func (m *Model) SetSize(width, height int) {
	if width == m.width && height == m.height {
		return
	}
	widthChanged := width != m.width
	m.width, m.height = width, height
	if widthChanged {
		m.blocks = slices.Clone(m.blocks)
		for i := range m.blocks {
			if m.blocks[i].Kind == uievent.KindTurnStart {
				m.blocks[i].Body = userLines(m.Theme, m.Tier, width, m.blocks[i].Input)
			}
		}
	}
	m.clampOffset()
}

// Width and Height report the current viewport size.
func (m Model) Width() int  { return m.width }
func (m Model) Height() int { return m.height }

// heights is the rendered row count of every block, in order.
func (m Model) heights() []int {
	out := make([]int, len(m.blocks))
	for i := range m.blocks {
		out[i] = m.blocks[i].Height(m.width)
	}
	return out
}

// TotalRows is the height of the whole conversation at the current width.
func (m Model) TotalRows() int {
	total := 0
	for _, h := range m.heights() {
		total += h
	}
	return total
}

// maxOffset is the largest first-visible row that still fills the screen.
//
// An unmeasured viewport cannot scroll: with no height there is no
// window to scroll within, and returning TotalRows here would leave a
// non-zero offset on a transcript that has never been sized.
func (m Model) maxOffset() int {
	if m.height <= 0 {
		return 0
	}
	if over := m.TotalRows() - m.height; over > 0 {
		return over
	}
	return 0
}

func (m *Model) clampOffset() {
	if m.follow {
		m.offset = m.maxOffset()
		return
	}
	if m.offset > m.maxOffset() {
		m.offset = m.maxOffset()
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// push appends a finished block and keeps the conversation bounded.
//
// A block that arrives while the reader paused auto-follow COUNTS: the
// jump-to-bottom affordance must state what the reader missed, or the
func (m *Model) push(b Block) {
	if !m.follow {
		m.missed++
	}
	if !b.Collapsible && !b.Prose {
		b.Collapsible = true
		b.Collapsed = defaultCollapsed(b.Body)
	} else if b.Collapsible && !b.Collapsed {
		b.Collapsed = defaultCollapsed(b.Body)
	}
	m.blocks = append(slices.Clone(m.blocks), b)
	m.trim()
	m.clampOffset()
}

// NewWhilePaused is how many finished blocks arrived since auto-follow
// was paused. It is 0 while following.
func (m Model) NewWhilePaused() int { return m.missed }

// trim bounds the conversation at MaxTranscriptLines blocks and COUNTS
// what it dropped.
//
// The count is not decoration. A transcript that silently forgets its own
// start is a transcript that lies, and the user has no way to tell a
// short session from a truncated one. Section 8 of cockpit-research.md
// records this as the deliberate answer to "does the ring stay bounded".
func (m *Model) trim() {
	over := len(m.blocks) - uikitconfig.MaxTranscriptLines
	if over <= 0 {
		return
	}
	dropped := m.blocks[:over]
	rows := 0
	for _, b := range dropped {
		rows += b.Height(m.width)
	}
	m.dropped += over
	m.blocks = append([]Block(nil), m.blocks[over:]...)
	if !m.follow {
		// Keep the reader where they were reading: the rows above them
		// went away, so their offset must shrink by the same amount.
		m.offset -= rows
	}
	m.reindexFocus(over)
	m.clampOffset()
}

// Dropped is how many blocks the bound discarded from the start of the
// conversation. The view states it, so truncation is never silent.
func (m Model) Dropped() int { return m.dropped }

// Rows renders exactly the visible rows, padded to the viewport height.
//
// Only the blocks that intersect the viewport are styled. A block above
// or below it costs one Height call and nothing else.
func (m Model) Rows() []string {
	if m.height <= 0 {
		return nil
	}
	out := make([]string, 0, m.height)
	row := 0
	for i := range m.blocks {
		h := m.blocks[i].Height(m.width)
		if row+h <= m.offset {
			row += h // entirely above the viewport
			continue
		}
		if row >= m.offset+m.height {
			break // entirely below it
		}
		lines := strings.Split(m.blocks[i].Render(m.Theme, m.Tier, m.width), "\n")
		for j, line := range lines {
			at := row + j
			if at < m.offset || at >= m.offset+m.height {
				continue
			}
			out = append(out, line)
		}
		row += h
	}
	// The streaming tail sits below the last finished block.
	for _, line := range m.tailRows() {
		if row >= m.offset && row < m.offset+m.height {
			out = append(out, line)
		}
		row++
	}
	for len(out) < m.height {
		out = append(out, "")
	}
	return out
}

// View is the visible rows joined, which is what the screen draws.
func (m Model) View() string { return strings.Join(m.Rows(), "\n") }

// ExpandBlockAtScreenRow expands the collapsed block that draws on the
// given viewport row, if any. y is relative to the transcript's own
// top row, the way a mouse event reports it. It reports false when the
// row holds no collapsed block header, so a click can fall through.
//
// Only the header row of a block is clickable: the body of a collapsed
// block is not on screen, and clicking expanded content must not
// collapse it by surprise - the keyboard toggle stays the only way
// back.
func (m Model) ExpandBlockAtScreenRow(y int) (Model, bool) {
	if y < 0 || !m.FocusedRowVisible(y) {
		return m, false
	}
	row := m.offset + y
	top := 0
	for i := range m.blocks {
		h := m.blocks[i].Height(m.width)
		if row == top && m.blocks[i].Collapsible && m.blocks[i].Collapsed {
			blocks := make([]Block, len(m.blocks))
			copy(blocks, m.blocks)
			blocks[i].Collapsed = false
			m.blocks = blocks
			m.clampOffset()
			return m, true
		}
		top += h
	}
	return m, false
}

// FocusedRowVisible reports whether y is inside the viewport.
func (m Model) FocusedRowVisible(y int) bool {
	return m.height > 0 && y < m.height
}

// Following reports whether new output pulls the view to the bottom.
func (m Model) Following() bool { return m.follow }

// Offset is the first visible row of the conversation.
func (m Model) Offset() int { return m.offset }

// ScrollBy moves the viewport by delta rows. Scrolling up pauses
// auto-follow, so streaming output does not drag the reader away from
// what they stopped to read. Reaching the bottom resumes it and clears
// the missed count: the reader is caught up.
func (m Model) ScrollBy(delta int) Model {
	if delta == 0 {
		return m
	}
	m.follow = false
	m.offset += delta
	if m.offset < 0 {
		m.offset = 0
	}
	if m.offset >= m.maxOffset() {
		m.offset = m.maxOffset()
		m.follow = true // back at the bottom: follow again
		m.missed = 0
	}
	return m
}

// ScrollToTop jumps to the start of the conversation.
func (m Model) ScrollToTop() Model {
	m.follow = false
	m.offset = 0
	return m
}

// ScrollToBottom jumps to the newest output and resumes auto-follow.
func (m Model) ScrollToBottom() Model {
	m.follow = true
	m.missed = 0
	m.offset = m.maxOffset()
	return m
}

// PageBy moves by whole screens. fraction 2 is a half page.
func (m Model) PageBy(pages, fraction int) Model {
	step := m.height / max(fraction, 1)
	if step < 1 {
		step = 1
	}
	return m.ScrollBy(pages * step)
}

// findLive returns the index of the block for a tool call, or -1.
func (m Model) findLive(callID string) int {
	if callID == "" {
		return -1
	}
	for i := range m.blocks {
		if m.blocks[i].CallID == callID {
			return i
		}
	}
	return -1
}

// updateLive mutates the block for a tool call in place. It reports false
// when no such block exists, so the caller can push a fresh one.
//
// Unlike the inline version this cannot fail by height: the block may
// grow to any size, because the viewport scrolls instead of evicting.
func (m *Model) updateLive(callID string, fn func(*Block)) bool {
	i := m.findLive(callID)
	if i < 0 {
		return false
	}
	m.blocks = slices.Clone(m.blocks)
	blk := m.blocks[i]
	fn(&blk)
	if blk.Collapsible && defaultCollapsed(blk.Body) {
		blk.Collapsed = true
	}
	m.blocks[i] = blk
	m.clampOffset()
	return true
}

// Blocks returns the whole conversation, oldest first.
func (m Model) Blocks() []Block { return cloneBlocks(m.blocks) }

// cloneBlocks deep-copies, including each Body.
//
// Copying the Block struct alone is not enough: Body is a slice, so the
// copies share one backing array, and a caller writing to a returned row
// would write through into the transcript.
func cloneBlocks(in []Block) []Block {
	out := make([]Block, len(in))
	copy(out, in)
	for i := range out {
		out[i].Body = append([]string(nil), in[i].Body...)
	}
	return out
}

// Dump renders the WHOLE conversation, every block expanded, at the
// current width.
//
// This is the content behind cockpit-research.md rule 6.3: the cockpit
// takes the terminal's drawing surface, so it must be able to hand the
// conversation back. Writing this into native scrollback returns the
// session to grep, tmux copy-mode, and the terminal's own find.
//
// Collapsed blocks are expanded here. A collapse is a view state, and a
// dump the user asked for should not hide what they cannot see.
func (m Model) Dump() string {
	rows := make([]string, 0, len(m.blocks))
	if m.dropped > 0 {
		rows = append(rows, fmt.Sprintf("[%d earlier blocks dropped from this transcript]", m.dropped))
	}
	for _, b := range m.blocks {
		b.Collapsed = false
		b.Focused = false
		rows = append(rows, b.Render(m.Theme, m.Tier, m.width))
	}
	rows = append(rows, m.tailRows()...)
	return strings.Join(rows, "\n")
}
