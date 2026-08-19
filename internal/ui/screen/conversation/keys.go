package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/themepicker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

// handleKey routes one key press through the keymap contexts, most
// specific first.
//
// The order IS the precedence rule, and it is the whole reason the
// keymap is a table rather than a switch. Enter means accept-completion,
// approve, toggle-block, open-file or send depending on what is on
// screen; Esc means dismiss-menu, defocus-panel, unfocus, or
// cancel-the-turn. Resolving that by asking each context in order keeps
// one answer per state, and keymap.Collisions proves no context answers
// twice.
//
//	approval  - a pending tool call blocks everything else
//	picker    - an open /model or /agents dialog claims every key
//	panel     - while the files panel's list holds focus (its content
//	            dialog first: any key closes it, three keys survive)
//	overlay   - the help overlay clears on any key
//	transcript- only while a block holds the focus
//	global    - always available
//	composer  - the resting state, and the fallback for plain text
//
// The panel and the overlay cannot both claim a key: the overlay opens
// only from the composer's "?" and /help, both of which require the
// composer's focus, which the panel's list never has at that moment.
func (s Screen) handleKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	if next, cmd, handled := s.handleApprovalKey(msg); handled {
		return next, cmd
	}

	if next, cmd, handled := s.handleOpenPickerKey(msg); handled {
		return next, cmd
	}

	// Any key other than a second ctrl+c ends the quit-armed state, so a
	// stray keystroke cannot leave the session one press from exiting.
	// This runs before the branches that return early for their keys
	// (the panel's list, the overlay) for the same reason.
	if msg.String() != "ctrl+c" {
		s.quitArmed = false
	}

	if next, cmd, handled := s.handlePanelKey(msg); handled {
		return next, cmd
	}

	// Any key dismisses an overlay, and does nothing else. An overlay
	// covers the transcript, so acting on the key underneath it would act
	// on something the user cannot see. Clearing the screen on dismissal
	// matters too: the overlay drew over content the transcript/composer
	// never redrew, and a diffing renderer that fails to blank a row
	// neither frame wrote to leaves the overlay bleeding through.
	if s.overlay != "" {
		s.overlay = ""
		return s, tea.ClearScreen
	}

	key := msg.String()
	if s.composer.MenuActive() {
		if id, ok := s.keys.Match(keymap.ContextCompletion, key); ok {
			return s.completionAction(id)
		}
	}
	if s.transcript.Focused() {
		if id, ok := s.keys.Match(keymap.ContextTranscript, key); ok {
			return s.transcriptAction(id)
		}
	}
	if id, ok := s.keys.Match(keymap.ContextGlobal, key); ok {
		if next, cmd, handled := s.globalAction(id); handled {
			return next, cmd
		}
	}
	if id, ok := s.keys.Match(keymap.ContextComposer, key); ok {
		if next, cmd, handled := s.composerAction(id); handled {
			return next, cmd
		}
	}

	next, cmd := s.composer.Update(msg)
	s.composer = next
	return s, cmd
}

// handleApprovalKey routes a key when the approval prompt is active.
func (s Screen) handleApprovalKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if !s.approval.Active() {
		return s, nil, false
	}
	// Scroll keys window the inline diff preview. Only the scroll IDs
	// are consumed here: a matched decision key (o/a/d/esc/enter) must
	// still reach approval.Update below, or this branch would silently
	// eat deny.
	if id, ok := s.keys.Match(keymap.ContextApproval, msg.String()); ok {
		switch id {
		case keymap.IDScrollUp:
			s.approval = s.approval.ScrollBy(-1)
			return s, nil, true
		case keymap.IDScrollDown:
			s.approval = s.approval.ScrollBy(1)
			return s, nil, true
		}
	}
	// ctrl+c stays the emergency exit even under the modal: quit()
	// clears the approval and cancels the turn itself. Everything
	// else the prompt does not answer is swallowed - a pending tool
	// call blocks the rest of the screen.
	if id, ok := s.keys.Match(keymap.ContextGlobal, msg.String()); ok && id == keymap.IDQuit {
		next, cmd, _ := s.quit()
		return next, cmd, true
	}
	next, cmd := s.approval.Update(msg)
	s.approval = next
	return s, cmd, true
}

// handleOpenPickerKey routes a key to an open /model or /agents
// picker: it is a modal, exactly like the approval prompt, and the
// transcript/composer beneath it must not react to a key the user
// aimed at the picker. handled is false when no picker is open.
func (s Screen) handleOpenPickerKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if s.modelPicker != nil {
		next, cmd := s.handleModelPickerKey(msg)
		return next, cmd, true
	}
	if s.agentPicker != nil {
		next, cmd := s.handleAgentPickerKey(msg)
		return next, cmd, true
	}
	return s, nil, false
}

