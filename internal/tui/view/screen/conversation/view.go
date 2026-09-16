package conversation

import (
	"strings"
)

// View draws the cockpit. The transcript area fills the surface between
// the top bar's margin and the chrome below (approval prompt, composer,
// status row), and the status row is the last row. With the panel open
// wide, that whole assembly moves into the split's left reading pane
// with the file list in the right nav pane (panelFrameRows); narrow, the
// list replaces the transcript area and the chrome keeps its place.
// The embedded subagent-thread construction draws the same assembly
// minus the top bar, sized to the dialog body it renders inside.
//
// The transcript always returns exactly its own height, padded, so
// nothing below it moves as output streams in (ux-rules.md rule 2.8).
func (s Screen) View() string {
	var lines []string
	if !s.embedded && s.panelIsSplit() {
		lines = s.panelFrameRows()
	} else {
		if !s.embedded {
			// The top bar may draw a second breadcrumb row (topbar.Height
			// accounts for it); its rows land as frame rows, then the margin.
			lines = append(lines, strings.Split(s.topbar.View(), "\n")...)
			lines = append(lines, "")
		}
		switch {
		case s.modelPicker != nil || s.agentPicker != nil || s.sessionPicker != nil || s.palettePicker != nil || s.effortPicker != nil || s.login != nil || s.overlay != "":
			lines = append(lines, s.centerRows()...)
		case !s.embedded && s.panel.open:
			lines = append(lines, s.narrowPanelRows()...)
		case !s.embedded && s.transcript.Empty():
			lines = append(lines, s.welcome.Rows(s.chatWidth(), s.transcriptHeight())...)
		default:
			tRows := s.transcript.Rows()
			tH := s.transcriptHeight()
			if len(tRows) > tH {
				tRows = tRows[:tH]
			}
			lines = append(lines, tRows...)
		}
		lines = append(lines, s.chatTailRows()...)
	}

	lines = s.overlayComposerPopup(lines)

	if s.height > 0 {
		innerH := s.contentHeight()
		if len(lines) > innerH {
			lines = lines[:innerH]
		}
	}
	return s.gutter(lines)
}

// reservedRows is how many rows the chrome below the transcript claims.
//
// The status row is PERMANENT in the cockpit. The inline design had no
// persistent status bar because every row it drew pushed the transcript
// up; a cockpit owns a fixed surface, so a reserved row costs nothing
// that moves. A row that is always there also never reflows the
// transcript when it changes (docs/design/ux-rules.md rule 2.7).
func (s Screen) reservedRows() int {
	// the top bar, a one-row margin under it so content never touches its
	// edge, the composer (its completion popup is an overlay and claims no
	// row), and the status row. The embedded
	// subagent-thread construction has no top bar: the dialog frame it
	// renders inside is the chrome above it.
	rows := 1
	if !s.hideComposer {
		rows += s.composer.Height()
	}
	if !s.embedded {
		rows += s.topbar.Height() + 1
	}
	if s.approval.Active() {
		rows += s.approval.Height() // bordered box: title, optional diff preview, hint, and the border rows
	}
	if s.history.Active() {
		rows += s.history.Height()
	}
	if s.queueOverlay.Active() {
		rows += s.queueOverlay.Height()
	}
	if s.blackboard.Active() {
		rows += s.blackboard.Height()
	}
	return rows
}
