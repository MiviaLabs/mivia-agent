package app

import (
	tea "charm.land/bubbletea/v2"

	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/clipboardwrite"
)

// Component-owned mouse drag-select. The router drives only the
// arm/drag/commit state machine; each selectable region (the transcript
// window, the composer body) owns its rect, its highlight, and its text
// extraction through sel.Selectable. This replaces the old frame-text
// selection, which re-rendered top.View() at release and cut plain text
// out of a string - fragile against styling, gutters, and any screen
// swap between press and release.
//
// Mouse capture itself is per-frame (View().MouseMode), gated on
// Opts.Mouse && AltScreen; this file never touches that. While capture
// is off the terminal's own selection works natively and none of these
// messages arrive.

// dragThreshold is the cell distance a held press must move before it
// counts as a drag rather than a click. Below it, the motion is jitter
// under a held button: swallowed, so a shaky click does not start a
// copy, but the press still reached the screen exactly as before.
const dragThreshold = 1

// dragState is one in-flight selection over a single region. The router
// keeps only the gesture bookkeeping; the component handle owns the
// anchor/focus cells (SetSelection mirrors them, Selection reads them
// back), so a value-copy of the router between press and motion - or a
// screen swap underneath a held button - can never lose the armed
// state. That discard was the defect class that broke the old frame-text
// selection.
type dragState struct {
	armed    bool // left button down, anchor recorded on the handle
	dragging bool // motion past threshold since arming
	region   sel.RegionID
	handle   *sel.Selectable // pointer INTO the live stack entry's field
}

// updateMouse advances the drag-select state machine. consume reports
// whether the router owns this Msg outright (drag motion, or a release
// that completed a drag); when false the caller still delivers msg to
// the top screen, unchanged from the pre-selection behavior - a plain
// click and its release keep firing exactly the actions they always
// did (block expand, composer cursor placement, dialog dismiss).
func (m *Model) updateMouse(msg tea.Msg) (cmd tea.Cmd, consume bool) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		return m.mousePress(msg)
	case tea.MouseMotionMsg:
		return m.mouseMotion(msg)
	case tea.MouseReleaseMsg:
		return m.mouseRelease(msg)
	case tea.MouseWheelMsg:
		if m.drag.armed {
			// Scrolling mid-drag moves the rows under the anchor; cancel
			// instead of copying drifted text, and swallow this notch so
			// the viewport does not jump while the user still holds the
			// button.
			m.cancelDrag()
			return nil, true
		}
		return nil, false
	}
	return nil, false
}

// mousePress arms a potential selection on a left press inside a
// region. The press is never consumed: an ordinary click keeps firing
// exactly what it fired before selection existed. A press without a
// prior release (a terminal that dropped it) simply re-arms.
func (m *Model) mousePress(msg tea.MouseClickMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return nil, false
	}
	m.cancelDrag()
	reg, ok := m.hitRegion(msg.X, msg.Y)
	if !ok {
		return nil, false // chrome, dialogs, dead space: ordinary click
	}
	cell := sel.FromScreen((*reg.Handle).SelectionRect(), msg.X, msg.Y)
	m.drag = dragState{armed: true, region: reg.ID, handle: reg.Handle}
	(*m.drag.handle).SetSelection(sel.Selection{Active: true, Anchor: cell, Focus: cell})
	return nil, false
}

// mouseMotion drags the armed selection. Sub-threshold motion is jitter
// under a held button: swallowed, but no drag starts. Past the
// threshold the motion updates the focus cell and is consumed, so it
// never reaches handleClick.
func (m *Model) mouseMotion(msg tea.MouseMotionMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft || !m.drag.armed {
		return nil, false // hover motion without our press: ignore
	}
	if m.drag.handle == nil {
		m.cancelDrag()
		return nil, true
	}
	handle := *m.drag.handle
	anchor := handle.Selection().Anchor
	cell := sel.FromScreen(handle.SelectionRect(), msg.X, msg.Y)
	if !m.drag.dragging {
		dx, dy := cell.Col-anchor.Col, cell.Row-anchor.Row
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx < dragThreshold && dy < dragThreshold {
			return nil, true // jitter under a held button; not a drag yet
		}
		m.drag.dragging = true
	}
	handle.SetSelection(sel.Selection{Active: true, Anchor: anchor, Focus: cell})
	return nil, true
}

