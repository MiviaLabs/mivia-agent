package conversation

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/blackboard"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/history"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/queue"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

type sessionState struct {
	conv         ports.Conversation
	transcript   transcript.Model
	composer     composer.Model
	active       ports.TurnHandle
	statusline   statusline.Model
	approval     approval.Model
	history      history.Model
	queueOverlay queue.Model
	blackboard   blackboard.Model
	panel        panel
	threads      ports.SubagentThreads
	queue        []string
	pendingForce *string
	// liveUsage is this session's own in-flight turn accounting. It is
	// per-session state like everything above: held only on the Screen, one
	// session's reading overrode the top bar for whichever session the user
	// switched to, and the owning session's TurnEnd - delivered on the
	// background path - could never clear it.
	liveUsage *ports.Usage
	// live/liveEvents are this session's live view of a background
	// automation conversation (see live_view.go). Kept across
	// switch-away so the run's events keep updating the snapshotted
	// transcript while the user is on another tab.
	live       ports.LiveSubscription
	liveEvents <-chan uievent.Event
}

func (st *sessionState) handleTurnEvent(ev uievent.Event) {
	st.transcript, _ = st.transcript.HandleEvent(ev)
	switch b := ev.Body.(type) {
	case uievent.ToolPendingBody:
		st.approval.SetRequest(b)
		st.statusline.SetLabel("pending")
		st.statusline.SetDetail(toolDetail(b.Name, b.Args))
		st.panel.dialog, st.panel.dialogAgent = false, ""
	case uievent.ToolStartBody:
		// This call's own prompt only - see the same rule in events.go.
		st.approval.Resolve(b.ToolCallID)
		st.statusline.SetLabel("running")
		st.statusline.SetDetail(toolDetail(b.Name, b.Args))
		// The SAME helpers the foreground path uses. Hand-rolled copies
		// here dropped a dispatch group's per-task rows and every
		// blackboard message a backgrounded session raised.
		observeToolStartInto(&st.panel, st.threads, b)
		recordBlackboardToolInto(&st.blackboard, b.Name, b.Args)
	case uievent.ToolOutputBody:
		if b.Progress != nil {
			st.panel.observeAgent(b.ToolCallID, b.Progress)
		}
	case uievent.ToolEndBody:
		st.approval.Resolve(b.ToolCallID)
		st.statusline.SetLabel("thinking")
		observeToolEndInto(&st.panel, b)
	case uievent.UsageBody:
		st.statusline.SetCost(b.CostUSD)
	case uievent.TurnEndBody:
		st.approval.ClearAll()
		st.panel.reconcileTerminal(b.Reason)
		// The turn is over, so this session's committed estimate is
		// authoritative again - exactly as on the foreground path. Left
		// set, the snapshotted reading overrode the top bar the moment the
		// user switched back, and nothing else could ever clear it.
		st.liveUsage = nil
	}
}

func (s Screen) convID() string {
	if s.conv == nil || s.conv.ID() == "" {
		return "default"
	}
	return s.conv.ID()
}

// snapshotSessionState captures everything belonging to the session being
// switched away from, so it resumes exactly as it was left. Every field here
// is per-session: one held on the Screen instead leaks into whichever
// session the user switches to (liveUsage did exactly that).
func (s *Screen) snapshotSessionState() *sessionState {
	return &sessionState{
		conv:         s.conv,
		transcript:   s.transcript,
		composer:     s.composer,
		active:       s.active,
		statusline:   s.statusline,
		approval:     s.approval,
		history:      s.history,
		queueOverlay: s.queueOverlay,
		blackboard:   s.blackboard,
		panel:        s.panel,
		threads:      s.threads,
		queue:        s.queue,
		pendingForce: s.pendingForce,
		liveUsage:    s.liveUsage,
		live:         s.liveSub,
		liveEvents:   s.liveEvents,
	}
}

func (s *Screen) dismissModals() {
	s.closeThread()
	s.hideComposer = false
	s.modelPicker = nil
	s.agentPicker = nil
	s.sessionPicker = nil
	s.palettePicker = nil
	s.effortPicker = nil
	s.login = nil
	s.overlay = ""
}

func (s *Screen) applySessionState(st *sessionState) {
	s.transcript = st.transcript
	s.composer = st.composer
	s.active = st.active
	s.statusline = st.statusline
	s.approval = st.approval
	s.history = st.history
	s.queueOverlay = st.queueOverlay
	s.blackboard = st.blackboard
	s.panel = st.panel
	s.queue = st.queue
	s.pendingForce = st.pendingForce
	s.liveUsage = st.liveUsage
	s.liveSub = st.live
	s.liveEvents = st.liveEvents
	if st.threads != nil {
		s.threads = st.threads
	}
}

