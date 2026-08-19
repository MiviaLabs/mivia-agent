package conversation

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
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
	case o.OpenHelp:
		return s.openHelp(), nil
	case o.ClearTranscript:
		s.transcript = s.transcript.Clear()
		return s, nil
	case len(o.ModelChoices) > 0:
		pm := picker.New(s.Theme, s.Tier, o.ModelChoices)
		s.modelPicker = &pm
		return s, nil
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

// openHelp draws the keymap overlay in place. Shared by "?" (keys.go's
// composerAction) and /help.
func (s Screen) openHelp() Screen {
	help := render.Help(s.Theme, s.Tier, s.keys.Help())
	if s.mouseHint != "" {
		help += "\n\nmouse captured - hold " + s.mouseHint +
			" to select with the terminal (--no-mouse releases it)"
	}
	s.overlay = help
	return s
}

// handleModelPickerKey routes one key press to the open /model picker.
// A selection asks the runner to apply it and shows the resulting
// outcome (typically a confirmation notice); Esc cancels with no
// notice.
func (s Screen) handleModelPickerKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	next, cmd := s.modelPicker.Update(msg)
	s.modelPicker = &next
	if cmd == nil {
		return s, nil
	}
	// picker.Model's Update only ever produces a non-nil Cmd for "enter"
	// (SelectMsg) or "esc" (CancelMsg) - see internal/ui/component/picker.
	switch m := cmd().(type) {
	case picker.SelectMsg:
		s.modelPicker = nil
		if s.runner == nil {
			return s.withError("no command runner configured for /model"), nil
		}
		outcome := s.runner.SelectModel(context.Background(), m.Item)
		return s.applyCommandOutcome(outcome)
	case picker.CancelMsg:
		s.modelPicker = nil
		return s, nil
	}
	return s, nil
}

// renderModelPicker draws the /model picker full-surface, mirroring
// themepicker.Screen's own layout.
func renderModelPicker(t theme.Theme, tier theme.Tier, p picker.Model) string {
	title := render.Role(t, tier, theme.RoleFG).Bold(true).Render("select a model")
	hint := render.Role(t, tier, theme.RoleFGSubtle).Render("[enter] select  [esc] cancel  type to filter")
	return title + "\n\n" + p.View() + "\n\n" + hint
}
