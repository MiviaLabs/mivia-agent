package uiadapter

import (
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Per-kind source-of-truth mapping. TranslateEvent's switch in event.go
// delegates to one helper below per case. The per-row contract tests
// live in event_test.go and exercise every constant listed here.
//
//	EventAssistant        -> text.delta ("delta"); text.end otherwise
//	EventToolPending      -> tool.pending
//	EventToolStart        -> tool.start
//	EventToolEnd          -> tool.end
//	EventStep             -> notice
//	EventHeartbeat        -> dropped
//	EventPrune            -> notice
//	EventToolParallel     -> notice
//	EventSubagentStart    -> tool.start
//	EventSubagentEnd      -> tool.end
//	EventSubagentHeartbeat -> tool.output progress (Step parsed from Detail)
//	EventSubagentDone     -> notice + tool.output progress (when Origin.TaskID is set)
//	EventThinking         -> reasoning.delta
//	EventHook             -> hook
//	EventCompaction       -> notice
//	EventCacheUsage       -> notice
//	EventTokenUsage       -> notice
//	EventWorkLimit        -> notice
//	EventSchemaRetry      -> notice
//	EventEmptyResponseRetry -> notice
//	EventUnactedContinuation -> notice

// translateAssistant fans out an assistant content event into the right
// uievent shape based on the Detail mode the agent loop set. A streaming
// chunk ("delta") becomes a text.delta with the chunk text; an interim
// batched content ("interim") and the final empty-Detail pass become a
// text.end carrying the full accumulated text for one-time markdown render.
//
// An empty Content on either mode is dropped: deltas with no payload, and
// interim/final events that arrived before any text was produced, are not
// representable in the current uievent body set. That guard - not the
// unknown-Detail fallback below - is what keeps the loop's content-free
// "complete" flag (events.DetailAssistantComplete) off the TUI; carrying
// text on it would render a text.end per iteration. Unknown Detail values
// fall back to text.end so a future agent addition that introduces a new
// mode still emits something visible rather than vanishing: only an
// unrecognized EventKind (caught at the switch in TranslateEvent) is fatal.
func translateAssistant(ev agent.Event) []uievent.Event {
	if ev.Content == "" {
		return nil
	}
	if ev.Detail == "delta" {
		return []uievent.Event{{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{
			Text: ev.Content,
		}}}
	}
	// "interim", "" (final), and any future mode all collapse to text.end.
	return []uievent.Event{{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{
		Text: ev.Content,
	}}}
}

// translateToolPending maps EventToolPending to a tool.pending uievent with
// the agent loop's bounded, redacted input parsed into Args.
func translateToolPending(ev agent.Event) []uievent.Event {
	return []uievent.Event{{Kind: uievent.KindToolPending, Body: uievent.ToolPendingBody{
		ToolCallID: ev.ToolCallID,
		Name:       ev.Name,
		Args:       parseArgs(ev.Input),
	}}}
}

// translateToolStart maps EventToolStart to a tool.start uievent with the
// agent loop's bounded, redacted input parsed into Args. Attribution for
// the call (which subagent dispatched it, etc.) lives on the input event
// but does not flow into tool.start's body; the UI keeps subagent rows
// distinguishable via parallel tool events that carry their own Origin.
func translateToolStart(ev agent.Event) []uievent.Event {
	return []uievent.Event{{Kind: uievent.KindToolStart, Body: uievent.ToolStartBody{
		ToolCallID: ev.ToolCallID,
		Name:       ev.Name,
		Args:       parseArgs(ev.Input),
	}}}
}

// translateToolEnd maps EventToolEnd to a tool.end uievent. OK is derived
// from Detail per the agent loop's toolEndDetail vocabulary ("completed"
// vs anything starting with "failed"); the same string is reused as Err
// so a renderer has something to show without re-parsing Detail.
func translateToolEnd(ev agent.Event) []uievent.Event {
	detail := ev.Detail
	ok := detail != "" && !strings.HasPrefix(detail, "failed")
	var diff *uievent.Diff
	if ok {
		diff = parseToolDiff(ev.Name, ev.Input, ev.Output)
	}
	return []uievent.Event{{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{
		ToolCallID: ev.ToolCallID,
		Name:       ev.Name,
		OK:         ok,
		Result:     ev.Output,
		Err:        errFromDetail(detail, ok),
		Diff:       diff,
	}}}
}

// translateSubagentStart reuses the tool.start body shape so the UI shows
// the row alongside other tool calls; subagent progress (heartbeats,
// nested tool calls) travels separately on Progress in tool.output and
// arrives later in the same turn rather than getting smuggled into start.
func translateSubagentStart(ev agent.Event) []uievent.Event {
	return translateToolStart(ev)
}

// laneLog wraps one note for Progress.Log, and returns nil for an empty note
// so an empty log entry never renders as a blank line.
func laneLog(detail string) []string {
	if detail == "" {
		return nil
	}
	return []string{detail}
}

// translateAssistantReset maps the turn-restart signal to a transcript reset.
//
// The turn is being re-driven from the beginning, so any assistant text the
// transcript is holding for it belongs to an attempt that no longer exists.
// Without this the replay is appended to the abandoned attempt and the reader
// sees the answer twice.
func translateAssistantReset(ev agent.Event) []uievent.Event {
	return []uievent.Event{{
		Kind: uievent.KindAssistantReset,
		Body: uievent.AssistantResetBody{Reason: ev.Detail},
	}}
}

// translateSubagentBegin maps the RUN-level opening signal to a progress
// update on that run's own row, keyed by Origin.TaskID exactly as
// translateSubagentDone and translateSubagentHeartbeat are.
//
// It is not a tool.start: no tool call is beginning. It exists so the row
// appears, with the task it was given, before the run makes its first tool
// call - which is the whole reason the event was added.
func translateSubagentBegin(ev agent.Event) []uievent.Event {
	out := notice(subagentBeginText(ev))
	if ev.Origin.TaskID == "" {
		// No run key to attach a row to, so the notice is all this can say.
		// It is still said: a run that announces itself and produces nothing
		// is indistinguishable from a run that never started.
		return out
	}
	return append(out, uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: ev.Origin.TaskID,
			// Log carries the task text: Progress has no free-text field,
			// and Log is what the row already renders for a run's latest
			// human-readable note.
			Progress: &uievent.Progress{Status: "running", Log: laneLog(ev.Detail)},
		},
	})
}