func (s *Screen) switchConversation(newConv ports.Conversation) tea.Cmd {
	if newConv == nil {
		return nil
	}
	if s.compaction != nil {
		s.compaction.Cancel()
		// The compaction belongs to the session being saved below. Stop its
		// activity mark before copying the statusline into that session's
		// state; the canceled worker may still emit a late Done event.
		s.statusline.Stop()
		s.compaction = nil
		s.compactionSessionID = ""
		s.compactionCancelRequested = false
	}
	if s.sessions == nil {
		s.sessions = make(map[string]*sessionState)
	}

	s.dismissModals()

	// Save current session state. The conversation being switched away
	// from stops being screen-owned: its sends no longer install the
	// process-wide subagent-progress registrar (ForegroundMarker), and
	// its live view - if armed - rides the snapshot so the run's events
	// keep updating the off-screen transcript.
	if s.conv != nil {
		if fm, ok := s.conv.(ports.ForegroundMarker); ok {
			fm.SetForeground(false)
		}
		s.sessions[s.convID()] = s.snapshotSessionState()
	}

	s.conv = newConv
	newID := s.convID()
	s.transcript.SetModel(newConv.Model().Name)
	s.registerSession(newID)

	s.syncRunnerActiveSession(newID)

	if cr, ok := s.runner.(interface{ Commands() []composer.Command }); ok {
		s.commands = cr.Commands()
	}

	var liveCmd tea.Cmd
	if st, ok := s.sessions[newID]; ok {
		s.applySessionState(st)
		s.composer.SetCommands(s.commands)
		s.composer.SetMentions(s.mentions)
		liveCmd = s.replayOrResumeLive(newID, st, newConv)
	} else {
		liveCmd = s.initFreshSessionState(newConv)
	}
	s.transcript.SetModel(newConv.Model().Name)

	// The top bar keeps the last usage reading it was handed, and
	// refreshTopbar deliberately falls back to it when the incoming session
	// has not priced a turn yet ("the last composition the bar held is the
	// best one available"). That fallback is only sound WITHIN one session,
	// so the bar is re-seeded from the session being switched to - its own
	// live reading, or nothing - before the refresh consults it.
	seed := ports.Usage{}
	if s.liveUsage != nil {
		seed = *s.liveUsage
	}
	s.topbar.SetUsage(seed)
	s.refreshTopbar()
	s.reflow()
	return liveCmd
}

// replayOrResumeLive reconnects a revisited session's live view. With a
// live view already armed, the read loop just resumes (its previous
// continuation died with the last update). Never subscribed or dropped
// (first visit, stale overflow, closed): the snapshotted transcript may
// have missed events, so replay from history and arm fresh.
func (s *Screen) replayOrResumeLive(newID string, st *sessionState, newConv ports.Conversation) tea.Cmd {
	if st.live != nil {
		return s.awaitLiveEvent(newID, st.liveEvents, st.live)
	}
	if s.active != nil {
		return nil
	}
	if _, capable := liveCapable(newConv); !capable {
		return nil
	}
	s.transcript = transcript.New(s.Theme, s.Tier)
	s.transcript.SetSize(s.chatWidth(), s.transcriptHeight())
	s.LoadHistory(newConv.History())
	return s.armSessionLive(newID, st)
}

// initFreshSessionState builds every component a first-visit session
// needs, replays its history, and arms its live view.
func (s *Screen) initFreshSessionState(newConv ports.Conversation) tea.Cmd {
	s.transcript = transcript.New(s.Theme, s.Tier)
	s.transcript.SetSize(s.chatWidth(), s.transcriptHeight())
	s.composer = composer.New(s.Theme, s.Tier, s.chatWidth())
	s.composer.SetCommands(s.commands)
	s.composer.SetMentions(s.mentions)
	s.active = nil
	s.queue = nil
	s.pendingForce = nil
	s.liveUsage = nil
	s.statusline = statusline.New(s.Theme, s.Tier)
	s.approval = approval.New(s.Theme, s.Tier)
	s.approval.SetWidth(contentWidth(s.width))
	s.history = history.New(s.Theme, s.Tier)
	s.history.SetWidth(contentWidth(s.width))
	s.queueOverlay = queue.New(s.Theme, s.Tier)
	s.queueOverlay.SetWidth(contentWidth(s.width))
	s.blackboard = blackboard.New(s.Theme, s.Tier)
	s.blackboard.SetWidth(contentWidth(s.width))
	s.panel = newPanel(s.Theme, s.Tier)
	s.LoadHistory(newConv.History())
	return s.adoptLive()
}

// syncRunnerActiveSession tells the runner which session is now on screen.
// switchConversation is the sole place s.conv changes, including the fast,
// cached-tab path (switchToSessionID's `if st, ok := s.sessions[id]` branch)
// that never calls s.runner.SelectSession again - without this call, a
// /model (or any other per-session runner command) issued after cycling
// back to an already-visited, idle tab kept acting on whichever session the
// runner last touched, refusing the switch on that OTHER session's
// activeTurns/switching state instead of this one's.
func (s *Screen) syncRunnerActiveSession(id string) {
	if s.runner != nil {
		s.runner.SetActiveSessionID(id)
	}
}

