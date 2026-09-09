package clichat

// Rendering of events RECEIVED from other processes onto this process's own
// NDJSON surface. Split from chat_hub.go, which owns joining and the per-run
// state, so both stay inside the structural budget.

import (
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// externalSubagentType maps a relayed subagent event to its own NDJSON type.
//
// A subagent's activity gets its OWN types rather than the root types with
// attribution fields added, which is the same rule the chat-sync wire follows
// and for the same reason: a consumer keyed on `type` - the only thing it can
// key on - must be able to keep a subagent's output out of the root turn.
// Before the split, every subagent's answer was appended to the root turn's
// answer stream by any consumer that had never heard of the origin fields,
// which is every consumer built before they existed.
//
// An older reader drops an unknown type with a warning. That is a real cost -
// one warning per dropped line - and it is still strictly better than silent
// corruption of the root turn's text.
func externalSubagentType(kind events.Kind) string {
	switch kind {
	case events.KindAssistant:
		return "external_subagent_chunk"
	case events.KindThinking:
		return "external_subagent_thinking"
	case events.KindAssistantReset:
		return "external_subagent_assistant_reset"
	case events.KindSubagentBegin:
		return "external_subagent_begin"
	case events.KindSubagentHeartbeat:
		return "external_subagent_heartbeat"
	case events.KindSubagentDone:
		return "external_subagent_done"
	case events.KindToolStart, events.KindSubagentStart:
		return "external_subagent_tool_start"
	case events.KindToolEnd, events.KindSubagentEnd:
		return "external_subagent_tool_end"
	default:
		return ""
	}
}

// isSubagentEvent reports whether a relayed event came from a subagent.
//
// Attribution is checked FIRST, but the kind matters independently: the
// subagent kinds are subagent-by-construction, and a peer that relays one
// without stamping an origin would otherwise have its subagent tool calls
// rendered as the root agent's own.
func isSubagentEvent(ev events.Event) bool {
	if ev.AgentTask != "" || ev.AgentName != "" || ev.AgentDepth > 0 {
		return true
	}
	switch ev.Kind {
	case events.KindSubagentBegin, events.KindSubagentStart,
		events.KindSubagentEnd, events.KindSubagentHeartbeat,
		events.KindSubagentDone:
		return true
	default:
		return false
	}
}

// withExternalOrigin copies an event's subagent attribution onto a relayed
// NDJSON line. The line's TYPE already says the event came from a subagent;
// these fields say WHICH RUN, which two runs of the same agent do not share.
func withExternalOrigin(line ndjsonEvent, ev events.Event) ndjsonEvent {
	line.OriginTaskID = ev.AgentTask
	line.OriginAgent = ev.AgentName
	line.OriginDepth = ev.AgentDepth
	return line
}

// renderExternalSubagentEvent relays one subagent event under its own type.
//
// It deliberately takes no *externalRun. A subagent shares its run id with the
// root loop, so any per-run state reachable here could be read or written for
// the wrong producer - which is exactly the defect the root path had, where a
// subagent's aggregate consumed the root's "already streamed" flag. Having no
// access to that state makes the mistake unrepresentable rather than guarded.
func renderExternalSubagentEvent(w io.Writer, ev events.Event) {
	lineType := externalSubagentType(ev.Kind)
	if lineType == "" {
		return
	}
	line := withExternalOrigin(ndjsonEvent{Type: lineType, RunID: ev.TurnID}, ev)

	switch ev.Kind {
	case events.KindAssistant, events.KindThinking:
		if ev.Content == "" {
			return
		}
		line.Text = ev.Content
	case events.KindAssistantReset:
		// The run is answering again. Detail is a content-free reason.
		line.Message = ev.Detail
	case events.KindSubagentBegin:
		// Detail carries the bounded task description the run was given.
		line.Name, line.Input = ev.Name, ev.Detail
	case events.KindSubagentHeartbeat:
		line.Message = ev.Detail
	case events.KindSubagentDone:
		// The run's terminal. Without it a relayed run opened and never
		// closed, so any live-agents view built on external_subagent_begin
		// pinned every subagent of the session forever. It is NOT
		// external_done: that ends the whole turn, and a subagent finishing
		// is one run inside a turn that continues.
		line.Name, line.Status = ev.Name, ev.Detail
	case events.KindToolStart, events.KindSubagentStart:
		line.ToolCallID, line.Name, line.Input = ev.ToolCallID, ev.Name, ev.Input
	case events.KindToolEnd, events.KindSubagentEnd:
		line.ToolCallID, line.Name, line.Output = ev.ToolCallID, ev.Name, ev.Output
		line.Status = toolEndStatus(ev.Detail)
	}
	writeNDJSONEvent(w, line)
}

// renderExternalTurnEvent relays one ROOT-loop event of an external turn.
//
// The KindAssistant case relays a "delta" chunk (streamed live - see
// teeWriter) as it arrives, and falls back to the turn-end aggregate
// (Detail != "delta") only when this run never streamed one at all. A run that
// already received live deltas would otherwise see the same content twice,
// once incrementally and once again in full.
//
// Every event reaching here is the root loop's: a subagent's is routed to
// renderExternalSubagentEvent before this is called. The run state below is
// therefore unambiguously the root's, which is why neither branch has to ask
// whose event it is holding.
func renderExternalTurnEvent(w io.Writer, r *externalRun, ev events.Event) {
	switch ev.Kind {
	case events.KindAssistant:
		if ev.Content == "" {
			break
		}
		line := ndjsonEvent{Type: "external_chunk", RunID: ev.TurnID, Text: ev.Content}
		if ev.Detail == "delta" {
			r.streamed = true
			writeNDJSONEvent(w, line)
			break
		}
		if !r.streamed {
			writeNDJSONEvent(w, line)
		}
	case events.KindAssistantReset:
		// The attempt this run already relayed is void. Clearing `streamed`
		// matters as much as the line itself: the replacement arrives as a
		// turn-end aggregate when the retry does not stream, and a run still
		// marked as having streamed would drop it, leaving the consumer with
		// the discard and no answer.
		r.streamed = false
		writeNDJSONEvent(w, ndjsonEvent{
			Type: "external_assistant_reset", RunID: ev.TurnID, Message: ev.Detail,
		})
	case events.KindThinking:
		if ev.Content != "" {
			writeNDJSONEvent(w, ndjsonEvent{
				Type: "external_thinking", RunID: ev.TurnID, Text: ev.Content,
			})
		}
	case events.KindToolStart:
		writeNDJSONEvent(w, ndjsonEvent{
			Type: "external_tool_start", RunID: ev.TurnID, ToolCallID: ev.ToolCallID,
			Name: ev.Name, Input: ev.Input,
		})
	case events.KindToolEnd:
		writeNDJSONEvent(w, ndjsonEvent{
			Type: "external_tool_end", RunID: ev.TurnID, ToolCallID: ev.ToolCallID,
			Name: ev.Name, Output: ev.Output, Status: toolEndStatus(ev.Detail),
		})
	// KindSubagentDone is deliberately NOT a case here: it retires one
	// subagent inside the turn, not the turn itself. Mapping it to
	// "external_done" (as this once did) made a consumer mark the whole
	// external turn finished - and drop it from any live-agents view -
	// the moment the run's first subagent completed, mid-turn.
	case events.KindTurnEnd:
		writeNDJSONEvent(w, ndjsonEvent{Type: "external_done", RunID: ev.TurnID})
	case events.KindError:
		writeNDJSONEvent(w, ndjsonEvent{Type: "external_error", RunID: ev.TurnID, Message: errorEventMessage(ev)})
	}
}
