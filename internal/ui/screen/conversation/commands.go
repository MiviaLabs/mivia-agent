package conversation

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	settingsscreen "github.com/MiviaLabs/mivia-agent/internal/ui/screen/settings"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/themepicker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// isSlashCommand reports whether text is a slash-command line: the
// first character is "/". This mirrors the composer's own trigger rule
// (docs/design/ux-rules.md rule 5.1); Enter on such a line never falls
// through to Send.
func isSlashCommand(text string) bool { return strings.HasPrefix(text, "/") }

// splitCommand separates the command word from its arguments.
func splitCommand(line string) (name, args string) {
	line = strings.TrimPrefix(line, "/")
	name, args, _ = strings.Cut(line, " ")
	return name, strings.TrimSpace(args)
}

// runSlashCommand executes one slash-command line through the command
// runner and clears the composer. It never falls through to Send: with
// no runner configured, every command is a visible transcript error
// instead of a silent no-op or a chat message.
func (s Screen) runSlashCommand(line string) (app.Screen, tea.Cmd) {
	name, args := splitCommand(line)
	s.composer.Clear()
	if s.runner == nil {
		return s.withError("no command runner configured for /" + name), nil
	}
	outcome := s.runner.Run(context.Background(), name, args)
	if s.conv != nil {
		s.topbar.SetSession(s.conv.Model(), s.conv.ContextUsage())
	}
	return s.applyCommandOutcome(outcome)
}

// applyCommandOutcome interprets one ports.CommandOutcome, in the fixed
// priority order documented on the type: Err first, then Quit, then the
// modal-opening fields, then ClearTranscript, then ModelChoices, then
// Notice.
func (s Screen) applyCommandOutcome(o ports.CommandOutcome) (app.Screen, tea.Cmd) {
	switch {
	case o.Err != "":
		return s.withError(o.Err), nil
	case o.Quit:
		return s, tea.Quit
	case o.OpenTheme:
		return s.openThemePicker()
	case o.OpenSettings:
		return s.openSettingsScreen(o.SettingsSection)
	case o.OpenHelp:
		return s.openHelp(), tea.ClearScreen
	case o.OpenQueue:
		return s.openQueue(), tea.ClearScreen
	case o.ClearTranscript:
		if o.Conversation != nil {
			s.switchConversation(o.Conversation)
		} else {
			s.transcript = s.transcript.Clear()
			if s.conv != nil {
				s.LoadHistory(s.conv.History())
				s.topbar.SetSession(s.conv.Model(), s.conv.ContextUsage())
				if title := s.conv.Title(); title != "" {
					s.topbar.SetBreadcrumb([]string{title})
				} else {
					s.topbar.SetBreadcrumb(nil)
				}
			}
		}
		if o.Notice != "" {
			return s.withNotice(o.Notice), nil
		}
		return s, nil
	case len(o.ModelChoiceGroups) > 0:
		var groups []picker.Group
		for _, g := range o.ModelChoiceGroups {
			groups = append(groups, picker.Group{Provider: g.Provider, Models: g.Models})
		}
		pm := picker.NewGroups(s.Theme, s.Tier, groups)
		s.modelPicker = &pm
		return s, tea.ClearScreen
	case len(o.ModelChoices) > 0:
		pm := picker.New(s.Theme, s.Tier, o.ModelChoices)
		s.modelPicker = &pm
		return s, tea.ClearScreen
	case len(o.AgentChoices) > 0:
		ap := picker.New(s.Theme, s.Tier, o.AgentChoices)
		s.agentPicker = &ap
		return s, tea.ClearScreen
	case len(o.SessionChoices) > 0:
		sp := newSessionPicker(s.Theme, s.Tier, o.SessionChoices)
		s.sessionPicker = &sp
		return s, tea.ClearScreen
	case len(o.EffortChoices) > 0:
		ep := picker.New(s.Theme, s.Tier, o.EffortChoices)
		s.effortPicker = &ep
		return s, tea.ClearScreen
	case o.Notice != "":
		return s.withNotice(o.Notice), nil
	}
	return s, nil
}

// withNotice and withError append a transcript block. Both take a value
// receiver and return the updated Screen, matching every other method
// in this package's calling convention; Notice/errorNotice are pointer
// methods on the addressable local copy s.
func (s Screen) withNotice(text string) Screen {
	s.Notice(text)
	return s
}

func (s Screen) withError(text string) Screen {
	next, _ := s.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindError,
		Body: uievent.ErrorBody{Text: text},
	})
	s.transcript = next
	return s
}

// openThemePicker pushes the existing theme-picker modal. Shared by
// ctrl+t (keys.go's globalAction) and /theme, so both paths open the
// exact same screen.
func (s Screen) openThemePicker() (app.Screen, tea.Cmd) {
	if len(s.themes) == 0 {
		return s, nil
	}
	next := themepicker.New(s.Theme, s.Tier, s.themes)
	return s, func() tea.Msg { return app.PushScreenMsg{Screen: next} }
}

