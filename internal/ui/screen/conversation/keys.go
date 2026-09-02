package conversation

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
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
	if next, cmd, handled := s.handleModalKey(msg); handled {
		return next, cmd
	}

	// Any key other than a second ctrl+c ends the quit-armed state, so a
	// stray keystroke cannot leave the session one press from exiting.
	// The modal surfaces above return before this point and manage the arm
	// themselves; every other key path runs through this clear.
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
	if s.composer.MentionMenuActive() {
		if id, ok := s.keys.Match(keymap.ContextCompletion, key); ok {
			return s.mentionCompletionAction(id)
		}
	}
	if s.panel.open && !s.panel.focused && key == "tab" {
		s.panelFocus(true)
		s.transcript = s.transcript.ClearFocus()
		return s, nil
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

	if key == "up" && (s.composer.CursorLine() == 0 || s.composer.IsEmpty()) && s.history.Len() > 0 {
		s.history.Open()
		return s, nil
	}

	next, cmd := s.composer.Update(msg)
	s.composer = next
	return s, cmd
}

func (s Screen) handleModalKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if next, cmd, handled := s.handleApprovalKey(msg); handled {
		return next, cmd, true
	}
	if next, cmd, handled := s.handleOpenPickerKey(msg); handled {
		return next, cmd, true
	}
	if next, cmd, handled := s.handleHistoryKey(msg); handled {
		return next, cmd, true
	}
	if next, cmd, handled := s.handleQueueKey(msg); handled {
		return next, cmd, true
	}
	if next, cmd, handled := s.handleBlackboardKey(msg); handled {
		return next, cmd, true
	}
	// The login dialog is checked last. It only ever opens from the
	// composer via /login, which is unreachable while any earlier modal
	// (approval, a picker, history, the queue, the blackboard) already
	// holds focus, so this ordering is defensive only - there is no
	// state today where two of these are open at once.
	if next, cmd, handled := s.handleLoginKey(msg); handled {
		return next, cmd, true
	}
	return s, nil, false
}

// handleHistoryKey routes a key when the message history overlay is active.
func (s Screen) handleHistoryKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if !s.history.Active() {
		return s, nil, false
	}
	switch msg.String() {
	case "up", "k":
		s.history.Up()
		return s, nil, true
	case "down", "j":
		s.history.Down()
		return s, nil, true
	case "enter":
		if sel := s.history.Selected(); sel != "" {
			s.composer.SetValue(sel)
		}
		s.history.Close()
		return s, nil, true
	case "esc":
		s.history.Close()
		return s, nil, true
	case "ctrl+c":
		s.history.Close()
		next, cmd, _ := s.quit()
		return next, cmd, true
	default:
		return s, nil, true
	}
}

// handleQueueKey routes a key when the queued messages overlay is active.
func (s Screen) handleQueueKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if !s.queueOverlay.Active() {
		return s, nil, false
	}
	switch msg.String() {
	case "up", "k":
		s.queueOverlay.Up()
		return s, nil, true
	case "down", "j":
		s.queueOverlay.Down()
		return s, nil, true
	case "d", "x", "backspace", "delete":
		if _, ok := s.queueOverlay.DeleteSelected(); ok {
			s.queue = s.queueOverlay.Items()
			s.statusline.Notice("removed queued message")
		}
		return s, nil, true
	case "f", "F":
		// Defense in depth: composerAction's own IDForceSend case
		// refuses to force-send while embedded (a subagent thread owns
		// no turn of its own to interrupt). IDQueueDialog is swallowed
		// while embedded today (globalAction), so this overlay cannot
		// currently open inside a thread - but this case would
		// force-push anyway if either assumption ever changed, so it
		// carries the identical guard rather than relying on that
		// upstream swallow alone.
		if s.embedded {
			s.statusline.Notice("force send is unavailable in subagent threads")
			return s, nil, true
		}
		idx := s.queueOverlay.Cursor() // BEFORE DeleteSelected: it re-clamps the cursor
		sel, ok := s.queueOverlay.DeleteSelected()
		if idx < 0 || !ok {
			return s, nil, true
		}
		s.queue = s.queueOverlay.Items()
		if s.active == nil {
			// P6: idle sends the selected item immediately.
			next, cmd := s.sendText(sel)
			sc := next.(Screen)
			if sc.active == nil {
				sc.queueOverlay.InsertAt(idx, sel)
				sc.queue = sc.queueOverlay.Items()
				sc.statusline.Notice("send failed; re-queued")
				return sc, cmd, true // overlay stays open
			}
			sc.queueOverlay.Close()
			sc.statusline.Notice("sent")
			return sc, cmd, true
		}
		if !s.forcePush(sel) {
			s.queueOverlay.InsertAt(idx, sel)
			s.queue = s.queueOverlay.Items()
			s.statusline.Notice("nothing to interrupt")
			return s, nil, true // overlay stays open, cursor on restored item
		}
		s.queueOverlay.Close()
		return s, nil, true
	case "enter":
		if deleted, ok := s.queueOverlay.DeleteSelected(); ok {
			s.queue = s.queueOverlay.Items()
			s.composer.SetValue(deleted)
			s.queueOverlay.Close()
		}
		return s, nil, true
	case "esc":
		s.queueOverlay.Close()
		return s, nil, true
	case "ctrl+c":
		s.queueOverlay.Close()
		next, cmd, _ := s.quit()
		return next, cmd, true
	default:
		return s, nil, true
	}
}

