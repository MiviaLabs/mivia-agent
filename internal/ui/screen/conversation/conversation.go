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

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
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

	conv        ports.Conversation
	approver    ports.Approver      // nil is valid: no approval wiring
	runner      ports.CommandRunner // nil is valid: every "/x" then shows an error, never sends
	modelPicker *picker.Model       // non-nil while the /model picker is open
	agentPicker *picker.Model       // non-nil while the /agents picker is open

	topbar     topbar.Model
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

	// mouseHint names the terminal's mouse-override key (rule 6.5). It
	// is appended to the help overlay so the escape hatch is on screen,
	// not buried in documentation. Empty when the hint was never set.
	mouseHint string

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
	s := Screen{
		Theme: th, Tier: tier, themes: themes,
		conv: conv, approver: approver,
		transcript: transcript.New(th, tier),
		composer:   composer.New(th, tier, width),
		statusline: statusline.New(th, tier),
		approval:   approval.New(th, tier),
		keys:       keymap.New(keymap.Default()),
		now:        now,
	}
	s.approval.SetWidth(contentWidth(width))
	s.topbar = topbar.New(th, tier, conv.Model(), conv.ContextUsage(), contentWidth(width))
	return s
}

func (s Screen) Init() tea.Cmd { return nil }

// ViewFlags holds the alternate screen: the conversation is the cockpit.
func (s Screen) ViewFlags() app.ViewFlags { return app.ViewFlags{AltScreen: true} }

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
		return uievent.EventMsg{Event: ev}
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

// contentWidth is the usable column count: the terminal minus the
// one-column gutter each side, so no component or message touches the
// screen edge. Below 3 columns the gutter gives way - there is nothing
// to frame.
func contentWidth(width int) int {
	if width < 3 {
		return width
	}
	return width - 2
}

// resize gives the transcript the rows the chrome does not claim.
func (s *Screen) resize() {
	s.transcript.SetSize(contentWidth(s.width), s.height-s.reservedRows())
}

