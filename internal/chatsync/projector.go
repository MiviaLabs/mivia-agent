package chatsync

import (
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// ProjectorOptions configures a Projector.
//
// Settled decision 7 keeps this package a LEAF: it takes values and functions
// rather than importing the app's own packages. ErrorMessage and
// RedactToolArgs exist for that reason - the alternatives were an import of
// internal/chat (which drags in ~47 internal packages) and a read of
// internal/tools' process-global atomic.Bool (which makes any test of gate
// composition mutate a package global).
type ProjectorOptions struct {
	StreamAssistant bool
	IncludeToolIO   bool
	IncludeThinking bool
	WriterID        string

	// RedactToolArgs is the host's tool-argument redaction decision, passed
	// in as a value. It composes with IncludeToolIO as an AND: tool IO ships
	// only when IncludeToolIO is true AND RedactToolArgs is false.
	RedactToolArgs bool

	// ErrorMessage classifies a turn error into redaction-safe text for the
	// wire. Nil selects defaultErrorMessage, which is content-free by
	// construction. Provider and tool error text can quote the request that
	// produced it (DC-14), so err.Error() must never reach this wire.
	ErrorMessage func(error) string
}

// Projector performs pure synchronous projection of events.Event streams
// into WireEvent sequences for a single chat session.
type Projector struct {
	sessionID           string
	seq                 int64
	opts                ProjectorOptions
	syntheticNum        int
	activeSyntheticTurn string
	turns               map[string]*turnState
	turnOrder           []string
	// lanes holds one turnState per (turn, subagent task). Only the
	// streaming counters are used; started/done belong to a real turn.
	lanes       map[string]*turnState
	laneOrder   []string
	lastDrops   uint64
	currentTurn string
	// nextSegment is the one source of step ids for every turn and lane.
	// See turnState.segment.
	nextSegment int
}

// allocSegment mints a step id no stream of this projector has used.
func (p *Projector) allocSegment() int {
	id := p.nextSegment
	p.nextSegment++
	return id
}

// NewProjector constructs a Projector for sessionID starting at initialSeq.
func NewProjector(sessionID string, initialSeq int64, opts ProjectorOptions) *Projector {
	return &Projector{
		sessionID: sessionID,
		seq:       initialSeq,
		opts:      opts,
		turns:     make(map[string]*turnState),
		lanes:     make(map[string]*turnState),
	}
}

// LastSeq returns the current sequence number assigned by the projector.
func (p *Projector) LastSeq() int64 {
	return p.seq
}

// Project converts an events.Event into zero or more WireEvents.
func (p *Projector) Project(ev events.Event) []WireEvent {
	return p.ProjectWithDrops(ev, p.lastDrops)
}

// ProjectWithDrops checks the subscription drop counter and projects ev.
// If drops advanced, a sync.dropped event is emitted immediately before ev.
func (p *Projector) ProjectWithDrops(ev events.Event, currentDrops uint64) []WireEvent {
	// Strict session filter: empty SessionID is rejected, sessionID must match.
	if ev.SessionID == "" || p.sessionID == "" || ev.SessionID != p.sessionID {
		return nil
	}

	var out []WireEvent
	if dropEv := p.checkDrops(currentDrops); dropEv != nil {
		out = append(out, *dropEv)
	}

	turnID, isSynthetic := p.resolveTurnID(ev.TurnID, ev.Kind)
	p.currentTurn = turnID
	env := p.buildEnvelope(ev, turnID)

	projected := p.projectByKind(ev, turnID, env, isSynthetic)
	if len(projected) == 0 {
		return out
	}
	return append(out, projected...)
}

func (p *Projector) projectByKind(ev events.Event, turnID string, env Envelope, isSynthetic bool) []WireEvent {
	switch ev.Kind {
	case events.KindTurnStart:
		ts := p.turn(turnID)
		if ts.started || ts.done {
			return nil
		}
		ts.started = true
		return p.projectTurnStart(env, ev.Detail, isSynthetic)

	case events.KindTurnEnd:
		if !p.knownTurn(turnID) {
			return nil
		}
		ts := p.turn(turnID)
		if ts.done {
			return nil
		}
		ts.done = true
		// A turn's end closes its open thinking block. Without this the last
		// step's reasoning - the step that thought and then finished, calling
		// no tool - never settled and was dropped with the state.
		return append(p.settleThinkingFor(env, turnID, ev), p.projectTurnEnd(env, ev.Detail)...)

	case events.KindError:
		if !p.knownTurn(turnID) {
			return nil
		}
		ts := p.turn(turnID)
		if ts.done {
			return nil
		}
		ts.done = true
		return append(p.settleThinkingFor(env, turnID, ev), p.projectTurnError(env, ev)...)

	case events.KindAssistant:
		// The attribution check MUST come before p.turn(turnID). A subagent's
		// delta folded into the ROOT turn's state sets ts.streamed and
		// increments ts.fragments there, and the root's own aggregate then
		// takes the streamed branch and ships an EMPTY text with a non-zero
		// fragment count - a blank assistant message in every viewer, for a
		// root loop that never streamed a token itself.
		if isDispatched(ev) {
			return p.projectSubagentAssistant(env, turnID, ev)
		}
		ts := p.turn(turnID)
		return p.projectAssistant(env, turnID, ts, ev)

	case events.KindAssistantReset:
		return p.projectAssistantReset(env, turnID, ev)

	case events.KindThinking:
		if isDispatched(ev) {
			return p.projectSubagentThinking(env, turnID, ev)
		}
		p.turn(turnID)
		return p.projectThinking(env, turnID, ev.Content)

	case events.KindToolStart, events.KindToolEnd:
		settled := p.settleThinkingOnStepClose(env, turnID, ev)
		p.closeStepOnToolStart(ev, turnID)
		return append(settled, p.projectTool(env, ev)...)

	case events.KindSubagentBegin, events.KindSubagentStart, events.KindSubagentEnd,
		events.KindSubagentHeartbeat, events.KindSubagentDone:
		settled := p.settleThinkingOnStepClose(env, turnID, ev)
		p.closeStepOnToolStart(ev, turnID)
		return append(settled, p.projectSubagent(env, ev)...)

	case events.KindHook:
		return p.projectHook(env, ev)

	case events.KindCompaction:
		return p.projectCompaction(env, ev)

	default:
		return nil
	}
}

// Flush emits any pending sync.dropped event if drops advanced.
func (p *Projector) Flush(currentDrops uint64) []WireEvent {
	if dropEv := p.checkDrops(currentDrops); dropEv != nil {
		return []WireEvent{*dropEv}
	}
	return nil
}

func (p *Projector) checkDrops(currentDrops uint64) *WireEvent {
	if currentDrops <= p.lastDrops {
		return nil
	}
	delta := currentDrops - p.lastDrops
	p.lastDrops = currentDrops

	turn := p.currentTurn
	if turn == "" {
		turn = "synthetic:0"
	}
	payload := &SyncDroppedPayload{
		Envelope: Envelope{
			V:    1,
			At:   time.Now(),
			Turn: turn,
		},
		Dropped:      delta,
		TotalDropped: currentDrops,
	}
	ev := p.nextWireEvent(TypeSyncDropped, payload)
	return &ev
}

func (p *Projector) resolveTurnID(rawTurnID string, kind events.Kind) (string, bool) {
	if rawTurnID != "" {
		return rawTurnID, false
	}
	if kind == events.KindTurnStart || p.activeSyntheticTurn == "" {
		p.syntheticNum++
		p.activeSyntheticTurn = fmt.Sprintf("synthetic:%d", p.syntheticNum)
	}
	turnID := p.activeSyntheticTurn
	if kind == events.KindTurnEnd || kind == events.KindError {
		p.activeSyntheticTurn = ""
	}
	return turnID, true
}

// isDispatched reports whether ev was produced by a DISPATCHED run rather
// than the root loop, and is the single predicate every lane decision keys
// on.
//
// It must agree with buildEnvelope, which stamps AgentOrigin only when
// AgentDepth > 0. The four lane decisions used to ask "does it have a task
// id?" instead, and the two answers are not the same question: a task id is
// an attribution key that non-dispatch producers also set (the workflow
// progress sinks in internal/cliworkflow and internal/workflows/localengine
// publish AgentTask with no depth at all), so the type could say "subagent"
// on an event whose envelope carried no agent origin. A consumer splitting
// the main transcript from the subagent lanes on the type then files that
// event under a lane it cannot key, and the prose disappears from the main
// history with nothing to put it back.
//
// Depth is the honest signal: originForRequest (internal/subagents/
// multi_step.go) stamps req.Depth+1, so every dispatched run is at 1 or
// deeper and the root loop is at 0. The task id is still required, because
// the lane state and the block ids are keyed by it and a depth without a key
// is not a lane.
func isDispatched(ev events.Event) bool {
	return ev.AgentDepth > 0 && ev.AgentTask != ""
}

func (p *Projector) buildEnvelope(ev events.Event, turnID string) Envelope {
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	env := Envelope{
		V:        1,
		At:       ts,
		Turn:     turnID,
		WriterID: p.opts.WriterID,
	}
	// Ids and names take the same route as every other string that leaves this
	// machine. They are short by construction today, but "by construction"
	// means "by every producer that exists so far": an agent NAME comes from a
	// workspace-authored definition file, and one NUL in any of these rejects
	// the whole hundred-event batch it travels in.
	env.SourceTurnID = applyTruncation(&env, "source_turn_id", ev.TurnID, BudgetShortField)
	// Only a DISPATCHED run gets an origin. AgentOrigin identifies a subagent,
	// and the root loop is not one: it carries a task id like everything else
	// (stampRoutedOrigin fills an empty TaskID with the instance id), so
	// keying on "has a task" attributed the root agent's own events too.
	// Consumers split the main transcript from the subagent lanes on this
	// field, so the web viewer filed the root agent's prose and reasoning
	// under a lane nobody opens and rendered tool cards with nothing between
	// them. Depth is the honest signal: the root runs at 0, a dispatched run
	// at 1 or deeper.
	if ev.AgentDepth > 0 {
		env.Agent = &AgentOrigin{
			Task:       applyTruncation(&env, "agent_task", ev.AgentTask, BudgetShortField),
			Name:       applyTruncation(&env, "agent_name", ev.AgentName, BudgetShortField),
			Depth:      ev.AgentDepth,
			ParentTask: applyTruncation(&env, "agent_parent_task", ev.AgentParent, BudgetShortField),
		}
	}
	return env
}

func (p *Projector) projectTurnStart(env Envelope, detail string, isSynthetic bool) []WireEvent {
	text := redactText(detail)
	text = applyTruncation(&env, "text", text, BudgetPromptText)
	payload := &TurnStartedPayload{
		Envelope:  env,
		Text:      text,
		Synthetic: isSynthetic,
	}
	return []WireEvent{p.nextWireEvent(TypeTurnStarted, payload)}
}

func (p *Projector) projectTurnEnd(env Envelope, detail string) []WireEvent {
	reason := detail
	if reason == "" {
		reason = "completed"
	}
	reason = applyTruncation(&env, "reason", reason, BudgetShortField)
	payload := &TurnEndedPayload{
		Envelope: env,
		Reason:   reason,
	}
	return []WireEvent{p.nextWireEvent(TypeTurnEnded, payload)}
}

func (p *Projector) projectTurnError(env Envelope, ev events.Event) []WireEvent {
	msg := p.errorEventMessage(ev)
	msg = redactText(msg)
	msg = applyTruncation(&env, "message", msg, BudgetErrorMessage)
	payload := &TurnFailedPayload{
		Envelope: env,
		Message:  msg,
	}
	return []WireEvent{p.nextWireEvent(TypeTurnFailed, payload)}
}

func (p *Projector) projectAssistant(env Envelope, turnID string, ts *turnState, ev events.Event) []WireEvent {
	if ev.Content == "" {
		return nil
	}
	// A delta names the segment it is streaming into NOW; the settled aggregate
	// names the one its deltas actually used, which a tool call since then may
	// have left behind.
	seg := ts.blockSegment(ev.Detail == "delta")
	env.Block = proseBlock(turnID+":assistant", seg)
	content := redactText(ev.Content)

	if ev.Detail == "delta" {
		if p.opts.StreamAssistant && !redactionActive() {
			// streamed and fragments record what actually REACHED the wire.
			// Setting them before this gate made the settled message claim
			// fragments a viewer never got: a redaction policy installed
			// mid-turn - which a workflow tool can do - then produced an
			// empty message with a non-zero count and the whole answer was
			// unrecoverable.
			ts.streamed = true
			ts.fragments++
			// Same rule for the step counter: a segment is only open if prose
			// actually went out into it. And for the settle block: a segment
			// is only "used" once its delta shipped.
			ts.segmentAssistant++
			ts.recordDeltaSegment(seg)
			content = applyTruncation(&env, "text", content, BudgetDeltaText)
			payload := &AssistantDeltaPayload{
				Envelope: env,
				Text:     content,
				Index:    ts.fragments - 1,
			}
			return []WireEvent{p.nextWireEvent(TypeAssistantDelta, payload)}
		}
		return nil
	}

	// Final aggregate.
	//
	// The decision is `did any delta actually reach the wire`, NOT a re-run of
	// the gates. Re-evaluating them here let a mid-turn change of either one
	// disagree with what was sent: a policy installed mid-turn emptied a
	// message whose fragments were never sent, and a policy removed mid-turn
	// sent the answer a second time.
	text := content
	fragments := 0
	if ts.streamed && !ts.streamUnrecoverable {
		fragments = ts.fragments
		text = "" // INV-1: text empty iff fragments > 0
	} else {
		text = applyTruncation(&env, "text", text, BudgetAssistantText)
	}
	payload := &AssistantMessagePayload{
		Envelope:  env,
		Fragments: fragments,
		Bytes:     len(ev.Content),
		Status:    "completed",
		Text:      text,
	}
	return []WireEvent{p.nextWireEvent(TypeAssistantMessage, payload)}
}

func (p *Projector) projectThinking(env Envelope, turnID, content string) []WireEvent {
	if content == "" {
		return nil
	}
	ts := p.turn(turnID)
	env.Block = proseBlock(turnID+":thinking", ts.segment)
	text := ""
	// A redaction policy withholds the TEXT here rather than suppressing the
	// event. Thinking has no settled aggregate to fall back to - every
	// thinking event is a fragment - so suppressing would withhold the fact
	// that the agent is reasoning at all. Redacting per fragment cannot work:
	// a pattern spanning two fragments matches neither. Bytes still ship, so
	// a viewer shows activity without the content.
	if p.opts.IncludeThinking && !redactionActive() {
		text = redactText(content)
		text = applyTruncation(&env, "text", text, BudgetDeltaText)
	}
	// Accumulate for the settled aggregate regardless of the gate above. That
	// is the whole repair: when the gate withheld the fragment, this is the
	// only copy of the reasoning left, and it gets redacted as one string at
	// settle time rather than per fragment.
	ts.recordThinking(content, ts.segment, text != "")
	// Reasoning opens a step as surely as narration does: a step that thought
	// and then called a tool has closed something, even if it never spoke.
	ts.segmentThinking++
	index := ts.thinkingFragments
	ts.thinkingFragments++
	payload := &ThinkingDeltaPayload{
		Envelope: env,
		Bytes:    len(content),
		Index:    index,
		Text:     text,
	}
	return []WireEvent{p.nextWireEvent(TypeThinkingDelta, payload)}
}

func (p *Projector) projectCompaction(env Envelope, ev events.Event) []WireEvent {
	message := applyTruncation(&env, "message", ev.Detail, BudgetShortField)
	payload := &ContextCompactedPayload{
		Envelope:   env,
		Message:    message,
		Compaction: ev.Compaction,
	}
	return []WireEvent{p.nextWireEvent(TypeContextCompacted, payload)}
}

func (p *Projector) nextWireEvent(eventType string, payload any) WireEvent {
	p.seq++
	return WireEvent{
		Seq:     p.seq,
		Type:    eventType,
		Payload: payload,
	}
}

// errorEventMessage classifies an error event into wire text. The classifier
// is injected (ProjectorOptions.ErrorMessage) so this package does not import
// internal/chat; the host passes chat.TurnErrorMessage.
func (p *Projector) errorEventMessage(ev events.Event) string {
	if ev.Err != nil {
		classify := p.opts.ErrorMessage
		if classify == nil {
			classify = defaultErrorMessage
		}
		return classify(ev.Err)
	}
	return ev.Detail
}

// defaultErrorMessage is the fail-closed fallback when no classifier is
// injected. It never reads err.Error(), so it cannot leak request content; it
// matches the default branch of the host's own classifier and only loses that
// classifier's few sentinel-specific messages.
func defaultErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return "chat turn failed"
}
