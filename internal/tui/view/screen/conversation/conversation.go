package conversation

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	sel "github.com/MiviaLabs/mivia-agent/internal/tui/view/select"

	"github.com/charmbracelet/x/ansi"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/tui/kit/config"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/uievent"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/app"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/blackboard"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/history"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/queue"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/welcome"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/render"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/theme"
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
	palettePicker *picker.Model       // non-nil while the universal command palette is open
	effortPicker  *picker.Model       // non-nil while the /effort picker is open
	login         *loginDialog        // non-nil while the /login dialog is open

	// threads resolves a dispatched subagent's conversation for the
	// panel's thread dialog; nil is valid (every entry then falls back
	// to the step-log view). Set via SetSubagentThreads, the same seam
	// SetCommandRunner uses.
	threads ports.SubagentThreads

	// settings is the /settings screen's dependency knob: any field of
	// it may itself be nil, and the zero value (every field nil) is
	// valid - /settings still opens, every section reads "unavailable".
	// Set via SetSettings, the same seam SetCommandRunner uses.
	settings ports.Settings

	// remoteInputs is the inbound steering port (ports.RemoteInputs); nil is
	// valid - no channel means no remote-origin turns, ever. Set via
	// SetRemoteInputs, the same seam SetCommandRunner uses. See remote_input.go.
	remoteInputs <-chan ports.RemoteInputEvent

	// notices is the out-of-band advisory port (ports.Notices); nil is valid -
	// no channel means no out-of-turn advisories are rendered. Set via
	// SetNotices, the same seam SetRemoteInputs uses. See notices.go.
	notices <-chan uievent.Event

	// runActivity reports whether an automation run currently executes
	// on a session id (the automation service's RunActiveForSession,
	// wired by RunTUI). nil is valid: no send is ever refused for run
	// activity. See live_view.go for the guard and the live view.
	runActivity func(sessionID string) bool

	// liveSub/liveEvents are the CURRENT session's live view of a
	// background automation conversation (see live_view.go). Per-session
	// state like everything else: snapshotted on switch-away, restored
	// on revisit. nil is valid - a foreground session has neither.
	liveSub    ports.LiveSubscription
	liveEvents <-chan uievent.Event
	// workflowStatus is the replaceable liveness stream, separate from
	// notices; workflow is the newest value read from it, drawn on the status
	// row (status.go). The zero value means nothing is running and the row
	// says nothing about workflows.
	workflowStatus <-chan uievent.Event
	workflow       uievent.WorkflowStatusBody

	// mounter resolves untracked sessions on demand for remote steering.
	mounter ports.SessionMounter

	// mounting tracks in-flight mounts and queued inputs for unmounted sessions.
	mounting map[string][]ports.RemoteInputEvent

	// embedded marks the subagent-thread construction of this same
	// Screen type: no top bar, no activity panel, wrapped event Msgs -
	// everything else is the identical main-chat machinery. See
	// thread.go.
	embedded bool

	// hideComposer omits the composer row from layout and rendering (e.g.
	// when viewing subagent history).
	hideComposer bool

	// thread is the open subagent thread's embedded Screen (cached per
	// callID so reopening continues the same transcript); threadID is
	// the call it belongs to. See thread.go.
	thread   *Screen
	threadID string

	topbar       topbar.Model
	transcript   transcript.Model
	composer     composer.Model
	commands     []composer.Command
	mentions     []composer.Mention
	statusline   statusline.Model
	approval     approval.Model
	history      history.Model
	queueOverlay queue.Model
	blackboard   blackboard.Model
	welcome      welcome.Model

	sessions     map[string]*sessionState
	sessionOrder []string

	active                    ports.TurnHandle
	compaction                ports.CompactionHandle
	compactionSessionID       string
	compactionCancelRequested bool
	queue                     []string

	// pendingForce holds the FORCED text parked between the keypress and
	// the async turnEndedMsg; NOT the displaced turn's text - chat.Session's
	// stale-turn fence already preserves it.
	pendingForce *string

	now func() time.Time

	// tickArmed is the one spinner clock's in-flight flag, shared by
	// POINTER across every copy of this Screen (session switches, the
	// embedded thread screen, the value receivers this package returns)
	// because the clock is process-wide state, not per-copy state.
	//
	// statusline.TickMsg is self-re-arming: handling one returns the Cmd
	// for the next. Every UNCONDITIONAL statusline.TickCmd() therefore
	// starts a clock that lives until the whole surface goes idle, and a
	// second one does not replace the first - it runs beside it. Subagent
	// progress events (see events.go) arrive continuously while a dispatch
	// batch runs, so an unguarded arm there multiplied the clock once per
	// event: the marks animate N times too fast and the entire cockpit
	// repaints N times per interval, which is what makes input and
	// scrolling lag. armTick is the only way to start the clock.
	tickArmed *bool

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

	// liveUsage is the newest provider-reported token accounting for the
	// running turn, or nil when no turn has reported yet. It supersedes
	// the session's own estimate for as long as it is set: mid-turn the
	// session still measures the history it STARTED with (it adopts the
	// turn's messages only at commit), so the estimate is stale exactly
	// while a turn is growing the context. Cleared at the turn boundary,
	// where the committed - and possibly compacted - estimate becomes the
	// current answer again.
	liveUsage *ports.Usage

	lastClickTime time.Time
	lastClickX    int
	lastClickY    int
	// lastNavClickTime/Row detect a double-click on the sidebar's model
	// row (handleNavClick), the way lastClick* does for the top bar.
	lastNavClickTime time.Time
	lastNavClickRow  int
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
		sessions:     make(map[string]*sessionState),
		transcript:   transcript.New(th, tier),
		composer:     composer.New(th, tier, width),
		statusline:   statusline.New(th, tier),
		approval:     approval.New(th, tier),
		history:      history.New(th, tier),
		queueOverlay: queue.New(th, tier),
		blackboard:   blackboard.New(th, tier),
		welcome:      welcome.New(th, tier),
		panel:        newPanel(th, tier),
		keys:         keymap.New(keymap.Default()),
		now:          now,
		tickArmed:    new(bool),
	}
	s.approval.SetWidth(contentWidth(width))
	s.history.SetWidth(contentWidth(width))
	s.queueOverlay.SetWidth(contentWidth(width))
	s.blackboard.SetWidth(contentWidth(width))
	s.transcript.SetSize(contentWidth(width), 24)
	if conv != nil {
		s.registerSession(conv.ID())
		if rp, ok := conv.(interface{ ShowReasoning() bool }); ok {
			s.transcript = s.transcript.SetHideReasoning(!rp.ShowReasoning())
		}
		s.topbar = topbar.New(th, tier, conv.Model(), conv.ContextUsage(), contentWidth(width))
		s.transcript.SetModel(conv.Model().Name)
		if title := conv.Title(); title != "" {
			s.topbar.SetBreadcrumb([]string{title})
		}
		s.LoadHistory(conv.History())
	} else {
		s.topbar = topbar.New(th, tier, ports.ModelInfo{}, ports.Usage{}, contentWidth(width))
	}
	return s
}