// openSettingsScreen pushes the full-screen settings modal, optionally
// deep-linked to one section. Shared by f2 (keys.go's globalAction) and
// /settings, so both paths open the exact same screen. An unresolved
// section name is a transcript error, not a silent fall-back to
// General - settings-screen.md §6.
func (s Screen) openSettingsScreen(section string) (app.Screen, tea.Cmd) {
	idx, ok := settingsscreen.SectionIndex(section)
	if !ok {
		return s.withError("unknown settings section " + section), nil
	}
	next := settingsscreen.New(s.Theme, s.Tier, s.topbar, s.settings, idx)
	return s, func() tea.Msg { return app.PushScreenMsg{Screen: next} }
}

// openHelp draws the keymap as a centered dialog. Shared by "?" (keys.go's
// composerAction) and /help.
func (s Screen) openHelp() Screen {
	help := render.Help(s.Theme, s.Tier, s.keys.Help())
	// Rule 6.5: the terminal's own mouse-override key rides in the
	// hint, the dialog's last row - the one row the clip guarantees,
	// because mouse capture is the most common friction point over SSH
	// and inside tmux and must never scroll off the help.
	hint := "any key closes this"
	if s.mouseHint != "" {
		// With --mouse the user opts into capture. The override key
		// (Fn/Option/Shift, set by termprobe) is the escape hatch back
		// to the terminal's own selection.
		hint = "any key closes this  -  hold " + s.mouseHint +
			" to select with the terminal (drop --mouse for native selection by default)"
	}
	w, h := s.dialogSize()
	s.overlay = render.Dialog(s.Theme, s.Tier, w, h, "keys", help, hint)
	return s
}

// openQueue draws the queue overlay dialog. Shared by ctrl+up (keys.go's
// globalAction) and /queue.
func (s Screen) openQueue() Screen {
	hint := "any key closes this"
	body := "No queued messages."
	w, h := s.dialogSize()
	s.overlay = render.Dialog(s.Theme, s.Tier, w, h, "queue", body, hint)
	return s
}

// dialogSize is the area an inline dialog actually occupies: the chat
// column minus the chrome rows (top bar, status row, composer) that stay
// pinned around it - and the chat column is the split's left pane when
// the panel is open wide. Sizing the dialog to the full terminal
// instead recenters it against rows it never gets, and its bottom
// border and hint land below the cut.
func (s Screen) dialogSize() (int, int) {
	if s.height <= 0 {
		return s.chatWidth(), 0
	}
	return s.chatWidth(), s.transcriptHeight()
}

// handlePickerKey routes one key press to an open picker modal (/model
// or /agents). A selection asks the runner to apply it and shows the
// resulting outcome (typically a confirmation notice); Esc cancels with
// no notice. Both close the picker, so both clear the screen (the same
// reasoning as opening it: the picker drew content the transcript and
// composer underneath never redrew, and closing it exposes them again).
func (s Screen) handlePickerKey(msg tea.KeyPressMsg, which *picker.Model, cmdName string, apply func(string) ports.CommandOutcome) (app.Screen, tea.Cmd) {
	// ctrl+c is the emergency exit and must not be swallowed by the
	// modal, the same rule as the approval prompt: close the picker,
	// then run the ordinary quit flow (cancel the turn, arm the second
	// press).
	if msg.String() == "ctrl+c" {
		*which = picker.Model{}
		s.modelPicker, s.agentPicker, s.sessionPicker, s.palettePicker, s.effortPicker = nil, nil, nil, nil, nil
		next, cmd, _ := s.quit()
		return next, tea.Batch(cmd, tea.ClearScreen)
	}
	next, cmd := which.Update(msg)
	*which = next
	if cmd == nil {
		return s, nil
	}
	// picker.Model's Update only ever produces a non-nil Cmd for "enter"
	// (SelectMsg) or "esc" (CancelMsg) - see internal/ui/component/picker.
	switch m := cmd().(type) {
	case picker.SelectMsg:
		s.modelPicker, s.agentPicker, s.sessionPicker, s.palettePicker, s.effortPicker = nil, nil, nil, nil, nil
		if s.runner == nil {
			return s.withError("no command runner configured for /" + cmdName), tea.ClearScreen
		}
		out := apply(m.Item)
		if s.conv != nil {
			s.topbar.SetSession(s.conv.Model(), s.conv.ContextUsage())
		}
		next, outcomeCmd := s.applyCommandOutcome(out)
		return next, tea.Batch(outcomeCmd, tea.ClearScreen)
	case picker.CancelMsg:
		s.modelPicker, s.agentPicker, s.sessionPicker, s.palettePicker, s.effortPicker = nil, nil, nil, nil, nil
		return s, tea.ClearScreen
	}
	return s, nil
}

// handleModelPickerKey, handleAgentPickerKey, and handleEffortPickerKey adapt
// handlePickerKey to the specific pickers, so call sites stay one line.
func (s Screen) handleModelPickerKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	return s.handlePickerKey(msg, s.modelPicker, "model", func(name string) ports.CommandOutcome {
		return s.runner.SelectModel(context.Background(), name)
	})
}