// handleBlackboardKey routes a key when the blackboard & messaging overlay is active.
func (s Screen) handleBlackboardKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if !s.blackboard.Active() {
		return s, nil, false
	}
	switch msg.String() {
	case "up", "k":
		s.blackboard.Up()
		return s, nil, true
	case "down", "j":
		s.blackboard.Down()
		return s, nil, true
	case "tab":
		s.blackboard.ToggleTab()
		return s, nil, true
	case "esc":
		s.blackboard.Close()
		return s, nil, true
	case "ctrl+c":
		s.blackboard.Close()
		next, cmd, _ := s.quit()
		return next, cmd, true
	default:
		return s, nil, true
	}
}

// handleLoginKey routes a key when the /login dialog is open: Esc
// cancels with no notice (the same rule the queue overlay and history
// follow), ctrl+c closes the dialog and runs the ordinary quit-arm flow
// (keys.go's own handleQueueKey "ctrl+c" case is the precedent), and
// Enter on the password field submits. Every other key reaches
// loginDialog.Update, which routes Tab/Enter-on-email to focus the
// password field and everything else to the focused field's own input.
func (s Screen) handleLoginKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if s.login == nil {
		return s, nil, false
	}
	switch msg.String() {
	case "esc":
		s.login = nil
		return s, tea.ClearScreen, true
	case "ctrl+c":
		s.login = nil
		next, cmd, _ := s.quit()
		return next, tea.Batch(cmd, tea.ClearScreen), true
	case "enter":
		if s.login.focus == 1 {
			next, cmd := s.submitLogin()
			return next, cmd, true
		}
	}
	next, cmd := s.login.Update(msg)
	s.login = &next
	return s, cmd, true
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
	if s.sessionPicker != nil {
		next, cmd := s.handleSessionPickerKey(msg)
		return next, cmd, true
	}
	if s.palettePicker != nil {
		next, cmd := s.handlePalettePickerKey(msg)
		return next, cmd, true
	}
	if s.effortPicker != nil {
		next, cmd := s.handleEffortPickerKey(msg)
		return next, cmd, true
	}
	return s, nil, false
}

// handlePanelKey routes a key into the open, focused panel: its content
// dialog first (any key closes it; the view toggle, the half-page
// scrolls, and ctrl+c survive), then the list. The list is a focusable
// pane, not a modal: ctrl+c, ctrl+b, ctrl+t, and ctrl+o stay live over
// it (a ctrl-modified key carries no Text, so the picker would silently
// swallow them), esc hands focus back to the composer WITHOUT closing
// the panel, the files bindings navigate, and every other key feeds the
// list's filter. handled is false when the panel does not hold focus,
// so the ordinary chat flow resumes.
func (s Screen) handlePanelKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if !s.panel.open || !s.panel.focused {
		return s, nil, false
	}
	if s.panel.dialog {
		// A live subagent thread routes its keys to the embedded
		// screen's own Update (its composer, its transcript); file and
		// step-log dialogs keep the any-key-closes rule.
		if s.panel.dialogAgent != "" && s.thread != nil && s.threadID == s.panel.dialogAgent {
			next, cmd := s.threadDialogKey(msg)
			return next, cmd, true
		}
		if msg.String() == "ctrl+c" {
			s.panel.dialog, s.panel.dialogAgent = false, ""
			return s, tea.ClearScreen, true
		}
		return s.panelDialogKey(msg), nil, true
	}
	if msg.String() == "ctrl+c" {
		next, cmd, _ := s.quit()
		return next, cmd, true
	}
	if msg.String() == "tab" {
		s.panelFocus(false)
		return s, nil, true
	}
	if id, ok := s.keys.Match(keymap.ContextGlobal, msg.String()); ok {
		switch id {
		case keymap.IDPanelToggle:
			// The middle state of ctrl+b's cycle: focus returns to the
			// composer; the panel stays open and live beside it.
			s.panelFocus(false)
			return s, nil, true
		case keymap.IDThemeDialog, keymap.IDOpenPager:
			// Still reachable: the conversation is on screen beside the
			// panel, so its global surfaces stay one key away.
			return s, nil, false
		}
	}
	return s.handlePanelListKey(msg)
}

