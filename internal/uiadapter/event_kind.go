package uiadapter

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Per-kind source-of-truth mapping. TranslateEvent's switch in event.go
// delegates to one helper below per case. The per-row contract tests
// live in event_test.go and exercise every constant listed here.
//
//	EventAssistant        -> text.delta ("delta"); text.end otherwise
//	EventToolStart        -> tool.start
//	EventToolEnd          -> tool.end
//	EventStep             -> notice
//	EventHeartbeat        -> dropped
//	EventPrune            -> notice
//	EventToolParallel     -> notice
//	EventSubagentStart    -> tool.start
//	EventSubagentEnd      -> tool.end
//	EventSubagentHeartbeat -> dropped
//	EventSubagentDone     -> notice
//	EventThinking         -> reasoning.delta
//	EventHook             -> notice
//	EventCompaction       -> notice
//	EventCacheUsage       -> notice
//	EventTokenUsage       -> notice
//	EventWorkLimit        -> notice

// translateAssistant fans out an assistant content event into the right
// uievent shape based on the Detail mode the agent loop set. A streaming
// chunk ("delta") becomes a text.delta with the chunk text; an interim
// batched content ("interim") and the final empty-Detail pass become a
// text.end carrying the full accumulated text for one-time markdown render.
//
// An empty Content on either mode is dropped: deltas with no payload, and
// interim/final events that arrived before any text was produced, are not
// representable in the current uievent body set. Unknown Detail values
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
	return []uievent.Event{{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{
		ToolCallID: ev.ToolCallID,
		Name:       ev.Name,
		OK:         ok,
		Result:     ev.Output,
		Err:        errFromDetail(detail, ok),
	}}}
}

// translateSubagentStart reuses the tool.start body shape so the UI shows
// the row alongside other tool calls; subagent progress (heartbeats,
// nested tool calls) travels separately on Progress in tool.output and
// arrives later in the same turn rather than getting smuggled into start.
func translateSubagentStart(ev agent.Event) []uievent.Event {
	return translateToolStart(ev)
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

// hookText picks the most informative label available on a hook event:
// Detail carries the one-line summary (the agent loop's hookRunDetail),
// and a hook fired with no Detail falls back to the hook event name
// (PreToolUse / PostToolUse) so a row has something to display.
func hookText(ev agent.Event) string {
	if ev.Detail != "" {
		return ev.Detail
	}
	return ev.Name
}
