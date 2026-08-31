package chatsync

import (
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// maxTrackedTurns bounds active turns remembered by the projector.
const maxTrackedTurns = 64

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

type turnState struct {
	started           bool
	done              bool
	streamed          bool
	fragments         int
	thinkingFragments int
	bytes             int
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
	lastDrops           uint64
	currentTurn         string
}

// NewProjector constructs a Projector for sessionID starting at initialSeq.
func NewProjector(sessionID string, initialSeq int64, opts ProjectorOptions) *Projector {
	return &Projector{
		sessionID: sessionID,
		seq:       initialSeq,
		opts:      opts,
		turns:     make(map[string]*turnState),
	}
}

// LastSeq returns the current sequence number assigned by the projector.
func (p *Projector) LastSeq() int64 {
	return p.seq
}

// RollbackSeq rolls back the sequence counter by n (e.g. on outbox append failure).
func (p *Projector) RollbackSeq(n int) {
	p.seq -= int64(n)
}

// ResetSeq resets the sequence counter to a specified sequence (e.g. on fork).
func (p *Projector) ResetSeq(seq int64) {
	p.seq = seq
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
		return p.projectTurnEnd(env, ev.Detail)

	case events.KindError:
		if !p.knownTurn(turnID) {
			return nil
		}
		ts := p.turn(turnID)
		if ts.done {
			return nil
		}
		ts.done = true
		return p.projectTurnError(env, ev)

	case events.KindAssistant:
		ts := p.turn(turnID)
		return p.projectAssistant(env, turnID, ts, ev)

	case events.KindThinking:
		p.turn(turnID)
		return p.projectThinking(env, turnID, ev.Content)

	case events.KindToolStart, events.KindToolEnd:
		p.turn(turnID)
		return p.projectTool(env, ev)

	case events.KindSubagentStart, events.KindSubagentEnd, events.KindSubagentHeartbeat, events.KindSubagentDone:
		p.turn(turnID)
		return p.projectSubagent(env, ev)

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

func (p *Projector) turn(id string) *turnState {
	if t, ok := p.turns[id]; ok {
		p.touchTurn(id)
		return t
	}
	t := &turnState{}
	p.turns[id] = t
	p.turnOrder = append(p.turnOrder, id)
	for len(p.turnOrder) > maxTrackedTurns {
		delete(p.turns, p.turnOrder[0])
		p.turnOrder = p.turnOrder[1:]
	}
	return t
}

func (p *Projector) touchTurn(id string) {
	for i, cur := range p.turnOrder {
		if cur == id {
			p.turnOrder = append(p.turnOrder[:i], p.turnOrder[i+1:]...)
			p.turnOrder = append(p.turnOrder, id)
			return
		}
	}
}

func (p *Projector) knownTurn(id string) bool {
	_, ok := p.turns[id]
	return ok
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

func (p *Projector) buildEnvelope(ev events.Event, turnID string) Envelope {
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	env := Envelope{
		V:            1,
		At:           ts,
		Turn:         turnID,
		SourceTurnID: ev.TurnID,
		WriterID:     p.opts.WriterID,
	}
	if ev.AgentTask != "" || ev.AgentName != "" || ev.AgentDepth > 0 {
		env.Agent = &AgentOrigin{
			Task:  ev.AgentTask,
			Name:  ev.AgentName,
			Depth: ev.AgentDepth,
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
	env.Block = turnID + ":assistant"
	content := redactText(ev.Content)

	if ev.Detail == "delta" {
		ts.streamed = true
		ts.fragments++
		ts.bytes += len(ev.Content)
		if p.opts.StreamAssistant {
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

	// Final aggregate
	text := content
	fragments := 0
	if ts.streamed && p.opts.StreamAssistant {
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
	env.Block = turnID + ":thinking"
	text := ""
	if p.opts.IncludeThinking {
		text = redactText(content)
		text = applyTruncation(&env, "text", text, BudgetDeltaText)
	}
	ts := p.turn(turnID)
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
