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

// Update delegates to update, then re-budgets the transcript whenever the
// chrome's row claim changed.
//
// The re-budget cannot live at the individual call sites. Arming an
// approval prompt, starting the status line, and resolving a decision all
// change reservedRows, and each one that forgot to re-budget would let
// View draw more rows than the terminal has - the exact failure the live
// window exists to prevent. Comparing before and after cannot be
// forgotten by a later change.
func (s Screen) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	before := s.reservedRows()
	next, cmd := s.update(msg)
	scr, ok := next.(Screen)
	if !ok || scr.reservedRows() == before {
		return next, cmd
	}
	commit := scr.resize()
	if commit == nil {
		return scr, cmd
	}
	// Sequence, not Batch: tea.Batch documents "no ordering guarantees",
	// and cmd may itself carry content evicted earlier in this same
	// update. Scrollback order is the transcript's whole contract.
	return scr, tea.Sequence(cmd, commit)
}

// resize re-budgets the transcript from the current size and chrome, and
// returns the Cmd that commits whatever the new budget evicted.
func (s *Screen) resize() tea.Cmd {
	text := s.transcript.SetSize(s.width, s.height, s.reservedRows())
	if text == "" {
		return nil
	}
	return func() tea.Msg { return app.PrintMsg{Text: text} }
}

func (s Screen) update(msg tea.Msg) (app.Screen, tea.Cmd) {
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
		// Evicted content leaves the managed frame for native scrollback.
		// It is NOT printed here: tea.Println is a documented no-op while
		// the altscreen is active, so printing directly would silently
		// destroy transcript lines whenever a modal happened to be open.
		// app.Model owns the decision because only the router knows the
		// stack depth.
		return s, func() tea.Msg { return app.PrintMsg{Text: msg.Text} }
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.composer.SetWidth(msg.Width)
		return s, s.resize()
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

// reservedRows is how many rows the non-transcript chrome claims. The
// transcript's eviction budget is the remaining height, so this must
// account for every row View can draw below the transcript, plus one
// spare so the frame never exactly fills the terminal.
func (s Screen) reservedRows() int {
	rows := 1 + 1 // composer, plus the reserve
	if s.approval.Active() {
		rows += 2 // title and hint
	}
	if s.statusline.Active() {
		rows++
	}
	return rows
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
