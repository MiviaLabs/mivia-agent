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
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/welcome"
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

	conv          ports.Conversation
	approver      ports.Approver      // nil is valid: no approval wiring
	runner        ports.CommandRunner // nil is valid: every "/x" then shows an error, never sends
	modelPicker   *picker.Model       // non-nil while the /model picker is open
	agentPicker   *picker.Model       // non-nil while the /agents picker is open
	sessionPicker *sessionPicker      // non-nil while the /resume picker is open

	// threads resolves a dispatched subagent's conversation for the
	// panel's thread dialog; nil is valid (every entry then falls back
	// to the step-log view). Set via SetSubagentThreads, the same seam
	// SetCommandRunner uses.
	threads ports.SubagentThreads

	// embedded marks the subagent-thread construction of this same
	// Screen type: no top bar, no activity panel, wrapped event Msgs -
	// everything else is the identical main-chat machinery. See
	// thread.go.
	embedded bool

	// thread is the open subagent thread's embedded Screen (cached per
	// callID so reopening continues the same transcript); threadID is
	// the call it belongs to. See thread.go.
	thread   *Screen
	threadID string

	topbar     topbar.Model
	transcript transcript.Model
	composer   composer.Model
	statusline statusline.Model
	approval   approval.Model
	welcome    welcome.Model

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

	// panel is the session's touched-files pane: a derived, live view
	// over the tool-end diffs the transcript already renders. It
	// accumulates here because this screen sees every event; see
	// filespanel.go.
	panel panel

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
		welcome:    welcome.New(th, tier),
		panel:      newPanel(th, tier),
		keys:       keymap.New(keymap.Default()),
		now:        now,
	}
	s.approval.SetWidth(contentWidth(width))
	s.topbar = topbar.New(th, tier, conv.Model(), conv.ContextUsage(), contentWidth(width))
	if title := conv.Title(); title != "" {
		s.topbar.SetBreadcrumb([]string{title})
	}
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
	s.transcript.SetSize(s.chatWidth(), s.transcriptHeight())
}

// reflow re-applies the chat column's width to every component that
// renders into it. Toggling the panel and resizing the terminal both
// change that width; Update's reservedRows comparison cannot see a
// width-only change, so the explicit call is the only reliable trigger.
func (s *Screen) reflow() {
	w := s.chatWidth()
	s.composer.SetWidth(w)
	s.approval.SetWidth(w)
	s.resize()
}

func (s Screen) update(msg tea.Msg) (app.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	case uievent.EventMsg:
		return s.handleTurnEvent(msg.Event)
	case threadEventMsg:
		// A subagent thread's streamed event. Marked Msgs reaching the
		// MAIN screen are forwarded to the cached thread; the same Msg
		// reaching an EMBEDDED screen is its own turn's event. See
		// thread.go.
		if s.embedded {
			return s.handleTurnEvent(msg.event)
		}
		return s.forwardThreadMsg(msg)
	case threadEndedMsg:
		if s.embedded {
			s.statusline.Stop()
			s.approval.Clear()
			s.active = nil
			return s, nil
		}
		return s.forwardThreadMsg(msg)
	case turnEndedMsg:
		s.statusline.Stop()
		s.approval.Clear()
		s.active = nil
		// Session values move at turn boundaries: refresh the top bar's
		// model, context share, and title now, not per token.
		s.topbar.SetSession(s.conv.Model(), s.conv.ContextUsage())
		if title := s.conv.Title(); title != "" {
			s.topbar.SetBreadcrumb([]string{title})
		}
		return s, nil
	case approval.DecisionMsg:
		if s.approver != nil {
			s.approver.Resolve(msg.ToolCallID, msg.Decision)
		}
		return s, nil
	case statusline.TickMsg:
		next, cmd := s.statusline.Update(msg)
		s.statusline = next
		// Ticks are idempotent repaint clocks: the embedded thread (if
		// one is cached) gets its own copy.
		s.forwardSharedMsg(msg)
		return s, cmd
	case transcript.FlushMsg:
		next, cmd := s.transcript.Update(msg)
		s.transcript = next
		s.forwardSharedMsg(msg)
		return s, cmd
	case tea.MouseWheelMsg:
		return s.handleWheel(msg)
	case tea.MouseClickMsg:
		return s.handleClick(msg)
	case tea.MouseReleaseMsg:
		// Actions fire on the click, not the release: there is no
		// drag selection to complete (deferred behind rule 6.3's
		// cheaper scrollback handover), so release carries nothing.
		return s, nil
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.topbar.SetWidth(contentWidth(msg.Width))
		s.panel.offset = 0
		s.reflow()
		return s, nil
	case app.ThemeChangedMsg:
		return s.applyTheme(msg), nil
	}
	return s, nil
}

