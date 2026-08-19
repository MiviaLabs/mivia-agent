// Package conversation is the base screen: the transcript, composer,
// transient status line, and inline approval prompt, driven by a real
// ports.Conversation. It never calls a harness directly - only Send,
// Cancel, and Approver.Resolve, exactly the ports surface.
package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
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

	// keys is the one dispatch table. See keys.go for the context order.
	keys *keymap.Map

	// quitArmed is true between the first ctrl+c and the second. Any
	// other key clears it, so the session is never left one keystroke
	// from exiting because of a stray press.
	quitArmed bool

	// overlay replaces the transcript while it is set. The cockpit has no
	// scrollback to print into, so content that used to be printed - the
	// generated keymap - is drawn in place instead. Any key clears it.
	overlay string

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
		keys:       keymap.New(keymap.Default()),
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

// Update delegates to update, then re-sizes the transcript whenever the
// chrome's row claim changed.
//
// The re-size cannot live at the individual call sites. Arming an
// approval prompt, starting the status line, and opening the completion
// menu all change reservedRows, and each site that forgot would leave the
// transcript drawing into rows the chrome now owns. Comparing before and
// after cannot be forgotten by a later change.
func (s Screen) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	before := s.reservedRows()
	next, cmd := s.update(msg)
	scr, ok := next.(Screen)
	if !ok || scr.reservedRows() == before {
		return next, cmd
	}
	scr.resize()
	return scr, cmd
}

// resize gives the transcript the rows the chrome does not claim.
func (s *Screen) resize() {
	s.transcript.SetSize(s.width, s.height-s.reservedRows())
}

func (s Screen) update(msg tea.Msg) (app.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	case turnEventMsg:
		return s.handleTurnEvent(msg.ev)
	case turnEndedMsg:
		s.statusline.Stop()
		s.approval.Clear()
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
	case tea.MouseWheelMsg:
		// Wheel events scroll the conversation. CockpitScrollLines is the
		// multiplier: terminals disagree on how many events one physical
		// notch produces, and some amplify while others send exactly one
		// (docs/design/cockpit-research.md rule 6.6).
		step := uikitconfig.CockpitScrollLines
		if msg.Button == tea.MouseWheelUp {
			step = -step
		}
		s.transcript = s.transcript.ScrollBy(step)
		return s, nil
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.composer.SetWidth(msg.Width)
		s.resize()
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

	switch b := ev.Body.(type) {
	case uievent.ToolPendingBody:
		s.approval.SetRequest(b)
	case uievent.ToolStartBody, uievent.ToolEndBody, uievent.TurnEndBody:
		s.approval.Clear()
	}

	var readCmd tea.Cmd
	if s.active != nil {
		readCmd = waitForEvent(s.active.Events())
	}
	return s, tea.Batch(flushCmd, readCmd)
}

// reservedRows is how many rows the chrome below the transcript claims.
//
// The status row is PERMANENT in the cockpit. The inline design had no
// persistent status bar because every row it drew pushed the transcript
// up; a cockpit owns a fixed surface, so a reserved row costs nothing
// that moves. A row that is always there also never reflows the
// transcript when it changes (docs/design/ux-rules.md rule 2.7).
func (s Screen) reservedRows() int {
	rows := s.composer.Height() + 1 // the composer and its menu, plus the status row
	if s.approval.Active() {
		rows += 2 // title and hint
	}
	return rows
}

// View draws the cockpit: the transcript fills the surface, the approval
// prompt and the status row sit above the composer, and the composer is
// the last row.
//
// The transcript always returns exactly its own height, padded, so
// nothing below it moves as output streams in (ux-rules.md rule 2.8).
func (s Screen) View() string {
	var lines []string
	if v := s.overlay; v != "" {
		lines = append(lines, overlayRows(v, s.height-s.reservedRows())...)
	} else {
		lines = append(lines, s.transcript.Rows()...)
	}
	if v := s.approval.View(); v != "" {
		lines = append(lines, strings.Split(v, "\n")...)
	}
	lines = append(lines, s.statusRow())
	lines = append(lines, strings.Split(s.composer.View(), "\n")...)
	return strings.Join(lines, "\n")
}

// overlayRows pads or clips an overlay to the transcript's own height, so
// the chrome below it never moves.
func overlayRows(text string, height int) []string {
	rows := strings.Split(text, "\n")
	if height <= 0 {
		return rows
	}
	if len(rows) > height {
		return rows[:height]
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	return rows
}

// SetCommands supplies the slash-completion candidates. The command set
// belongs to the harness, so the screen takes it rather than inventing
// one.
func (s *Screen) SetCommands(cmds []composer.Command) { s.composer.SetCommands(cmds) }

// statusRow is the permanent bottom status line.
//
// It is always drawn, even when there is nothing to say, because a row
// that appears and disappears reflows every wrapped line above it
// (docs/design/ux-rules.md rule 2.7). When the transcript is scrolled
// away from the tail it says so and offers the way back, which is the
// affordance auto-follow needs (cockpit-research.md rule 6.7).
func (s Screen) statusRow() string {
	if v := s.statusline.View(s.now()); v != "" {
		return v
	}
	if !s.transcript.Following() {
		return render.Role(s.Theme, s.Tier, theme.RoleWarning).
			Render("scrolled up - ctrl+end to follow again")
	}
	if n := s.transcript.Dropped(); n > 0 {
		return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).
			Render(fmt.Sprintf("%d earlier blocks dropped from this transcript", n))
	}
	return ""
}