func (s Screen) Init() tea.Cmd {
	return tea.Batch(s.awaitRemoteInput(), s.awaitNotice(), s.awaitWorkflowStatus())
}

// ViewFlags holds the alternate screen: the conversation is the cockpit.
func (s Screen) ViewFlags() app.ViewFlags { return app.ViewFlags{AltScreen: true} }

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
	if !ok {
		return next, cmd
	}
	if scr.reservedRows() != before {
		scr.resize()
	}
	// Keep the components' selection rects current: any layout change -
	// resize, reflow, approval armed/cleared, panel toggle - moves the
	// rows their highlight paints on.
	scr.syncSelectionRects()
	return scr, cmd
}

// handleAppSettingsMsg folds the app-level settings/routing messages into
// the immutable-update flow: ScreenResumedMsg refreshes the topbar, and the
// settings layer's permanent, host-authored disclosure (the full-disk live
// re-arm's never-silent notice) is folded into the transcript. The returned
// Screen replaces the router's stack entry.
func (s Screen) handleAppSettingsMsg(msg tea.Msg) (app.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case app.ScreenResumedMsg:
		s.refreshTopbar()
		return s, nil
	case app.SettingsNoticeMsg:
		return s.handleSettingsNoticeMsg(msg)
	}
	return s, nil
}

// handleSettingsNoticeMsg folds the settings layer's permanent, host-
// authored disclosure (the full-disk live re-arm's never-silent notice)
// into the transcript through the immutable-update flow: Notice mutates
// this local copy and the returned Screen replaces the router's entry.
func (s Screen) handleSettingsNoticeMsg(msg app.SettingsNoticeMsg) (app.Screen, tea.Cmd) {
	s.Notice(msg.Text)
	return s, nil
}

