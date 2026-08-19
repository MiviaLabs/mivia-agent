// Package conversation is the base screen: the transcript, composer,
// transient status line, and inline approval prompt, driven by a real
// ports.Conversation. It never calls a harness directly - only Send,
// Cancel, and Approver.Resolve, exactly the ports surface.
package conversation

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/themepicker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

var _ app.Screen = Screen{}

// Screen assembles the four transcript-adjacent components around one
// ports.Conversation. themes is threaded through only so ctrl+t can
// build a themepicker.Screen without importing that package here (that
// import would be backwards: screens depend on app, not on each other).
type Screen struct {
	Theme  theme.Theme
	Tier   theme.Tier
	themes []theme.Theme

	conv     ports.Conversation
	approver ports.Approver // nil is valid: no approval wiring

	transcript transcript.Model
	composer   composer.Model
	statusline statusline.Model
	approval   approval.Model

	active ports.TurnHandle
	now    func() time.Time

	// width and height are the live terminal size, from WindowSizeMsg.
	// Nothing here may assume a size: the layout work that consumes
	// height lands with the cockpit architecture, but the size must be
	// tracked from the start or resize is silently a no-op.
	width  int
	height int
}

// New builds a Screen. themes is the candidate set offered by ctrl+t;
// pass nil to disable the theme picker for this Program. now defaults
// to time.Now if nil (tests pin it for deterministic statusline output).
func New(th theme.Theme, tier theme.Tier, themes []theme.Theme, conv ports.Conversation, approver ports.Approver, width int, now func() time.Time) Screen {
	if now == nil {
		now = time.Now
	}
	return Screen{
		Theme: th, Tier: tier, themes: themes,
		conv: conv, approver: approver,
		transcript: transcript.New(th, tier),
		composer:   composer.New(th, tier, width),
		statusline: statusline.New(th, tier),
		approval:   approval.New(th, tier),
		now:        now,
	}
}

func (s Screen) Init() tea.Cmd { return nil }

// turnEventMsg carries one event read off the active TurnHandle.
type turnEventMsg struct{ ev uievent.Event }

// turnEndedMsg signals the active TurnHandle's Events() channel closed.
type turnEndedMsg struct{}

// waitForEvent is the one real tea.Cmd that reads one event and, when
// the caller re-issues it after handling the Msg, reads the next one -
// the shape build spec section 4.5 requires: no goroutine touches the
// model, only this Cmd's own closure over the channel.
func waitForEvent(events <-chan uievent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return turnEndedMsg{}
		}
		return turnEventMsg{ev: ev}
	}
}

func (s Screen) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	case turnEventMsg:
		return s.handleTurnEvent(msg.ev)
	case turnEndedMsg:
		s.statusline.Stop()
		s.active = nil
		return s, nil
	case approval.DecisionMsg:
		if s.approver != nil {
			s.approver.Resolve(msg.ToolCallID, msg.Decision)
		}
		return s, nil
	case statusline.TickMsg:
		next, cmd := s.statusline.Update(msg)
		s.statusline = next
		return s, cmd
	case transcript.FlushMsg:
		next, cmd := s.transcript.Update(msg)
		s.transcript = next
		return s, cmd
	case transcript.CommitMsg:
		// The one place transcript's own CommitMsg becomes a real
		// tea.Println: finalized content leaves the managed frame and
		// joins native terminal scrollback, which is what makes the
		// inline transcript survive content taller than the terminal.
		return s, tea.Println(msg.Text)
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.composer.SetWidth(msg.Width)
		return s, nil
	case app.ThemeChangedMsg:
		s.Theme, s.Tier = msg.Theme, msg.Tier
		s.transcript.Theme, s.transcript.Tier = msg.Theme, msg.Tier
		s.composer.Theme, s.composer.Tier = msg.Theme, msg.Tier
		s.statusline.Theme, s.statusline.Tier = msg.Theme, msg.Tier
		s.approval.Theme, s.approval.Tier = msg.Theme, msg.Tier
		return s, nil
	}
	return s, nil
}

func (s Screen) handleKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	if s.approval.Active() {
		next, cmd := s.approval.Update(msg)
		s.approval = next
		return s, cmd
	}
	switch msg.String() {
	case "ctrl+t":
		if len(s.themes) == 0 {
			return s, nil
		}
		next := themepicker.New(s.Theme, s.Tier, s.themes)
		return s, func() tea.Msg { return app.PushScreenMsg{Screen: next} }
	case "enter":
		return s.send()
	}
	next, cmd := s.composer.Update(msg)
	s.composer = next
	return s, cmd
}

func (s Screen) send() (app.Screen, tea.Cmd) {
	text := s.composer.Value()
	if text == "" || s.active != nil {
		return s, nil
	}
	handle, err := s.conv.Send(context.Background(), intent.Send{Text: text})
	if err != nil {
		var cmd tea.Cmd
		s.transcript, cmd = s.transcript.HandleEvent(uievent.Event{
			Kind: uievent.KindError,
			Body: uievent.ErrorBody{Text: err.Error(), Fatal: false},
		})
		return s, cmd
	}
	s.composer.Clear()
	s.active = handle
	cmd := s.statusline.Start("thinking", s.now())
	return s, tea.Batch(cmd, waitForEvent(handle.Events()))
}

func (s Screen) handleTurnEvent(ev uievent.Event) (app.Screen, tea.Cmd) {
	next, flushCmd := s.transcript.HandleEvent(ev)
	s.transcript = next

	if b, ok := ev.Body.(uievent.ToolPendingBody); ok {
		s.approval.SetRequest(b)
	}

	var readCmd tea.Cmd
	if s.active != nil {
		readCmd = waitForEvent(s.active.Events())
	}
	return s, tea.Batch(flushCmd, readCmd)
}

func (s Screen) View() string {
	var lines []string
	// transcript.View() is usually "": finalized content already left
	// via CommitMsg (see internal/ui/component/transcript's doc
	// comment). Only a live streaming tail shows up here.
	if v := s.transcript.View(); v != "" {
		lines = append(lines, v)
	}
	if v := s.approval.View(); v != "" {
		lines = append(lines, v)
	}
	if v := s.statusline.View(s.now()); v != "" {
		lines = append(lines, v)
	}
	lines = append(lines, s.composer.View())

	out := lines[0]
	for _, l := range lines[1:] {
		out += "\n" + l
	}
	return out
}