// mouseRelease completes the gesture. A non-drag release resets the
// armed state and passes through inert, as it always was. A completed
// drag copies the region's SelectedText via OSC 52 and consumes the
// release either way, so no click-on-release double action survives.
func (m *Model) mouseRelease(msg tea.MouseReleaseMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return nil, false
	}
	if !m.drag.armed {
		return nil, false // release without our press: passthrough (inert today)
	}
	var text string
	if m.drag.dragging && m.drag.handle != nil {
		handle := *m.drag.handle
		anchor := handle.Selection().Anchor
		cell := sel.FromScreen(handle.SelectionRect(), msg.X, msg.Y)
		handle.SetSelection(sel.Selection{Active: true, Anchor: anchor, Focus: cell})
		text = handle.SelectedText()
	}
	m.cancelDrag()
	if text == "" {
		return nil, true
	}
	// OSC 52 gives no delivery confirmation; CopyTextMsg lets the
	// visible screens toast what was attempted. clipboardwrite is a
	// redundant local-only path: terminals that refuse OSC 52 outright
	// (VTE-based ones) still get a working copy when mivia runs
	// locally, and it is a no-op over SSH where OSC 52 remains the only
	// transport.
	return tea.Batch(
		tea.SetClipboard(text),
		func() tea.Msg { _ = clipboardwrite.Write(text); return nil },
		func() tea.Msg { return sel.CopyTextMsg{Text: text} },
	), true
}

// hitRegion finds the selectable region whose rect contains the
// absolute screen cell (x, y). Regions come from the top screen fresh
// at each event; a screen that offers none supports no selection. The
// returned handle is live: for a screen with regions it points into the
// stack slot's fields (conversation), and for a screen that is itself
// the Selectable it is the addressable slot pointer (the pager) - both
// reach the state that renders after deliverTop replaces the slot.
func (m Model) hitRegion(x, y int) (sel.RegionEntry, bool) {
	if len(m.stack) == 0 {
		return sel.RegionEntry{}, false
	}
	slot := &m.stack[len(m.stack)-1]
	if rs, ok := (*slot).(sel.RegionsScreen); ok {
		for _, reg := range rs.SelectionRegions() {
			if reg.Handle != nil && (*reg.Handle).SelectionRect().Contains(x, y) {
				return reg, true
			}
		}
		return sel.RegionEntry{}, false
	}
	if scr, ok := (*slot).(sel.Selectable); ok {
		r := scr.SelectionRect()
		if r.Height() > 0 && r.Width() > 0 && r.Contains(x, y) {
			h := sel.Selectable(scr)
			return sel.RegionEntry{ID: sel.RegionPager, Handle: &h}, true
		}
	}
	return sel.RegionEntry{}, false
}

// cancelDrag drops the in-flight selection and asks the owning region
// to clear its highlight. Safe when nothing is armed.
func (m *Model) cancelDrag() {
	if m.drag.handle != nil {
		(*m.drag.handle).ClearSelection()
	}
	m.drag = dragState{}
}

// cancelAllSelections clears every region's highlight across the whole
// screen stack. Used when capture is switched off mid-session so no
// stale reverse-video survives.
func (m *Model) cancelAllSelections() {
	m.cancelDrag()
	for _, sc := range m.stack {
		if rs, ok := sc.(sel.RegionsScreen); ok {
			for _, reg := range rs.SelectionRegions() {
				if reg.Handle != nil {
					(*reg.Handle).ClearSelection()
				}
			}
		}
	}
}