// contentWidth is the usable column count: the terminal minus the
// one-column gutter each side, so no component or message touches the
// screen edge. Below 3 columns the gutter gives way - there is nothing
// to frame.
func contentWidth(width int) int {
	if width <= 0 {
		return 80
	}
	if width < 3 {
		return width
	}
	return width - 2
}

func (s Screen) contentWidth() int {
	if s.width <= 0 {
		return 80
	}
	return contentWidth(s.width)
}

func contentHeight(height int) int {
	if height <= 0 {
		return 24
	}
	if height < 3 {
		return height
	}
	return height - 2
}

func (s Screen) contentHeight() int {
	if s.height <= 0 {
		return 24
	}
	if s.embedded {
		return s.height
	}
	return contentHeight(s.height)
}

// gutter frames every rendered row with one blank column each side, and
// one blank row at the top and bottom: no text touches the screen edge.
func (s Screen) gutter(lines []string) string {
	if (s.width > 0 && s.width < 3) && (s.height > 0 && s.height < 3) {
		return strings.Join(lines, "\n")
	}
	inner := s.contentWidth()
	blankRow := " " + strings.Repeat(" ", inner) + " "

	out := make([]string, 0, len(lines)+2)
	if s.height >= 3 && !s.embedded {
		out = append(out, blankRow)
	}
	for _, ln := range lines {
		pad := inner - ansi.StringWidth(ln)
		if pad < 0 {
			ln = ansi.Truncate(ln, inner, uikitconfig.ClipMarker)
			pad = 0
		}
		out = append(out, " "+ln+strings.Repeat(" ", pad)+" ")
	}
	if s.height >= 3 && !s.embedded {
		out = append(out, blankRow)
	}
	return render.FillBG(s.Theme, s.Tier, theme.RoleBG, strings.Join(out, "\n"))
}

// resize gives the transcript the rows the chrome does not claim.
func (s *Screen) resize() {
	s.transcript.SetSize(s.chatWidth(), s.transcriptHeight())
}

// reflow re-applies the chat column's width to every component that
// renders into it. Toggling the panel and resizing the terminal both
// change that width; Update's reservedRows comparison cannot see a
// width-only change, so the explicit call is the only reliable trigger.
// syncTopbarModel hides the top bar's model capsule and context badge
// while the sidebar is open: the sidebar's context and model sections
// say them instead, so each is named once on screen. Called from every
// path that opens or closes the panel.
func (s *Screen) syncTopbarModel() { s.topbar.SetSessionHidden(s.panel.open) }

func (s *Screen) reflow() {
	w := s.chatWidth()
	s.topbar.SetWidth(w)
	s.composer.SetWidth(w)
	s.approval.SetWidth(w)
	s.history.SetWidth(w)
	s.queueOverlay.SetWidth(w)
	s.blackboard.SetWidth(w)
	s.resize()
	s.refreshTopbar()
	if s.thread != nil {
		dw, _ := s.dialogSize()
		s.thread.setSurface(render.DialogBodyWidth(dw), s.panelBodyRows())
	}
}

func (s *Screen) refreshActivity() {
	if s.panel.open {
		s.topbar.SetActivity(0, 0)
	} else {
		s.topbar.SetActivity(len(s.panel.entries), s.panel.activeAgentCount())
	}
}

func (s *Screen) refreshTopbar() {
	if s.conv != nil {
		u := s.conv.ContextUsage()
		// A live provider-reported reading wins over the session estimate
		// while it is set. It is both more accurate (real prompt tokens,
		// not the len/4 heuristic) and more current (the session has not
		// adopted the running turn's messages yet). Without this the
		// refresh that follows every event handled overwrote the reading
		// the same event had just installed, which is what left the
		// gauge frozen at turn-start history for the whole turn.
		if s.liveUsage != nil {
			live := *s.liveUsage
			// The provider reports a total and no composition. The
			// session's estimate has the composition and is the only
			// source for it, so it is carried over and reconciled with
			// the authoritative total. Taking the live reading whole
			// would blank every bucket row for the length of a turn
			// while the header above them kept reporting a real share.
			parts := u.Breakdown
			if parts.Total() == 0 {
				// The session has not priced the running turn yet, so the
				// last composition the bar held is the best one available.
				parts = s.topbar.Usage().Breakdown
			}
			live.Breakdown = parts.WithLiveTotal(live.InputTokens)
			u = live
		} else if (u.InputTokens+u.OutputTokens == 0) && (s.topbar.Usage().InputTokens+s.topbar.Usage().OutputTokens > 0) {
			u = s.topbar.Usage()
		}
		s.topbar.SetSession(s.conv.Model(), u)
		s.transcript.SetModel(s.conv.Model().Name)
		if title := s.conv.Title(); title != "" {
			s.topbar.SetBreadcrumb([]string{title})
		} else {
			s.topbar.SetBreadcrumb(nil)
		}
	}
	s.refreshActivity()
	s.refreshTabs()
}