// subagentBeginText mirrors subagentDoneText so a run's opening and closing
// notices read as a pair.
func subagentBeginText(ev agent.Event) string {
	if desc := ev.Origin.TaskDescription; desc != "" {
		return "subagent started: " + desc
	}
	if name := ev.Origin.Agent; name != "" {
		return "subagent started: " + name
	}
	if ev.Name != "" {
		return "subagent started: " + ev.Name
	}
	return "subagent started"
}

// translateSubagentEnd reuses the tool.end body shape; the same OK / Err
// derivation as ordinary tool calls applies. Origin attribution rides on
// the input event and is preserved by callers that thread TurnID / Seq
// through.
func translateSubagentEnd(ev agent.Event) []uievent.Event {
	return translateToolEnd(ev)
}

// translateThinking maps EventThinking's chain-of-thought delta to a
// reasoning.delta uievent. Empty content is dropped: an emit with no
// payload is not representable in the current uievent body set.
func translateThinking(ev agent.Event) []uievent.Event {
	if ev.Content == "" {
		return nil
	}
	return []uievent.Event{{Kind: uievent.KindReasoning, Body: uievent.ReasoningDeltaBody{
		Text: ev.Content,
	}}}
}

// translateSubagentDone maps EventSubagentDone to its notice advisory
// line, plus - when Origin.TaskID identifies the producing subagent - a
// tool.output progress update carrying a terminal Status for that
// subagent's OWN row (keyed the same way conversation/events.go's
// dispatchTaskIDs keys a dispatch_tasks fan-out's per-task rows).
//
// Without this, a subagent's row only ever left "running" via the
// ENCLOSING tool call's own tool.end (observeToolEnd), and a dispatch_tasks
// call blocks until every dispatched task finishes - so a batch of rows
// all flipped to their terminal status at once, only after the slowest
// subagent finished, however long the fastest one had already been done.
//
// Status is the done event's own terminal classification
// (agent.Event.Status: "completed" | "canceled" | "timed_out" | "error"),
// stamped by the deferred emit site in subagents.MultiStepHandler.run from
// the run's real exit error. Empty keeps the old optimistic default:
// production emitters always set the status now, but an unclassified
// (legacy) emitter still gets today's behavior instead of a blank row
// state. The authoritative ok/failed status still lands moments later
// from the enclosing call's own tool.end / per-task JSON result and
// overwrites this unconditionally (panel.setAgentStatus /
// observeAgentGroupEnd).
func translateSubagentDone(ev agent.Event) []uievent.Event {
	status := ev.Status
	if status == "" {
		status = "completed"
	}
	out := notice(subagentDoneText(ev))
	if ev.Origin.TaskID == "" {
		return out
	}
	return append(out, uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: ev.Origin.TaskID,
			Progress:   &uievent.Progress{Status: status},
		},
	})
}

