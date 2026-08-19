package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
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
// approve, toggle-block or send depending on what is on screen; Esc
// means dismiss-menu, unfocus, or cancel-the-turn. Resolving that by
// asking each context in order keeps one answer per state, and
// keymap.Collisions proves no context answers twice.
//
//	approval  - a pending tool call blocks everything else
//	completion- the menu claims Enter/Tab/Up/Down/Esc while it is open
//	transcript- only while a block holds the focus
//	global    - always available
//	composer  - the resting state, and the fallback for plain text
func (s Screen) handleKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	if s.approval.Active() {
		next, cmd := s.approval.Update(msg)
		s.approval = next
		return s, cmd
	}

	// Any key dismisses an overlay, and does nothing else. An overlay
	// covers the transcript, so acting on the key underneath it would act
	// on something the user cannot see.
	if s.overlay != "" {
		s.overlay = ""
		return s, nil
	}

	key := msg.String()
	// Any key other than a second ctrl+c ends the quit-armed state, so a
	// stray keystroke cannot leave the session one press from exiting.
	if key != "ctrl+c" {
		s.quitArmed = false
	}

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

// handleClick routes one mouse click. The row layout mirrors View:
// transcript rows, then the approval prompt, then the status row, then
// the completion menu, then the input line as the last row.
//
// Left button only. A click on a collapsed block header expands it; a
// click on a completion row accepts it; a click on the input line
// places the cursor.
func (s Screen) handleClick(msg tea.MouseClickMsg) (app.Screen, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return s, nil
	}
	if s.overlay != "" {
		s.overlay = ""
		return s, nil
	}
	transcriptRows := s.height - s.reservedRows()
	inputRow := s.height - 1
	menuRows := s.composer.Height() - 1

	switch {
	case msg.Y < transcriptRows:
		next, expanded := s.transcript.ExpandBlockAtScreenRow(msg.Y)
		if expanded {
			s.transcript = next
		}
	case msg.Y == inputRow:
		s.composer.ClickToColumn(msg.X)
	case s.composer.MenuActive() && msg.Y >= inputRow-menuRows && msg.Y < inputRow:
		s.composer.MenuClickRow(msg.Y - (inputRow - menuRows))
	}
	return s, nil
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
		// Drawn in place, not printed: the alternate screen has no
		// scrollback to print into.
		s.overlay = render.Help(s.Theme, s.Tier, s.keys.Help())
		return s, nil, true
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