func (s Screen) handleEventMsg(msg uievent.EventMsg) (app.Screen, tea.Cmd) {
	if msg.SessionID != "" && s.convID() != msg.SessionID {
		if st, ok := s.sessions[msg.SessionID]; ok {
			st.handleTurnEvent(msg.Event)
			s.refreshTopbar()
			if st.active != nil {
				return s, s.awaitSessionEvent(msg.SessionID, st.active.Events())
			}
			return s, nil
		}
		// The session isn't (or is no longer) tracked in s.sessions, so its
		// transcript/statusline updates have nowhere to go. The channel this
		// event came from must still be drained: msg.Source is the read
		// loop's only remaining reference to it, and dropping it here would
		// permanently stop reading a channel its writer may still be filling
		// - the writer (the agent loop's synchronous event tap) then blocks
		// on the next send once the buffer fills, stalling that turn.
		if msg.Source != nil {
			return s, s.awaitSessionEvent(msg.SessionID, msg.Source)
		}
		return s, nil
	}
	return s.handleTurnEventFrom(msg.Event, msg.Source)
}

func (s Screen) handleTurnEndedMsg(msg turnEndedMsg) (app.Screen, tea.Cmd) {
	if msg.sessionID != "" && s.convID() != msg.sessionID {
		if st, ok := s.sessions[msg.sessionID]; ok {
			st.statusline.Stop()
			st.approval.ClearAll()
			st.panel.reconcileTerminal("interrupted")
			st.active = nil
			s.refreshTopbar()
			// The session's live view was paused for the local turn (its
			// events would have arrived twice). Drain first - a re-armed
			// queue send owns the view for its own turn - then arm the
			// live view again if no drain took over.
			next, cmd := s.drainTrackedSession(msg.sessionID, st)
			liveCmd := s.armSessionLive(msg.sessionID, st)
			if liveCmd != nil {
				return next, tea.Batch(cmd, liveCmd)
			}
			return next, cmd
		}
		return s, nil
	}
	s.statusline.Stop()
	s.approval.ClearAll()
	s.panel.reconcileTerminal("interrupted")
	s.active = nil
	s.refreshTopbar()

	if next, cmd, handled := s.drainPendingForce(); handled {
		return next, cmd
	}

	if len(s.queue) > 0 {
		nextText := s.queue[0]
		s.queue = s.queue[1:]
		if s.queueOverlay.Active() {
			s.queueOverlay.SetItems(s.queue)
		}
		next, cmd := s.sendText(nextText)
		sc := next.(Screen)
		if sc.active == nil {
			sc.queue = append([]string{nextText}, sc.queue...)
			if sc.queueOverlay.Active() {
				sc.queueOverlay.SetItems(sc.queue)
			}
		} else {
			sc.statusline.SetQueued(len(sc.queue))
		}
		return sc, cmd
	}

	// The foreground turn that paused this session's live view is over:
	// arm it again so subsequent run events keep streaming.
	liveCmd := s.adoptLive()
	if liveCmd != nil {
		return s, liveCmd
	}
	return s, nil
}

// drainTrackedSession runs a tracked (not on-screen) session's post-turn
// work: the re-queued force-send, then the queued prompts. Every Send
// here re-arms that session's own turn read, exactly as
// handleTurnEndedMsg's foreground tail does for the active session.
func (s Screen) drainTrackedSession(sessionID string, st *sessionState) (app.Screen, tea.Cmd) {
	if st.pendingForce != nil {
		forced := *st.pendingForce
		st.pendingForce = nil
		handle, err := st.conv.Send(context.Background(), intent.Send{Text: forced})
		if err == nil {
			st.active = handle
			st.statusline.Start("thinking", s.now())
			st.statusline.SetQueued(len(st.queue))
			s.refreshTopbar()
			return s, s.awaitSessionEvent(sessionID, handle.Events())
		}
		st.queue = append([]string{forced}, st.queue...)
		st.handleTurnEvent(uievent.Event{
			Kind: uievent.KindError,
			Body: uievent.ErrorBody{Text: fmt.Sprintf("send failed: %v (message re-queued)", err), Fatal: false},
		})
		st.statusline.Notice("send failed; re-queued")
		return s, nil
	}
	if len(st.queue) > 0 {
		nextText := st.queue[0]
		st.queue = st.queue[1:]
		handle, err := st.conv.Send(context.Background(), intent.Send{Text: nextText})
		if err == nil {
			st.active = handle
			st.statusline.Start("thinking", s.now())
			st.statusline.SetQueued(len(st.queue))
			s.refreshTopbar()
			return s, s.awaitSessionEvent(sessionID, handle.Events())
		}
		st.queue = append([]string{nextText}, st.queue...)
	}
	return s, nil
}