// handlePanelKey routes a key into the open, focused panel: its content
// dialog first (any key closes it; the view toggle, the half-page
// scrolls, and ctrl+c survive), then the list. The list is a focusable
// pane, not a modal: ctrl+c, ctrl+n, ctrl+t, and ctrl+o stay live over
// it (a ctrl-modified key carries no Text, so the picker would silently
// swallow them), esc hands focus back to the composer WITHOUT closing
// the panel, the files bindings navigate, and every other key feeds the
// list's filter. handled is false when the panel does not hold focus,
// so the ordinary chat flow resumes.
func (s Screen) handlePanelKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if !s.panel.open || !s.panel.focused {
		return s, nil, false
	}
	if msg.String() == "ctrl+c" {
		next, cmd, _ := s.quit()
		return next, cmd, true
	}
	if id, ok := s.keys.Match(keymap.ContextGlobal, msg.String()); ok {
		switch id {
		case keymap.IDPanelToggle:
			// The middle state of ctrl+n's cycle: focus returns to the
			// composer; the panel stays open and live beside it.
			s.panelFocus(false)
			return s, nil, true
		case keymap.IDThemeDialog, keymap.IDOpenPager:
			// Still reachable: the conversation is on screen beside the
			// panel, so its global surfaces stay one key away.
			return s, nil, false
		}
	}
	if s.panel.dialog {
		return s.panelDialogKey(msg), nil, true
	}
	if id, ok := s.keys.Match(keymap.ContextFiles, msg.String()); ok {
		switch id {
		case keymap.IDCancel:
			s.panelFocus(false)
			return s, nil, true
		case keymap.IDFileToggleView:
			s.panel.sourceView = !s.panel.sourceView
			s.panel.offset = 0
			return s, nil, true
		case keymap.IDPagerHalfUp:
			s.scrollPanel(-1)
			return s, nil, true
		case keymap.IDPagerHalfDown:
			s.scrollPanel(1)
			return s, nil, true
		case keymap.IDPagerRowUp:
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case keymap.IDPagerRowDown:
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		}
	}
	// Everything else feeds the list: arrows move the selection, typing
	// filters, and Enter selects. The SelectMsg is consumed here, not
	// returned as a Cmd - the dialog is this screen's own state, not a
	// routed message.
	next, cmd := s.panel.list.Update(msg)
	s.panel.list = next
	s.panel.offset = 0 // a moved selection restarts the content at its top
	if cmd != nil {
		if _, ok := cmd().(picker.SelectMsg); ok {
			s.panel.dialog, s.panel.offset = true, 0
		}
	}
	return s, nil, true
}

// panelDialogKey applies the content dialog's one rule: any key closes
// it back to the list, except the view toggle, the half-page scrolls,
// and the emergency exit (which closes it and runs the ordinary quit
// flow, so the second-press warning lands on a visible status row).
func (s Screen) panelDialogKey(msg tea.KeyPressMsg) app.Screen {
	if msg.String() == "ctrl+c" {
		s.panel.dialog = false
		next, _, _ := s.quit()
		return next
	}
	if id, ok := s.keys.Match(keymap.ContextFiles, msg.String()); ok {
		switch id {
		case keymap.IDFileToggleView:
			s.panel.sourceView = !s.panel.sourceView
			s.panel.offset = 0
			return s
		case keymap.IDPagerHalfUp:
			s.scrollPanel(-1)
			return s
		case keymap.IDPagerHalfDown:
			s.scrollPanel(1)
			return s
		}
	}
	s.panel.dialog, s.panel.offset = false, 0
	return s
}

// panelFocus moves keyboard focus between the panel's list and the
// composer. Handing focus back closes a pending content dialog - the
// dialog belongs to browsing - and taking it clears any transcript
// block focus, so the two focus axes never overlap.
func (s *Screen) panelFocus(focused bool) {
	s.panel.focused = focused
	if focused {
		s.transcript = s.transcript.ClearFocus()
	} else {
		s.panel.dialog = false
	}
}