func (s Screen) handlePanelListKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if id, ok := s.keys.Match(keymap.ContextFiles, msg.String()); ok {
		switch id {
		case keymap.IDCancel:
			s.panelFocus(false)
			return s, nil, true
		case keymap.IDPagerRowUp:
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case keymap.IDPagerRowDown:
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		}
	}
	// Sidebar navigation: only arrow/nav keys and Enter act on the list (no search filter)
	switch msg.Code {
	case tea.KeyUp, tea.KeyDown, tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown, tea.KeyEnter:
		// allowed nav keys
	default:
		if msg.String() == "j" {
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		} else if msg.String() == "k" {
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		} else {
			return s, nil, true
		}
	}
	next, cmd := s.panel.list.Update(msg)
	s.panel.list = next
	s.panel.offset = 0 // a moved selection restarts the content at its top
	if cmd != nil {
		if _, ok := cmd().(picker.SelectMsg); ok && s.panelDialogFits() {
			// Enter on a subagent row opens its thread when one
			// resolves (openThread builds or reuses the embedded
			// screen); either way the dialog is named for the agent.
			// A file row keeps the diff/source dialog.
			if a, isAgent := s.panel.selectedAgent(); isAgent {
				s.panel.dialogAgent = a.ID
				_, openCmd := s.openThread(a.ID)
				s.panel.dialog, s.panel.offset = true, 0
				return s, openCmd, true
			} else {
				s.panel.dialogAgent = ""
			}
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
	switch msg.String() {
	case "up", "k":
		s.scrollPanel(-1)
		return s
	case "down", "j":
		s.scrollPanel(1)
		return s
	case "pgup":
		s.scrollPanel(-max(1, s.panelBodyRows()/2))
		return s
	case "pgdown":
		s.scrollPanel(max(1, s.panelBodyRows()/2))
		return s
	case "home":
		s.panel.offset = 0
		return s
	case "end":
		s.panel.offset = 100000
		s.scrollPanel(0)
		return s
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
		case keymap.IDCancel:
			s.panel.dialog, s.panel.dialogAgent = false, ""
			s.panel.offset = 0
			s.closeThread()
			return s
		}
	}
	s.panel.dialog, s.panel.dialogAgent = false, ""
	s.panel.offset = 0
	s.closeThread()
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
		s.composer.Blur()
		if _, ok := s.panel.list.Selected(); !ok {
			s.panel.list.MoveTo(0)
		}
	} else {
		s.composer.Focus()
		s.panel.dialog, s.panel.dialogAgent = false, ""
	}
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

// mentionCompletionAction routes ContextCompletion keys to the @-mention
// picker when the mention menu is open. The key IDs are the same as the
// slash-command menu — only one menu can be open at a time (the routing in
// keyPress guards on MentionMenuActive).
func (s Screen) mentionCompletionAction(id keymap.ID) (app.Screen, tea.Cmd) {
	switch id {
	case keymap.IDMenuNext:
		s.composer = s.composer.MentionMenuNext()
	case keymap.IDMenuPrev:
		s.composer = s.composer.MentionMenuPrev()
	case keymap.IDMenuDismiss:
		s.composer = s.composer.MentionMenuDismiss()
	case keymap.IDMenuAccept, keymap.IDAcceptPrefix:
		s.composer = s.composer.AcceptMention()
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
	case keymap.IDCancelToolCall:
		return s.cancelFocusedToolCall()
	}
	return s, nil
}