// applyTheme adopts a new theme across every component this screen
// owns. Theme and Tier are plain value fields all the way down - there
// is no shared pointer - so a component this misses keeps rendering in
// the old theme until it is rebuilt.
//
// The cached subagent-thread Screen is included: openThread reuses it
// for the same call ID, so a thread opened before the switch and
// reopened after it would otherwise come back in the previous theme.
func (s Screen) applyTheme(msg app.ThemeChangedMsg) Screen {
	s.Theme, s.Tier = msg.Theme, msg.Tier
	s.transcript.SetTheme(msg.Theme, msg.Tier)
	s.composer.SetTheme(msg.Theme, msg.Tier)
	s.statusline.SetTheme(msg.Theme, msg.Tier)
	s.approval.Theme, s.approval.Tier = msg.Theme, msg.Tier
	s.topbar.SetTheme(msg.Theme, msg.Tier)
	s.panel.list.Theme, s.panel.list.Tier = msg.Theme, msg.Tier
	s.welcome.SetTheme(msg.Theme, msg.Tier)
	if s.thread != nil {
		next := s.thread.applyTheme(msg)
		s.thread = &next
	}
	return s
}

// handleWheel applies one mouse-wheel notch. Wheel events scroll the
// conversation; CockpitScrollLines is the multiplier: terminals
// disagree on how many events one physical notch produces, and some
// amplify while others send exactly one
// (docs/design/cockpit-research.md rule 6.6).
func (s Screen) handleWheel(msg tea.MouseWheelMsg) (app.Screen, tea.Cmd) {
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
	// The content dialog covers the chat column; scrolling a transcript
	// the user cannot see acts on something invisible (the same rule
	// that dismisses overlays on any key). The dialog scrolls by
	// keyboard only.
	if s.panel.dialog {
		return s, nil
	}
	s.transcript = s.transcript.ScrollBy(step)
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
	return s, tea.Batch(cmd, s.awaitEvent(handle.Events()))
}