func (s Screen) update(msg tea.Msg) (app.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	case uievent.EventMsg:
		return s.handleTurnEvent(msg.Event)
	case turnEndedMsg:
		s.statusline.Stop()
		s.approval.Clear()
		s.active = nil
		// Session values move at turn boundaries: refresh the top bar's
		// model and context share now, not per token.
		s.topbar.SetSession(s.conv.Model(), s.conv.ContextUsage())
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
		if s.approval.Active() {
			// The approval prompt is modal for keys; the wheel follows it,
			// scrolling the diff preview instead of the transcript behind
			// it.
			s.approval = s.approval.ScrollBy(step)
			return s, nil
		}
		s.transcript = s.transcript.ScrollBy(step)
		return s, nil
	case tea.MouseClickMsg:
		return s.handleClick(msg)
	case tea.MouseReleaseMsg:
		// Actions fire on the click, not the release: there is no
		// drag selection to complete (deferred behind rule 6.3's
		// cheaper scrollback handover), so release carries nothing.
		return s, nil
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.composer.SetWidth(contentWidth(msg.Width))
		s.approval.SetWidth(contentWidth(msg.Width))
		s.topbar.SetWidth(contentWidth(msg.Width))
		s.resize()
		return s, nil
	case app.ThemeChangedMsg:
		s.Theme, s.Tier = msg.Theme, msg.Tier
		s.transcript.Theme, s.transcript.Tier = msg.Theme, msg.Tier
		s.composer.Theme, s.composer.Tier = msg.Theme, msg.Tier
		s.statusline.SetTheme(msg.Theme, msg.Tier)
		s.approval.Theme, s.approval.Tier = msg.Theme, msg.Tier
		s.topbar.SetTheme(msg.Theme, msg.Tier)
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
		s.statusline.SetLabel("pending")
	case uievent.ToolStartBody:
		s.approval.Clear()
		s.statusline.SetLabel("running")
	case uievent.ToolEndBody:
		s.approval.Clear()
		s.statusline.SetLabel("thinking")
	case uievent.TurnEndBody:
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
	// the top bar, the composer and its menu, and the status row
	rows := s.topbar.Height() + s.composer.Height() + 1
	if s.approval.Active() {
		rows += s.approval.Height() // bordered box: title, optional diff preview, hint, and the border rows
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
	lines = append(lines, s.topbar.View())
	switch {
	case s.modelPicker != nil:
		dw, dh := s.dialogSize()
		content := renderPickerDialog(s.Theme, s.Tier, dw, dh, "select a model", *s.modelPicker)
		lines = append(lines, overlayRows(content, s.height-s.reservedRows())...)
	case s.agentPicker != nil:
		dw, dh := s.dialogSize()
		content := renderPickerDialog(s.Theme, s.Tier, dw, dh, "select an agent", *s.agentPicker)
		lines = append(lines, overlayRows(content, s.height-s.reservedRows())...)
	case s.overlay != "":
		lines = append(lines, overlayRows(s.overlay, s.height-s.reservedRows())...)
	default:
		lines = append(lines, s.transcript.Rows()...)
	}
	if v := s.approval.View(); v != "" {
		lines = append(lines, strings.Split(v, "\n")...)
	}
	lines = append(lines, s.statusRow())
	lines = append(lines, strings.Split(s.composer.View(), "\n")...)
	return s.gutter(lines)
}

// gutter frames every rendered row with one blank column each side: no
// text touches the screen edge. Rows are padded to the full width so
// background styles (the diff colours, the approval border) reach the
// edge cleanly while their glyphs stay off it.
func (s Screen) gutter(lines []string) string {
	if s.width < 3 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, len(lines))
	inner := contentWidth(s.width)
	for i, ln := range lines {
		pad := inner - ansi.StringWidth(ln)
		if pad < 0 {
			ln = ansi.Truncate(ln, inner, "")
			pad = 0
		}
		out[i] = " " + ln + strings.Repeat(" ", pad) + " "
	}
	return strings.Join(out, "\n")
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

// SetCommandRunner supplies the seam a slash command acts through (see
// commands.go). It is the integration knob docs/design/ui-isolation.md
// names for slash commands: the screen never inspects harness state
// directly, only this interface. A nil runner (the zero-value default)
// makes every "/x" line report an error instead of falling through to
// Send.
func (s *Screen) SetCommandRunner(r ports.CommandRunner) { s.runner = r }

// SetMouseOverrideHint records the terminal's mouse-override key
// (rule 6.5), shown in the help overlay. Empty clears it.
func (s *Screen) SetMouseOverrideHint(hint string) { s.mouseHint = hint }

// Notice pushes one permanent notice block into the transcript. Startup
// hazard warnings land here: they are part of the conversation record,
// not transient chrome, and they must appear exactly once.
func (s *Screen) Notice(text string) {
	next, _ := s.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindNotice,
		Body: uievent.NoticeBody{Text: text},
	})
	s.transcript = next
}

// statusRow is the permanent bottom status line.
//
// It is always drawn, even when there is nothing to say, because a row
// that appears and disappears reflows every wrapped line above it
// (docs/design/ux-rules.md rule 2.7). Its right side carries the compact
// key hint, generated from the keymap table so it cannot drift from the
// help screen; transient state (turn status, scroll affordances) takes
// the left. The whole row is one line, truncated to the terminal width.
func (s Screen) statusRow() string {
	line := s.statusText()
	hint := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).
		Render(s.keys.Hint(keymap.IDHelp, keymap.IDOpenPager, keymap.IDQuit))
	if line == "" {
		line = hint
	} else {
		line += "  " + hint
	}
	if s.width > 2 {
		line = ansi.Truncate(line, contentWidth(s.width), "")
	}
	return line
}

// statusText is the transient left side of the status row: the turn's
// status line, or the scroll and truncation affordances.
func (s Screen) statusText() string {
	if v := s.statusline.View(s.now()); v != "" {
		return v
	}
	if !s.transcript.Following() {
		if n := s.transcript.NewWhilePaused(); n > 0 {
			return render.Role(s.Theme, s.Tier, theme.RoleWarning).
				Render(fmt.Sprintf("%d new blocks while you read - ctrl+end to follow again", n))
		}
		return render.Role(s.Theme, s.Tier, theme.RoleWarning).
			Render("scrolled up - ctrl+end to follow again")
	}
	if n := s.transcript.Dropped(); n > 0 {
		return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).
			Render(fmt.Sprintf("%d earlier blocks dropped from this transcript", n))
	}
	return ""
}