func (s Screen) handleAgentPickerKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	return s.handlePickerKey(msg, s.agentPicker, "agents", func(name string) ports.CommandOutcome {
		return s.runner.SelectAgent(context.Background(), name)
	})
}

func (s Screen) handleEffortPickerKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	return s.handlePickerKey(msg, s.effortPicker, "effort", func(level string) ports.CommandOutcome {
		return s.runner.SelectEffort(context.Background(), level)
	})
}

func (s Screen) handleSessionPickerKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		s.sessionPicker = nil
		next, cmd, _ := s.quit()
		return next, tea.Batch(cmd, tea.ClearScreen)
	}
	next, cmd := s.sessionPicker.Update(msg)
	s.sessionPicker = &next
	if cmd == nil {
		return s, nil
	}
	switch m := cmd().(type) {
	case picker.SelectMsg:
		s.sessionPicker = nil
		if s.runner == nil {
			return s.withError("no command runner configured for /resume"), tea.ClearScreen
		}
		out := s.runner.SelectSession(context.Background(), m.Item)
		next, outcomeCmd := s.applyCommandOutcome(out)
		return next, tea.Batch(outcomeCmd, tea.ClearScreen)
	case picker.CancelMsg:
		s.sessionPicker = nil
		return s, tea.ClearScreen
	}
	return s, nil
}

func (s Screen) openCommandPalette() (app.Screen, tea.Cmd) {
	groups := []picker.Group{
		{
			Provider: "Commands",
			Models: []string{
				"/settings - configure providers, models, and tools",
				"/model - switch active model or provider",
				"/effort - set reasoning effort level for active model",
				"/theme - change UI color theme",
				"/new - start a fresh session",
				"/clear - clear conversation history",
				"/compact - compact current conversation context",
				"/cost - view session spending and token stats",
				"/context - check context capacity usage",
				"/help - show full keymap",
			},
		},
		{
			Provider: "Agents",
			Models: []string{
				"agent: Mivia (General orchestrator)",
				"agent: Planner (Requirements and task breakdown)",
				"agent: Plan Reviewer (Adversarial architecture check)",
				"agent: Builder (TDD code and test implementation)",
				"agent: Reviewer (Codebase and security reviewer)",
			},
		},
		{
			Provider: "Settings",
			Models: []string{
				"settings: General",
				"settings: Models",
				"settings: MCP",
				"settings: Automations",
				"settings: Projects",
			},
		},
	}
	pm := picker.NewGroups(s.Theme, s.Tier, groups)
	s.palettePicker = &pm
	return s, tea.ClearScreen
}

func (s Screen) handlePalettePickerKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		s.palettePicker = nil
		next, cmd, _ := s.quit()
		return next, tea.Batch(cmd, tea.ClearScreen)
	}
	next, cmd := s.palettePicker.Update(msg)
	s.palettePicker = &next
	if cmd == nil {
		return s, nil
	}
	switch m := cmd().(type) {
	case picker.SelectMsg:
		s.palettePicker = nil
		sel := m.Item
		switch {
		case strings.HasPrefix(sel, "/"):
			cmdLine := strings.Split(sel, " - ")[0]
			return s.runSlashCommand(cmdLine)
		case strings.HasPrefix(sel, "agent: "):
			raw := strings.TrimPrefix(sel, "agent: ")
			// Split before optional description parenthesis e.g. "Plan Reviewer (Adversarial architecture check)"
			namePart := raw
			if idx := strings.Index(raw, " ("); idx >= 0 {
				namePart = raw[:idx]
			}
			agentID := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(namePart), " ", "-"))
			if s.runner == nil {
				return s.withError("no command runner configured for agents"), tea.ClearScreen
			}
			out := s.runner.SelectAgent(context.Background(), agentID)
			next, outcomeCmd := s.applyCommandOutcome(out)
			return next, tea.Batch(outcomeCmd, tea.ClearScreen)
		case strings.HasPrefix(sel, "settings: "):
			section := strings.ToLower(strings.TrimPrefix(sel, "settings: "))
			next, outcomeCmd := s.openSettingsScreen(section)
			return next, tea.Batch(outcomeCmd, tea.ClearScreen)
		}
		return s, tea.ClearScreen
	case picker.CancelMsg:
		s.palettePicker = nil
		return s, tea.ClearScreen
	}
	return s, nil
}

// renderPickerDialog draws the /model and /agents pickers as centered
// dialogs, the same primitive themepicker.Screen uses.
func renderPickerDialog(t theme.Theme, tier theme.Tier, width, height int, title string, p picker.Model) string {
	var body string
	if height > 0 {
		body = p.ViewWindow(render.DialogBodyRows(height))
	} else {
		body = p.View()
	}
	return render.Dialog(t, tier, width, height, title, body,
		"[enter] select  [esc] cancel  type to filter")
}
