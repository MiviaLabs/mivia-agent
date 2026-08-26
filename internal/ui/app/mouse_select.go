package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// selection is the router's mouse drag-select state. It exists only
// while Options.Mouse captures the terminal (rule 7.1): capture
// replaces the terminal's own click-and-drag selection, so this
// restores "highlight and copy" for that case only. Coordinates are
// screen rows/cols of the frame the top screen just rendered - the
// same grid the terminal draws - not any screen's internal model.
type selection struct {
	pressed  bool
	dragging bool
	anchorX  int
	anchorY  int
	curX     int
	curY     int
}

// dragThreshold is the cell distance a held press must move before it
// counts as a drag rather than a click. Below it, the press is left to
// reach the top screen exactly as before (block expand, composer
// cursor placement, dialog dismiss all still fire on the press).
const dragThreshold = 1

// updateMouseSelect advances the drag-select state machine. handled
// reports whether the router owns this Msg outright (a release that
// completed a drag, or motion swallowed while a drag is live/settling);
// when handled is false the caller still delivers msg to the top
// screen, unchanged from the pre-selection behavior - a plain click and
// its release keep firing exactly the actions they always did.
func (m Model) updateMouseSelect(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil, false
		}
		m.sel = selection{pressed: true, anchorX: msg.X, anchorY: msg.Y, curX: msg.X, curY: msg.Y}
		return m, nil, false

	case tea.MouseMotionMsg:
		if !m.sel.pressed || msg.Button != tea.MouseLeft {
			return m, nil, false
		}
		m.sel.curX, m.sel.curY = msg.X, msg.Y
		if !m.sel.dragging {
			dx, dy := msg.X-m.sel.anchorX, msg.Y-m.sel.anchorY
			if dx < 0 {
				dx = -dx
			}
			if dy < 0 {
				dy = -dy
			}
			if dx < dragThreshold && dy < dragThreshold {
				return m, nil, true // jitter under a held button; not a drag yet
			}
			m.sel.dragging = true
		}
		return m, nil, true

	case tea.MouseReleaseMsg:
		if !m.sel.pressed {
			return m, nil, false
		}
		m.sel.curX, m.sel.curY = msg.X, msg.Y
		wasDragging := m.sel.dragging
		ax, ay, bx, by := m.sel.anchorX, m.sel.anchorY, m.sel.curX, m.sel.curY
		m.sel = selection{}
		if !wasDragging {
			// An ordinary click's release: nothing to complete, same as
			// before there was any selection state (conversation.go's
			// documented "actions fire on click, not release").
			return m, nil, false
		}
		text := selectedText(m.currentFrame(), ax, ay, bx, by)
		if text == "" {
			return m, nil, true
		}
		return m, tea.SetClipboard(text), true
	}
	return m, nil, false
}

// currentFrame is the top screen's rendered content, split into rows,
// re-fetched at drag-release rather than cached from an earlier View()
// call: View has a value receiver and returns no Model, so nothing from
// it survives into the next Update. Re-rendering here costs one extra
// View call per release, not per motion event.
func (m Model) currentFrame() []string {
	top, ok := m.top()
	if !ok {
		return nil
	}
	return strings.Split(top.View(), "\n")
}

// selectedText extracts the plain-text stream selection between two
// screen points from frame, normalizing so the earlier point in
// reading order comes first. This is a stream selection (terminal
// click-drag semantics), not a block selection: the anchor row runs
// from its column to the row's end, inner rows are taken whole, and the
// end row runs from its start to the release column.
func selectedText(frame []string, ax, ay, bx, by int) string {
	if ay > by || (ay == by && ax > bx) {
		ax, ay, bx, by = bx, by, ax, ay
	}
	if ay < 0 {
		ay = 0
	}
	if by >= len(frame) {
		by = len(frame) - 1
	}
	if by < ay {
		return ""
	}
	var sb strings.Builder
	for row := ay; row <= by; row++ {
		line := frame[row]
		w := ansi.StringWidth(line)
		left, right := 0, w
		if row == ay {
			left = clampInt(ax, 0, w)
		}
		if row == by {
			right = clampInt(bx+1, 0, w)
		}
		if right < left {
			right = left
		}
		sb.WriteString(ansi.Strip(ansi.Cut(line, left, right)))
		if row != by {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// highlightSelection wraps the selected cells of content in reverse
// video (SGR 7 / SGR 27), the same live-drag feedback a terminal's own
// selection gives, so the user sees what release will copy. It is
// applied to the already-rendered frame, not to any component's own
// styling - the router has no other view into "everything on screen".
func highlightSelection(content string, ax, ay, bx, by int) string {
	if ay > by || (ay == by && ax > bx) {
		ax, ay, bx, by = bx, by, ax, ay
	}
	lines := strings.Split(content, "\n")
	for row := ay; row <= by && row < len(lines); row++ {
		if row < 0 {
			continue
		}
		line := lines[row]
		w := ansi.StringWidth(line)
		left, right := 0, w
		if row == ay {
			left = clampInt(ax, 0, w)
		}
		if row == by {
			right = clampInt(bx+1, 0, w)
		}
		if right <= left {
			continue
		}
		prefix := ansi.Cut(line, 0, left)
		middle := ansi.Cut(line, left, right)
		suffix := ansi.Cut(line, right, w)
		lines[row] = prefix + "\x1b[7m" + middle + "\x1b[27m" + suffix
	}
	return strings.Join(lines, "\n")
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