func (s Screen) update(msg tea.Msg) (app.Screen, tea.Cmd) {
	if next, cmd, handled := s.updateAsyncPortMsg(msg); handled {
		return next, cmd
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if next, cmd, handled := s.handleCompactionKey(msg); handled {
			return next, cmd
		}
		return s.handleKey(msg)
	case tea.PasteMsg:
		// Paste lands in the composer; see handlePaste for the why.
		return s.handlePaste(msg)
	case uievent.EventMsg:
		return s.handleEventMsg(msg)
	case threadEventMsg:
		if s.embedded {
			return s.handleTurnEvent(msg.event)
		}
		return s.forwardThreadMsg(msg)
	case threadEndedMsg:
		if s.embedded {
			s.statusline.Stop()
			s.approval.ClearAll()
			s.panel.reconcileTerminal("interrupted")
			s.active = nil
			return s, nil
		}
		return s.forwardThreadMsg(msg)

	case app.ScreenResumedMsg, app.SettingsNoticeMsg:
		return s.handleAppSettingsMsg(msg)
	case turnEndedMsg:
		return s.handleTurnEndedMsg(msg)
	case approval.DecisionMsg:
		if s.approver != nil {
			s.approver.Resolve(msg.ToolCallID, msg.Decision)
		}
		return s, nil
	case statusline.TickMsg:
		return s.handleStatuslineTick(msg)
	case compactionEventMsg:
		return s.handleCompactionMessage(msg.event)
	case sessionPickerTickMsg:
		return s.handleSessionPickerTick()
	case loginResultMsg:
		return s.applyCommandOutcome(msg.outcome)
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
		// Actions fire on the click, not the release: a drag's release is
		// consumed by the router's selection state machine, and an
		// ordinary release carries nothing.
		return s, nil
	case sel.CopyTextMsg:
		s.handleCopyToast(msg.Text)
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

// updateAsyncPortMsg handles every message this screen produces for ITSELF
// out of band: the two port readers that re-arm one value at a time
// (ports.RemoteInputs, ports.Notices) and the results of requests issued
// earlier in a Cmd. They share a shape the rest of update's switch does not -
// each one owns its own re-arm or completion - so they are dispatched
// together here, which also keeps update inside the repo's per-function line
// budget. handled is false for anything else, leaving update's switch
// authoritative.
func (s Screen) updateAsyncPortMsg(msg tea.Msg) (app.Screen, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case remoteInputMsg:
		next, cmd := s.handleRemoteInput(msg.event)
		return next, cmd, true
	case noticeMsg:
		next, cmd := s.handleNotice(msg.event)
		return next, cmd, true
	case workflowStatusMsg:
		next, cmd := s.handleWorkflowStatus(msg.event)
		return next, cmd, true
	case sessionMountedMsg:
		next, cmd := s.handleSessionMountedMsg(msg)
		return next, cmd, true
	case subagentTaskCancelResultMsg:
		next, cmd := s.handleSubagentTaskCancelResult(msg)
		return next, cmd, true
	case threadToolCallCancelResultMsg:
		next, cmd := s.handleThreadToolCallCancelResult(msg)
		return next, cmd, true
	case liveEventMsg:
		next, cmd := s.handleLiveEvent(msg)
		return next, cmd, true
	case liveDoneMsg:
		next, cmd := s.handleLiveDone(msg)
		return next, cmd, true
	case liveStaleMsg:
		next, cmd := s.handleLiveStale(msg)
		return next, cmd, true
	}
	return s, nil, false
}

// handleSessionPickerTick refreshes the open /resume picker's per-row
// activity state. A stray in-flight tick after the picker closed (or with
// no runner to ask) is a silent no-op: returning a nil Cmd lets the
// self-re-arming loop lapse instead of ticking forever.
func (s Screen) handleSessionPickerTick() (app.Screen, tea.Cmd) {
	if s.sessionPicker == nil || s.runner == nil {
		return s, nil
	}
	next := s.sessionPicker.refresh(s.runner.SessionActive)
	s.sessionPicker = &next
	return s, sessionPickerTickCmd()
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
