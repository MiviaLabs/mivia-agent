package conversation

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// handleWheel applies one mouse-wheel notch. Wheel events scroll the
// conversation; CockpitScrollLines is the multiplier: terminals
// disagree on how many events one physical notch produces, and some
// amplify while others send exactly one
// (docs/design/cockpit-research.md rule 6.6).
func (s Screen) handleWheel(msg tea.MouseWheelMsg) (app.Screen, tea.Cmd) {
	step := uikitconfig.CockpitScrollLines
	if msg.Button == tea.MouseWheelUp {
		step = -step
	}
	if s.approval.Active() {
		// The approval prompt is modal for keys; the wheel follows it,
		// scrolling the diff preview instead of the transcript behind
		// it.
		s.approval = s.approval.ScrollBy(step)
		return s, nil
	}
	if s.panel.dialog {
		if s.panel.dialogAgent != "" && s.thread != nil && s.threadID == s.panel.dialogAgent {
			s.thread.transcript = s.thread.transcript.ScrollBy(step)
			return s, nil
		}
		s.scrollPanel(step)
		return s, nil
	}
	s.transcript = s.transcript.ScrollBy(step)
	return s, nil
}

// handleClick routes one mouse click. The row layout mirrors View:
// transcript rows, then the approval prompt, then the composer bar (with
// the completion popup overlaid on the rows just above it), and finally
// the status row at the bottom.
//
// Left button only. A click on a collapsed block header expands it; a
// click on a completion row accepts it; a click on the input line
// places the cursor. With the panel open wide the chat column keeps
// its normal geometry (the split draws no frame around it), and the
// nav pane and its rule carry no click actions; narrow, the list
// covers the transcript area and answers nothing.
func (s Screen) handleClick(msg tea.MouseClickMsg) (app.Screen, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return s, nil
	}
	x, y := msg.X, msg.Y
	topGutter := 0
	bottomGutter := 0
	if s.height >= 3 && !s.embedded {
		topGutter = 1
		bottomGutter = 1
	}

	if next, cmd, handled := s.handleModalClick(x, y, topGutter); handled {
		return next, cmd
	}
	if next, cmd, handled := s.handleTopbarDoubleClick(x, y, topGutter); handled {
		return next, cmd
	}

	transcriptTop := topGutter + s.topbar.Height() + 1
	if s.panelIsSplit() {
		// Column 0 is the gutter; the reading column runs to the rule.
		reading, _ := render.SplitWidths(contentWidth(s.width))
		if x > reading {
			return s.handleNavClick(y - topGutter)
		}
	} else if s.panel.open {
		if y-transcriptTop < s.transcriptHeight() {
			return s, nil // the list covers the transcript area
		}
	}
	transcriptRows := s.transcriptHeight()
	// The status row sits at the screen bottom, so the input row sits
	// above it. The composer owns the exact numbers (InputRowFromBottom, InputColumnOffset).
	statusRow := s.height - bottomGutter - 1
	inputRow := statusRow - s.composer.InputRowFromBottom()
	colOffset := s.composer.InputColumnOffset()
	menuRows := s.composer.MenuRows()

	// The popup ends on the row above the bar's first row, which is
	// Height() rows above the status row - not a fixed distance above the
	// input row, since the textarea grows to several rows and inputRow is
	// its last one (overlayComposerPopup anchors on the same row).
	menuStart := statusRow - s.composer.Height() - menuRows

	switch {
	// The completion popup (slash or mention: menuRows > 0 for either) is
	// an overlay drawn over the transcript's rows directly above the bar
	// (menuStart .. menuStart+menuRows-1), so it is tested BEFORE the
	// transcript: a click on it is a click on the popup, whatever the
	// transcript drew underneath.
	case menuRows > 0 && y >= menuStart && y < menuStart+menuRows:
		comp := s.composer
		if comp.MenuClickRow(y - menuStart) {
			s.composer = comp
		}
	case y-transcriptTop < transcriptRows && s.transcriptShown():
		next, expanded := s.transcript.ExpandBlockAtScreenRow(y - transcriptTop)
		if expanded {
			s.transcript = next
		}
	case y == inputRow:
		// One column in for the gutter, then the composer's own column
		// offset: the click lands on the input's column space.
		comp := s.composer
		comp.ClickToColumn(x - 1 - colOffset)
		s.composer = comp
	}
	return s, nil
}

func (s Screen) handleModalClick(x, y, topGutter int) (Screen, tea.Cmd, bool) {
	if s.overlay != "" {
		s.overlay = ""
		return s, tea.ClearScreen, true
	}
	if s.modelPicker != nil || s.agentPicker != nil || s.sessionPicker != nil || s.palettePicker != nil || s.effortPicker != nil || s.panel.dialog {
		dw, dh := s.dialogSize()
		transcriptTop := topGutter + s.topbar.Height() + 1
		localX, localY := x-1, y-transcriptTop
		if s.panelIsSplit() && localX >= dw {
			scr, cmd := s.handleNavClick(y - topGutter)
			if conv, ok := scr.(Screen); ok {
				return conv, cmd, true
			}
			return s, cmd, true
		}
		if render.DialogHitsClose(dw, dh, 10, localX, localY) || render.DialogHitsBackdrop(dw, dh, 10, localX, localY) {
			s.modelPicker = nil
			s.agentPicker = nil
			s.sessionPicker = nil
			s.palettePicker = nil
			s.effortPicker = nil
			s.panel.dialog = false
			s.panel.dialogAgent = ""
			return s, tea.ClearScreen, true
		}
		return s, nil, true
	}
	return s, nil, false
}

func (s Screen) handleTopbarDoubleClick(x, y, topGutter int) (Screen, tea.Cmd, bool) {
	if y != topGutter {
		return s, nil, false
	}
	clickCol := x - 1
	hitsModel := s.topbar.HitsModel(clickCol)
	hitsActivity := s.topbar.HitsActivity(clickCol)

	if !hitsModel && !hitsActivity {
		return s, nil, false
	}

	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	isDoubleClick := !s.lastClickTime.IsZero() && now.Sub(s.lastClickTime) < 500*time.Millisecond && abs(x-s.lastClickX) <= 1 && y == s.lastClickY
	s.lastClickTime = now
	s.lastClickX = x
	s.lastClickY = y
	if isDoubleClick {
		if hitsModel {
			scr, cmd := s.runSlashCommand("/model")
			return scr.(Screen), cmd, true
		}
		if hitsActivity {
			if !s.panel.open {
				s.panel.openPanel()
				s.transcript = s.transcript.ClearFocus()
				s.syncTopbarModel()
				s.reflow()
				return s, tea.ClearScreen, true
			}
		}
	}
	return s, nil, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// transcriptShown reports whether the transcript itself is the content
// of its area right now - not a picker dialog, the overlay, or the
// panel's content dialog. Clicks that hit the area while something else
// draws there must not act on the transcript hidden behind it.
func (s Screen) transcriptShown() bool {
	return s.modelPicker == nil && s.agentPicker == nil && s.sessionPicker == nil && s.palettePicker == nil && s.effortPicker == nil && s.overlay == "" && !s.panel.dialog
}