// handleClick routes one mouse click. The row layout mirrors View:
// transcript rows, then the approval prompt, then the status row, then
// the completion menu, then the input line as the last row.
//
// Left button only. A click on a collapsed block header expands it; a
// click on a completion row accepts it; a click on the input line
// places the cursor. With the panel open wide, all of that lives inside
// the left reading pane, so clicks shift past the pane's borders and
// the nav pane ignores clicks; narrow, the list covers the transcript
// area and answers nothing.
func (s Screen) handleClick(msg tea.MouseClickMsg) (app.Screen, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return s, nil
	}
	if s.overlay != "" {
		s.overlay = ""
		return s, tea.ClearScreen
	}
	x, y := msg.X, msg.Y
	transcriptTop := 2 // top bar, then its margin row
	if s.panelIsSplit() {
		reading, _ := render.SplitWidths(contentWidth(s.width))
		// Column 0 is the gutter; the reading pane runs to the nav
		// pane's left edge. Clicks on the nav pane are not panel
		// actions, so they stop here.
		if x > reading {
			return s, nil
		}
		x-- // the pane's left border
		y-- // the pane's top border
		transcriptTop = 3
	} else if s.panel.open {
		if y-transcriptTop < s.transcriptHeight() {
			return s, nil // the list covers the transcript area
		}
	}
	transcriptRows := s.transcriptHeight()
	// The composer's frame puts the input above the bottom border, so
	// the input row and its first column both shift; the composer owns
	// the exact numbers (InputRowFromBottom, InputColumnOffset). Under
	// the split, the pane's bottom border takes the screen's last row.
	bottom := s.height - 1
	if s.panelIsSplit() {
		bottom--
	}
	inputRow := bottom - s.composer.InputRowFromBottom()
	colOffset := s.composer.InputColumnOffset()
	menuRows := s.composer.Height() - 3
	if menuRows < 0 {
		menuRows = 0
	}

	switch {
	case y-transcriptTop < transcriptRows && s.transcriptShown():
		next, expanded := s.transcript.ExpandBlockAtScreenRow(y - transcriptTop)
		if expanded {
			s.transcript = next
		}
	case y == inputRow:
		// One column in for the gutter, then the composer's own border
		// offset: the click lands on the input's column space.
		s.composer.ClickToColumn(x - 1 - colOffset)
	// The menu sits above the frame's top border, which sits above the
	// input row: menu rows run from inputRow-1-menuRows to inputRow-2.
	case s.composer.MenuActive() && y >= inputRow-1-menuRows && y < inputRow-1:
		s.composer.MenuClickRow(y - (inputRow - 1 - menuRows))
	}
	return s, nil
}

// transcriptShown reports whether the transcript itself is the content
// of its area right now - not a picker dialog, the overlay, or the
// panel's content dialog. Clicks that hit the area while something else
// draws there must not act on the transcript hidden behind it.
func (s Screen) transcriptShown() bool {
	return s.modelPicker == nil && s.agentPicker == nil && s.overlay == "" && !s.panel.dialog
}

func (s Screen) completionAction(id keymap.ID) (app.Screen, tea.Cmd) {
	switch id {
	case keymap.IDMenuNext:
		s.composer = s.composer.MenuNext()
	case keymap.IDMenuPrev:
		s.composer = s.composer.MenuPrev()
	case keymap.IDMenuDismiss:
		s.composer = s.composer.MenuDismiss()
	case keymap.IDMenuAccept:
		s.composer = s.composer.AcceptSelected()
	case keymap.IDAcceptPrefix:
		// Tab extends to the shared prefix. When that adds nothing, it
		// falls through to accepting the highlighted row, so Tab always
		// does something visible rather than appearing dead.
		next, grew := s.composer.AcceptCommonPrefix()
		if !grew {
			next = next.AcceptSelected()
		}
		s.composer = next
	}
	return s, nil
}

func (s Screen) transcriptAction(id keymap.ID) (app.Screen, tea.Cmd) {
	switch id {
	case keymap.IDFocusNext:
		s.transcript = s.transcript.FocusNext()
	case keymap.IDFocusPrev:
		s.transcript = s.transcript.FocusPrev()
	case keymap.IDCancel:
		s.transcript = s.transcript.ClearFocus()
	case keymap.IDToggleBlock:
		s.transcript, _ = s.transcript.ToggleFocused()
	case keymap.IDExpandAll:
		s.transcript = s.transcript.SetAllCollapsed(false)
	case keymap.IDCollapseAll:
		s.transcript = s.transcript.SetAllCollapsed(true)
	case keymap.IDCopyBlock:
		if text, ok := s.transcript.FocusedText(); ok {
			// tea.SetClipboard writes OSC 52. It fails silently on VTE
			// and on Terminal.app, so the status line says what was
			// attempted rather than claiming success.
			s.statusline.Notice("copied the block")
			return s, tea.SetClipboard(text)
		}
	}
	return s, nil
}