func (s Screen) handleTurnEvent(ev uievent.Event) (app.Screen, tea.Cmd) {
	next, flushCmd := s.transcript.HandleEvent(ev)
	s.transcript = next

	switch b := ev.Body.(type) {
	case uievent.ToolPendingBody:
		s.approval.SetRequest(b)
		s.statusline.SetLabel("pending")
		// The approval prompt claims the chat column and every key; a
		// content dialog open over that column would hide the prompt it
		// pre-empts, so it closes.
		s.panel.dialog, s.panel.dialogAgent = false, ""
	case uievent.ToolStartBody:
		s.approval.Clear()
		s.statusline.SetLabel("running")
	case uievent.ToolOutputBody:
		// A progress-bearing output is a subagent status update (see
		// uievent.ToolOutputBody): the panel's subagents section feeds
		// from the same stream the transcript renders.
		if b.Progress != nil {
			s.panel.observeAgent(b.ToolCallID, b.Progress)
		}
	case uievent.ToolEndBody:
		s.approval.Clear()
		s.statusline.SetLabel("thinking")
		if b.Diff != nil {
			// The panel's data, fed live: every completed edit appears
			// as a touched file the moment it happens, exactly as the
			// transcript renders it. Deletions carry no diff in the
			// event vocabulary, so only edits and creations record here.
			s.panel.appendLive(*b.Diff)
		}
	case uievent.TurnEndBody:
		s.approval.Clear()
	}

	var readCmd tea.Cmd
	if s.active != nil {
		readCmd = s.awaitEvent(s.active.Events())
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
	// the top bar, a one-row margin under it so content never touches its
	// edge, the composer and its menu, and the status row. The embedded
	// subagent-thread construction has no top bar: the dialog frame it
	// renders inside is the chrome above it.
	rows := s.composer.Height() + 1
	if !s.embedded {
		rows += s.topbar.Height() + 1
	}
	if s.approval.Active() {
		rows += s.approval.Height() // bordered box: title, optional diff preview, hint, and the border rows
	}
	return rows
}

// View draws the cockpit. The transcript area fills the surface between
// the top bar's margin and the chrome below (approval prompt, status
// row, composer), and the composer is the last row. With the panel open
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
	if !s.embedded {
		// The top bar may draw a second breadcrumb row (topbar.Height
		// accounts for it); its rows land as frame rows, then the margin.
		lines = append(lines, strings.Split(s.topbar.View(), "\n")...)
		lines = append(lines, "")
	}
	if !s.embedded && s.panelIsSplit() {
		lines = append(lines, s.panelFrameRows()...)
		return s.gutter(lines)
	}
	switch {
	case s.modelPicker != nil || s.agentPicker != nil || s.sessionPicker != nil || s.overlay != "":
		lines = append(lines, s.centerRows()...)
	case !s.embedded && s.panel.open:
		lines = append(lines, s.narrowPanelRows()...)
	case !s.embedded && s.transcript.Empty():
		lines = append(lines, s.welcome.Rows(s.chatWidth(), s.transcriptHeight())...)
	default:
		lines = append(lines, s.transcript.Rows()...)
	}
	lines = append(lines, s.chatTailRows()...)
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
	// Paint the theme's own surface under every cell, including the cells
	// between styled runs. The screen is the last thing that can do this:
	// without it the terminal's background shows through and a theme
	// change looks like it did nothing, because the largest coloured area
	// on screen never changes. FillBG is a no-op at a tier without
	// colour, so NO_COLOR output is unaffected.
	return render.FillBG(s.Theme, s.Tier, theme.RoleBG, strings.Join(out, "\n"))
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
func (s *Screen) SetCommands(cmds []composer.Command) {
	s.composer.SetCommands(cmds)
	if s.thread != nil {
		s.thread.SetCommands(cmds)
	}
}

// SetCommandRunner supplies the seam a slash command acts through (see
// commands.go). It is the integration knob docs/design/ui-isolation.md
// names for slash commands: the screen never inspects harness state
// directly, only this interface. A nil runner (the zero-value default)
// makes every "/x" line report an error instead of falling through to
// Send.
func (s *Screen) SetCommandRunner(r ports.CommandRunner) {
	s.runner = r
	if s.thread != nil {
		s.thread.SetCommandRunner(r)
	}
}

// SetSubagentThreads supplies the seam the activity panel's thread
// dialog resolves a subagent's conversation through (see thread.go).
// The same integration-knob shape SetCommandRunner uses: nil (the
// zero-value default) makes every subagent entry fall back to the
// read-only step-log view.
func (s *Screen) SetSubagentThreads(t ports.SubagentThreads) { s.threads = t }

// ObserveAgent records a subagent progress update in the activity panel.
func (s *Screen) ObserveAgent(id string, pr *uievent.Progress) {
	s.panel.observeAgent(id, pr)
}

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
// the left. The whole row is one line, truncated to the chat column's
// width - inside the split's left pane it must not exceed the pane.
func (s Screen) statusRow() string {
	line := s.statusText()
	var hint string
	if s.embedded {
		hint = render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render("esc:close dialog")
	} else if s.quitArmed {
		prefix := s.keys.Hint(keymap.IDHelp, keymap.IDOpenPager, keymap.IDPanelToggle)
		warn := render.Role(s.Theme, s.Tier, theme.RoleWarning).Render("ctrl+c:press again to quit")
		if prefix != "" {
			hint = render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render(prefix) + "  " + warn
		} else {
			hint = warn
		}
	} else {
		hint = render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).
			Render(s.keys.Hint(keymap.IDHelp, keymap.IDOpenPager, keymap.IDPanelToggle, keymap.IDQuit))
	}
	if line == "" {
		line = hint
	} else {
		line += "  " + hint
	}
	if s.width > 2 {
		line = ansi.Truncate(line, s.chatWidth(), "")
	}
	return line
}

// statusText is the transient left side of the status row: the turn's
// status line, or the scroll and truncation affordances.
func (s Screen) statusText() string {
	if v := s.statusline.View(s.now()); v != "" {
		return v
	}
	// Narrow panel open: the transcript is hidden behind the list, so
	// its scroll affordances would narrate something the user cannot
	// see.
	if s.panel.open && !s.panelIsSplit() {
		return ""
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