// globalAction reports handled=false when the key should fall through to
// a later context, so one binding can be global without swallowing the
// composer's own use of it.
func (s Screen) globalAction(id keymap.ID) (app.Screen, tea.Cmd, bool) {
	// The embedded subagent-thread construction owns no terminal
	// surface of its own: the screen-stack globals (theme picker, pager,
	// activity panel) belong to the MAIN screen alone. Swallowing them
	// here keeps every other key path - composer, completion,
	// transcript - identical between the two constructions.
	if s.embedded {
		switch id {
		case keymap.IDThemeDialog, keymap.IDOpenPager, keymap.IDPanelToggle, keymap.IDSettingsDialog, keymap.IDPalette, keymap.IDQueueDialog, keymap.IDBlackboardDialog:
			return s, nil, true
		}
	}
	switch id {
	case keymap.IDThemeDialog:
		if len(s.themes) == 0 {
			return s, nil, true
		}
		next := themepicker.New(s.Theme, s.Tier, s.themes)
		return s, func() tea.Msg { return app.PushScreenMsg{Screen: next} }, true
	case keymap.IDSettingsDialog:
		next, cmd := s.openSettingsScreen("")
		return next, cmd, true
	case keymap.IDPalette:
		next, cmd := s.openCommandPalette()
		return next, cmd, true
	case keymap.IDQueueDialog:
		return s.openQueue(), nil, true
	case keymap.IDBlackboardDialog:
		return s.openBlackboard(), nil, true
	case keymap.IDToggleReason:
		s.transcript = s.transcript.ToggleReasoning()
		return s, nil, true
	default:
		if next, cmd, handled := s.globalScrollAction(id); handled {
			return next, cmd, true
		}
	}
	switch id {
	case keymap.IDPanelToggle:
		// ctrl+b drives the panel's three states. This site handles the
		// two the global context can see: closed opens the panel focused
		// in its list, and open-with-the-composer-focused closes it (a
		// close also drops the filter - a hidden list must not resurface
		// later as an unexplained short list). The middle state - the
		// list focused - claims ctrl+b earlier, in handlePanelKey, to
		// hand focus back without closing.
		if s.panel.open {
			s.panel.open, s.panel.focused, s.panel.dialog, s.panel.dialogAgent = false, false, false, ""
			s.panel.list.ClearFilter()
			s.closeThread() // the Conversation keeps the history; a reopen rebuilds
		} else {
			s.panel.openPanel()
			s.transcript = s.transcript.ClearFocus()
		}
		s.reflow()
		// The toggle rewraps every chat row and changes the surface's
		// shape. Terminals that coalesce positioned writes (the
		// full-repaint hazard class) leave the old column bleeding
		// through a diff update, so the toggle clears and redraws - the
		// same rule the overlay dismiss and the screen pop follow. A
		// toggle is rare, not per-keystroke; the cost is nothing.
		return s, tea.ClearScreen, true
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

func (s Screen) scrollStep() int {
	type scrollProvider interface {
		ScrollLines() int
	}
	if sp, ok := s.conv.(scrollProvider); ok {
		if lines := sp.ScrollLines(); lines > 0 {
			return lines
		}
	}
	return 2
}

func (s Screen) globalScrollAction(id keymap.ID) (app.Screen, tea.Cmd, bool) {
	step := s.scrollStep()
	switch id {
	case keymap.IDScrollUp:
		s.transcript = s.transcript.PageBy(-1, step)
		return s, nil, true
	case keymap.IDScrollDown:
		s.transcript = s.transcript.PageBy(1, step)
		return s, nil, true
	case keymap.IDScrollTop:
		s.transcript = s.transcript.ScrollToTop()
		return s, nil, true
	case keymap.IDScrollBottom:
		s.transcript = s.transcript.ScrollToBottom()
		return s, nil, true
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
	case keymap.IDForceSend:
		if s.embedded {
			s.statusline.Notice("force send is unavailable in subagent threads")
			return s, nil, true
		}
		text := s.composer.SubmitText()
		if strings.HasPrefix(strings.TrimSpace(text), "/") {
			s.statusline.Notice("slash commands cannot be force-sent")
			return s, nil, true
		}
		if text != "" {
			if s.active != nil {
				if s.forcePush(text) {
					s.composer.Clear()
				} else {
					s.statusline.Notice("nothing to interrupt")
				}
				return s, nil, true
			}
			next, cmd := s.send() // idle: ordinary send; send() consumes the composer itself
			return next, cmd, true
		}
		if s.active != nil && len(s.queue) > 0 {
			return s.forceSendHead(), nil, true
		}
		return s, nil, true
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
	s.approval.ClearAll()
	s.active.Cancel()
	s.statusline.Stop()
	return s, nil, true
}

// quit cancels first and only exits on a second press inside the
// double-press window. One ctrl+c must never discard a running turn AND
// the session at once (docs/design/ux-rules.md rule 1.3).
func (s Screen) quit() (app.Screen, tea.Cmd, bool) {
	if !s.quitArmed {
		s.quitArmed = true
		if s.active != nil {
			s.approval.ClearAll()
			s.active.Cancel()
			s.statusline.Stop()
			s.statusline.Notice("cancelled")
		}
		return s, nil, true
	}
	return s, tea.Quit, true
}
