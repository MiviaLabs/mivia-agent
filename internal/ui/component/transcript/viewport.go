package transcript

import (
	"fmt"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
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
	m.invalidateSelection()
	m.width, m.height = width, height
	if widthChanged {
		m.blocks = slices.Clone(m.blocks)
		for i := range m.blocks {
			m.blocks[i] = m.restyle(m.blocks[i])
		}
	}
	m.clampOffset()
}

// Width and Height report the current viewport size.
func (m Model) Width() int  { return m.width }
func (m Model) Height() int { return m.height }

// TotalRows is the height of the whole conversation at the current
// width: every block span plus the separators the layout places between
// sections (transcript-polish.md R1 - spacing follows turns, and the
// blank rows belong to the sequence, not to any block).
func (m Model) TotalRows() int {
	return m.totalLayoutRows(m.layout())
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
	// A block arriving mid-drag shifts the tail rows under the focus
	// cell; cancel rather than copy drifted text.
	m.invalidateSelection()
	if !m.follow {
		m.missed++
	}
	// A block is collapsible only when it has a body to collapse
	// (transcript-polish.md R3): push() used to force Collapsible on
	// every non-prose block, which painted the "v" marker over
	// header-only blocks with nothing under it. Blocks that arrive from
	// values.go already marked Collapsible keep that marking; prose
	// never takes a marker.
	if !b.Prose && len(b.Body) > 0 {
		b.Collapsible = true
	}
	if b.Collapsible && !b.Collapsed {
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
	// The rows that leave are exactly the survivor's new top: every row
	// above it, separators included.
	rows := m.layout()[over].top
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
// Geometry comes from layout(): separators between sections, the
// 2-column group indent on activity blocks, and coalesced leader rows
// for collapsed read-only runs (R1, R2). Only the blocks that intersect
// the viewport are styled; a block above or below it costs one Height
// call and nothing else.
func (m Model) Rows() []string {
	if m.height <= 0 {
		return nil
	}
	spans := m.layout()
	limit := m.offset + m.height
	out := make([]string, 0, m.height)
	emit := func(row int, line string) {
		if row >= m.offset && row < limit {
			out = append(out, line)
		}
	}
	row := 0
	for i := range m.blocks {
		s := spans[i]
		if s.height == 0 {
			continue // hidden inside a leader run; the head drew its row
		}
		if s.sepBefore {
			if row >= limit {
				break
			}
			emit(row, "")
			row++
		}
		if row >= limit {
			break
		}
		if row+s.height <= m.offset {
			row += s.height // entirely above the viewport: skip styling it
			continue
		}
		if s.runSize > 0 {
			emit(row, m.leaderRow(s, i))
			row++
			continue
		}
		for j, line := range m.renderSpanRows(m.blocks[i], s) {
			emit(row+j, line)
		}
		row += s.height
	}
	// The streaming tail is prose voice, so an activity block above it
	// gets the separating blank row (R1).
	if len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].Activity() {
		emit(row, "")
		row++
	}
	for _, line := range m.tailRows() {
		emit(row, line)
		row++
	}
	for len(out) < m.height {
		out = append(out, "")
	}
	if m.selState.Active {
		from, to := m.selState.Ordered()
		out = sel.HighlightLines(out, from, to)
	}
	return out
}

// View is the visible rows joined, which is what the screen draws.
func (m Model) View() string { return strings.Join(m.Rows(), "\n") }

// ToggleBlockAtScreenRow opens or closes the block whose HEADER draws on
// the given viewport row. y is relative to the transcript's own top row,
// the way a mouse event reports it. It reports false when the row holds
// no collapsible header, so a click can fall through.
//
// Only the header row acts. A click on a body row falls through, so
// expanded content is never collapsed by surprise - but the header is
// the affordance itself: it draws the "v"/">" marker, and a marker that
// only ever opens is a control the user cannot use to put the screen
// back the way it was. The keyboard toggle (space/enter on the focused
// block) is the same operation from the other input.
//
// Clicking a coalesced leader row (R2) opens the whole run: the row the
// user sees stands in for every member, so the click means "show me
// these". Closing that run again is per-member - collapse them and the
// layout re-coalesces them on its own.
func (m Model) ToggleBlockAtScreenRow(y int) (Model, bool) {
	if y < 0 || !m.FocusedRowVisible(y) {
		return m, false
	}
	row := m.offset + y
	spans := m.layout()
	for i := range m.blocks {
		s := spans[i]
		if s.height == 0 || row != s.top {
			continue
		}
		if s.runSize > 0 {
			m.expandRun(i)
			return m, true
		}
		if !m.blocks[i].Collapsible {
			return m, false
		}
		m.blocks = slices.Clone(m.blocks)
		m.blocks[i].Collapsed = !m.blocks[i].Collapsed
		m.clampOffset()
		return m, true
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
	// The rows under a live selection just moved; the anchor would copy
	// the wrong text, so scrolling cancels the drag.
	m.invalidateSelection()
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
// findLive returns the index of the live (not yet ended) block for a tool call, or -1.
func (m Model) findLive(callID string) int {
	if callID == "" {
		return -1
	}
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].CallID == callID {
			if m.blocks[i].Kind != uievent.KindToolEnd {
				return i
			}
			return -1
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
	// Re-apply push()'s collapsibility rule (transcript-polish.md R3):
	// a tool call starts header-only and is not collapsible, then grows
	// its body here as output and the end result merge in. Without the
	// promotion, the merged block would render a body with no marker to
	// open or close it.
	if !blk.Prose && len(blk.Body) > 0 {
		blk.Collapsible = true
	}
	// R2: a finished read-only lookup collapses by default whatever its
	// body size - the header already carries the path and the line
	// count - and consecutive collapsed lookups coalesce into one
	// leader row. Failed calls never coalesce.
	if !blk.Prose && len(blk.Body) > 0 && blk.Header.Role != theme.RoleDanger &&
		render.ReadOnlyToolClass(blk.Header.Label) != "" {
		blk.Collapsible = true
		blk.Collapsed = true
	} else if blk.Collapsible && defaultCollapsed(blk.Body) {
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
// dump the user asked for should not hide what they cannot see - which
// is also why leader runs never appear here: with every member expanded
// there is no run to coalesce, and each read keeps its own header.
func (m Model) Dump() string {
	rows := make([]string, 0, len(m.blocks))
	if m.dropped > 0 {
		rows = append(rows, fmt.Sprintf("[%d earlier blocks dropped from this transcript]", m.dropped))
	}
	blockRows, _ := m.ExpandedRows(m.width)
	rows = append(rows, blockRows...)
	if len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].Activity() {
		rows = append(rows, "")
	}
	rows = append(rows, m.tailRows()...)
	return strings.Join(rows, "\n")
}

// ExpandedRows renders every block expanded and unfocused at the given
// width, with the same section separators and group indents the live
// view uses, so the ctrl+o pager and the scrollback dump read exactly
// like the cockpit transcript. blockTops maps each block index to the
// first row its content occupies, for the pager's prompt jumps.
func (m Model) ExpandedRows(width int) (rows []string, blockTops []int) {
	dm := m
	dm.width = width
	dm.blocks = cloneBlocks(m.blocks)
	for i := range dm.blocks {
		dm.blocks[i].Collapsed = false
		dm.blocks[i].Focused = false
	}
	spans := dm.layout()
	rows = make([]string, 0, len(dm.blocks))
	blockTops = make([]int, len(dm.blocks))
	row := 0
	for i := range dm.blocks {
		s := spans[i]
		if s.sepBefore {
			rows = append(rows, "")
			row++
		}
		blockTops[i] = row
		lines := dm.renderSpanRows(dm.blocks[i], s)
		rows = append(rows, lines...)
		row += len(lines)
	}
	return rows, blockTops
}
