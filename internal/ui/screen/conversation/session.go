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
	}
}

func (s *Screen) dismissModals() {
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
}

func (s *Screen) switchConversation(newConv ports.Conversation) {
	if newConv == nil {
		return
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

	// Save current session state
	if s.conv != nil {
		s.sessions[s.convID()] = s.snapshotSessionState()
	}

	s.conv = newConv
	newID := s.convID()
	s.registerSession(newID)

	if st, ok := s.sessions[newID]; ok {
		s.applySessionState(st)
	} else {
		s.transcript = transcript.New(s.Theme, s.Tier)
		s.transcript.SetSize(s.chatWidth(), s.transcriptHeight())
		s.composer = composer.New(s.Theme, s.Tier, s.chatWidth())
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
	}

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
	return s.handleTurnEvent(msg.Event)
}

func (s Screen) handleTurnEndedMsg(msg turnEndedMsg) (app.Screen, tea.Cmd) {
	if msg.sessionID != "" && s.convID() != msg.sessionID {
		if st, ok := s.sessions[msg.sessionID]; ok {
			st.statusline.Stop()
			st.approval.ClearAll()
			st.panel.reconcileTerminal("interrupted")
			st.active = nil
			s.refreshTopbar()
			if st.pendingForce != nil {
				forced := *st.pendingForce
				st.pendingForce = nil
				handle, err := st.conv.Send(context.Background(), intent.Send{Text: forced})
				if err == nil {
					st.active = handle
					st.statusline.Start("thinking", s.now())
					return s, s.awaitSessionEvent(msg.sessionID, handle.Events())
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
					return s, s.awaitSessionEvent(msg.sessionID, handle.Events())
				}
				st.queue = append([]string{nextText}, st.queue...)
			}
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
		}
		return sc, cmd
	}

	return s, nil
}
