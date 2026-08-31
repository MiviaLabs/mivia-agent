package chatsync

import (
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// ProjectorOptions configures a Projector.
type ProjectorOptions struct {
	StreamAssistant bool
	IncludeToolIO   bool
	IncludeThinking bool
}

// Projector performs pure synchronous projection of events.Event streams
// into WireEvent sequences for a single chat session.
type Projector struct {
	sessionID    string
	seq          int64
	opts         ProjectorOptions
	syntheticNum int
}

// NewProjector constructs a Projector for sessionID starting at initialSeq.
func NewProjector(sessionID string, initialSeq int64, opts ProjectorOptions) *Projector {
	return &Projector{
		sessionID: sessionID,
		seq:       initialSeq,
		opts:      opts,
	}
}

// LastSeq returns the current sequence number assigned by the projector.
func (p *Projector) LastSeq() int64 {
	return p.seq
}

// Project converts an events.Event into zero or more WireEvents.
// It filters out events not belonging to p.sessionID and unrelayed kinds.
// Seq numbering happens strictly after filtering.
func (p *Projector) Project(ev events.Event) []WireEvent {
	// Strict session filter: empty SessionID is rejected, sessionID must match.
	if ev.SessionID == "" || p.sessionID == "" || ev.SessionID != p.sessionID {
		return nil
	}

	turnID, isSynthetic := p.resolveTurnID(ev.TurnID)
	env := p.buildEnvelope(ev, turnID)

	switch ev.Kind {
	case events.KindTurnStart:
		return p.projectTurnStart(env, ev.Detail, isSynthetic)
	case events.KindTurnEnd:
		return p.projectTurnEnd(env, ev.Detail)
	case events.KindError:
		return p.projectTurnError(env, ev)
	case events.KindAssistant:
		return p.projectAssistant(env, turnID, ev)
	case events.KindThinking:
		return p.projectThinking(env, turnID, ev.Content)
	case events.KindToolStart, events.KindToolEnd:
		return p.projectTool(env, ev)
	case events.KindSubagentStart, events.KindSubagentEnd, events.KindSubagentHeartbeat, events.KindSubagentDone:
		return p.projectSubagent(env, ev)
	case events.KindCompaction:
		return p.projectCompaction(env, ev)
	default:
		return nil
	}
}

func (p *Projector) resolveTurnID(rawTurnID string) (string, bool) {
	if rawTurnID == "" {
		p.syntheticNum++
		return fmt.Sprintf("synthetic:%d", p.syntheticNum), true
	}
	return rawTurnID, false
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
	payload := &TurnStartedPayload{
		Envelope:  env,
		Text:      detail,
		Synthetic: isSynthetic,
	}
	return []WireEvent{p.nextWireEvent(TypeTurnStarted, payload)}
}

func (p *Projector) projectTurnEnd(env Envelope, detail string) []WireEvent {
	reason := detail
	if reason == "" {
		reason = "completed"
	}
	payload := &TurnEndedPayload{
		Envelope: env,
		Reason:   reason,
	}
	return []WireEvent{p.nextWireEvent(TypeTurnEnded, payload)}
}

func (p *Projector) projectTurnError(env Envelope, ev events.Event) []WireEvent {
	payload := &TurnFailedPayload{
		Envelope: env,
		Message:  errorEventMessage(ev),
	}
	return []WireEvent{p.nextWireEvent(TypeTurnFailed, payload)}
}

func (p *Projector) projectAssistant(env Envelope, turnID string, ev events.Event) []WireEvent {
	if ev.Content == "" {
		return nil
	}
	env.Block = turnID + ":assistant"
	if ev.Detail == "delta" && p.opts.StreamAssistant {
		payload := &AssistantDeltaPayload{
			Envelope: env,
			Text:     ev.Content,
			Index:    0,
		}
		return []WireEvent{p.nextWireEvent(TypeAssistantDelta, payload)}
	}
	if ev.Detail != "delta" {
		payload := &AssistantMessagePayload{
			Envelope:  env,
			Fragments: 0,
			Bytes:     len(ev.Content),
			Status:    "completed",
			Text:      ev.Content,
		}
		return []WireEvent{p.nextWireEvent(TypeAssistantMessage, payload)}
	}
	return nil
}

func (p *Projector) projectThinking(env Envelope, turnID, content string) []WireEvent {
	if content == "" {
		return nil
	}
	env.Block = turnID + ":thinking"
	payload := &ThinkingDeltaPayload{
		Envelope: env,
		Bytes:    len(content),
		Index:    0,
		Text:     content,
	}
	return []WireEvent{p.nextWireEvent(TypeThinkingDelta, payload)}
}

func (p *Projector) projectTool(env Envelope, ev events.Event) []WireEvent {
	env.Block = ev.ToolCallID
	if ev.Kind == events.KindToolStart {
		payload := &ToolStartedPayload{
			Envelope:   env,
			ToolCallID: ev.ToolCallID,
			Name:       ev.Name,
			InputBytes: len(ev.Input),
			Input:      ev.Input,
		}
		return []WireEvent{p.nextWireEvent(TypeToolStarted, payload)}
	}
	payload := &ToolEndedPayload{
		Envelope:    env,
		ToolCallID:  ev.ToolCallID,
		Name:        ev.Name,
		Status:      toolEndStatus(ev.Detail),
		OutputBytes: len(ev.Output),
		Detail:      ev.Detail,
		Output:      ev.Output,
	}
	return []WireEvent{p.nextWireEvent(TypeToolEnded, payload)}
}

func (p *Projector) projectSubagent(env Envelope, ev events.Event) []WireEvent {
	env.Block = ev.ToolCallID
	switch ev.Kind {
	case events.KindSubagentStart:
		payload := &SubagentToolStartedPayload{
			Envelope:   env,
			ToolCallID: ev.ToolCallID,
			Name:       ev.Name,
			InputBytes: len(ev.Input),
			Input:      ev.Input,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentToolStarted, payload)}
	case events.KindSubagentEnd:
		payload := &SubagentToolEndedPayload{
			Envelope:    env,
			ToolCallID:  ev.ToolCallID,
			Name:        ev.Name,
			Status:      toolEndStatus(ev.Detail),
			OutputBytes: len(ev.Output),
			Detail:      ev.Detail,
			Output:      ev.Output,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentToolEnded, payload)}
	case events.KindSubagentHeartbeat:
		payload := &SubagentProgressPayload{
			Envelope: env,
			Detail:   ev.Detail,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentProgress, payload)}
	case events.KindSubagentDone:
		payload := &SubagentEndedPayload{
			Envelope: env,
			Name:     ev.AgentName,
			Status:   ev.Detail,
		}
		return []WireEvent{p.nextWireEvent(TypeSubagentEnded, payload)}
	default:
		return nil
	}
}

func (p *Projector) projectCompaction(env Envelope, ev events.Event) []WireEvent {
	payload := &ContextCompactedPayload{
		Envelope:   env,
		Message:    ev.Detail,
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

func toolEndStatus(detail string) string {
	if detail == "" {
		return "ok"
	}
	return detail
}

func errorEventMessage(ev events.Event) string {
	if ev.Err != nil {
		return chat.TurnErrorMessage(ev.Err)
	}
	return ev.Detail
}