// translateSubagentHeartbeat maps EventSubagentHeartbeat to a tool.output
// progress update for that subagent's OWN row (keyed by Origin.TaskID, the
// same key translateSubagentDone and conversation/events.go's
// dispatchTaskIDs use), so a blocking dispatch_tasks shows live per-task
// liveness - latest detail per row - instead of silent "running" rows until
// the whole batch settles. Renderers coalesce by replacing the row's progress
// state; emitting one event per heartbeat is intentional and bounded by the
// heartbeat cadence.
//
// Step and ToolCalls carry the steps=N/toolcalls=N counts parsed from the
// heartbeat Detail ("elapsed=Xs steps=N toolcalls=N", written by
// subagents.emitHeartbeat/heartbeatDetail). The files panel keys its stall
// clock on the step count: heartbeats whose step count is frozen are
// liveness without forward motion, so they must not refresh a "still
// working" clock. Detail text a parser cannot read leaves that field 0,
// and 0 never counts as progress downstream.
//
// Heartbeats without a TaskID cannot be attributed to any row and are
// dropped rather than guessed at. Status is always "running": a heartbeat is
// definitionally pre-terminal; the terminal transition still arrives via
// translateSubagentDone / the enclosing tool.end exactly as before.
func translateSubagentHeartbeat(ev agent.Event) []uievent.Event {
	if ev.Origin.TaskID == "" {
		return nil
	}
	var log []string
	if ev.Detail != "" {
		log = []string{ev.Detail}
	}
	return []uievent.Event{{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: ev.Origin.TaskID,
			Progress: &uievent.Progress{
				Status:    "running",
				Step:      heartbeatStep(ev.Detail),
				ToolCalls: heartbeatToolCalls(ev.Detail),
				Log:       log,
			},
		},
	}}
}

// heartbeatCount parses the value following key+"=" in a heartbeat Detail
// line, up to the next space or the end of the string - so a field is
// readable whether it is the last one on the line ("steps=2") or followed
// by another ("steps=2 toolcalls=5"). Any detail without a parseable,
// non-negative count - a missing key, raw loop EventStep remaps, plain
// prose - returns 0, which downstream code treats as "no progress
// information", never as count 0 of real work.
func heartbeatCount(detail, key string) int {
	_, after, ok := strings.Cut(detail, key+"=")
	if !ok {
		return 0
	}
	count, _, _ := strings.Cut(after, " ")
	n, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// heartbeatStep parses the steps=N field of a heartbeat Detail line.
func heartbeatStep(detail string) int { return heartbeatCount(detail, "steps") }

// heartbeatToolCalls parses the toolcalls=N field of a heartbeat Detail
// line. Absent on a heartbeat emitted before this field existed (or any
// other unparseable shape), it reads as "no tool-call count", identically
// to heartbeatStep's zero-value contract.
func heartbeatToolCalls(detail string) int { return heartbeatCount(detail, "toolcalls") }

// subagentDoneText picks the most informative label available on the
// subagent's Origin for a terminal "subagent done" advisory line:
// TaskDescription first (it says what the subagent was doing), then Agent
// name (which subagent), then the bare placeholder when neither is set.
func subagentDoneText(ev agent.Event) string {
	if desc := ev.Origin.TaskDescription; desc != "" {
		return "subagent done: " + desc
	}
	if name := ev.Origin.Agent; name != "" {
		return "subagent done: " + name
	}
	return "subagent done"
}

// translateHook maps EventHook to a hook uievent carrying the program, tool,
// input, and output an operator needs to answer "did my hook fire, and what
// did it do" - a bare notice string cannot carry that shape (Output was
// silently dropped when this case fell through to notice(hookText(ev))).
func translateHook(ev agent.Event) []uievent.Event {
	return []uievent.Event{{Kind: uievent.KindHook, Body: uievent.HookBody{
		Event:   ev.Name,
		Program: ev.Program,
		Tool:    ev.Tool,
		Input:   ev.Input,
		Output:  ev.Output,
		Denied:  ev.Denied,
	}}}
}