// globalAction reports handled=false when the key should fall through to
// a later context, so one binding can be global without swallowing the
// composer's own use of it.
func (s Screen) globalAction(id keymap.ID) (app.Screen, tea.Cmd, bool) {
	switch id {
	case keymap.IDThemeDialog:
		if len(s.themes) == 0 {
			return s, nil, true
		}
		next := themepicker.New(s.Theme, s.Tier, s.themes)
		return s, func() tea.Msg { return app.PushScreenMsg{Screen: next} }, true
	case keymap.IDToggleReason:
		s.transcript = s.transcript.ToggleReasoning()
		return s, nil, true
	case keymap.IDScrollUp:
		s.transcript = s.transcript.PageBy(-1, 2)
		return s, nil, true
	case keymap.IDScrollDown:
		s.transcript = s.transcript.PageBy(1, 2)
		return s, nil, true
	case keymap.IDScrollTop:
		s.transcript = s.transcript.ScrollToTop()
		return s, nil, true
	case keymap.IDScrollBottom:
		s.transcript = s.transcript.ScrollToBottom()
		return s, nil, true
	case keymap.IDPanelToggle:
		// ctrl+n drives the panel's three states. This site handles the
		// two the global context can see: closed opens the panel focused
		// in its list, and open-with-the-composer-focused closes it. The
		// middle state - the list focused - claims ctrl+n earlier, in
		// handlePanelKey, to hand focus back without closing.
		if s.panel.open {
			s.panel.open, s.panel.focused, s.panel.dialog = false, false, false
		} else {
			s.panel.openPanel()
			s.transcript = s.transcript.ClearFocus()
		}
		s.reflow()
		return s, nil, true
	case keymap.IDOpenPager:
		// Rule 6.2: transcript mode replaces terminal find. The pager
		// takes a VALUE snapshot of the conversation; blocks re-render
		// at any width, so the copy is cheap and stays coherent.
		pager := transcript.NewPager(s.Theme, s.Tier, s.transcript)
		return s, func() tea.Msg { return app.PushScreenMsg{Screen: pager} }, true
	case keymap.IDCancel:
		return s.cancelTurn()
	case keymap.IDQuit:
		return s.quit()
	}
	return s, nil, false
}

func (s Screen) composerAction(id keymap.ID) (app.Screen, tea.Cmd, bool) {
	switch id {
	case keymap.IDSend:
		if text := s.composer.Value(); text != "" && isSlashCommand(text) {
			next, cmd := s.runSlashCommand(text)
			return next, cmd, true
		}
		next, cmd := s.send()
		return next, cmd, true
	case keymap.IDFocusPrev:
		// Shift-Tab from the composer enters the transcript at the
		// NEWEST block, which is the one next to the composer.
		s.transcript = s.transcript.FocusPrev()
		return s, nil, true
	case keymap.IDHelp:
		// Only on an empty composer: "?" is an ordinary character in a
		// question, and swallowing it mid-sentence would be worse than
		// having no help key.
		if s.composer.Value() != "" {
			return s, nil, false
		}
		// openHelp is the one help renderer (dialog frame, mouse hint
		// included); this was a second hand-built copy until the
		// dialog wiring made the drift visible.
		return s.openHelp(), tea.ClearScreen, true
	}
	return s, nil, false
}

// cancelTurn stops the active turn and KEEPS the composer text. Losing
// what the user typed on a cancel is the reported defect this avoids.
//
// It never has to unfocus a block. handleKey offers Esc to the
// transcript context first, and that context claims it while a block
// holds the focus, so this is only reached with the composer focused.
func (s Screen) cancelTurn() (app.Screen, tea.Cmd, bool) {
	if s.active == nil {
		return s, nil, false
	}
	s.approval.Clear()
	s.active.Cancel()
	s.statusline.Stop()
	return s, nil, true
}

// quit cancels first and only exits on a second press inside the
// double-press window. One ctrl+c must never discard a running turn AND
// the session at once (docs/design/ux-rules.md rule 1.3).
func (s Screen) quit() (app.Screen, tea.Cmd, bool) {
	if s.active != nil && !s.quitArmed {
		s.approval.Clear()
		s.active.Cancel()
		s.statusline.Stop()
		s.quitArmed = true
		s.statusline.Notice("cancelled. press ctrl+c again to quit")
		return s, nil, true
	}
	if !s.quitArmed && s.composer.Value() != "" {
		s.quitArmed = true
		s.statusline.Notice("press ctrl+c again to quit")
		return s, nil, true
	}
	return s, tea.Quit, true
}
