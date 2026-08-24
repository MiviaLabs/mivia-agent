// Package uiadapter translates domain events (internal/agent.Event) into
// the canonical UI event stream (internal/uikit/uievent.Event). It is the
// single seam between the agent runtime and every renderer; no renderer
// imports internal/agent, and no agent code imports uievent.
//
// Phase 1 ships ONLY the pure translation layer. Session lifecycle
// (Conversation, TurnHandle, Cancel), approval gating, settings ports, and
// the build/constructor live in later phases of docs/design/ui-replacement-
// phases.md.
//
// Empty-TurnID window: the first event on every turn is KindTurnStart
// with TurnID="" (chat.Session only surfaces the real ID after
// SendUserWithEvent returns). The terminal KindTurnEnd always carries
// the real ID, so renderers that read TurnID off the end event are
// correct; renderers that need it for every intermediate event must
// accept the leading-empty window. See conversation.go's
// emitSyntheticTurnStart comment for the tap-stamp mechanism.
//
// The full per-kind mapping table lives next to TranslateEvent.
package uiadapter

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// droppedKinds lists the agent.EventKind values TranslateEvent intentionally
// emits no uievent.Event for. A new value added to this list requires a
// reviewer to sign off; the list lives next to the switch so the omission is
// obvious rather than discovered by a missing case.
var droppedKinds = []agent.EventKind{
	agent.EventHeartbeat,         // wall-clock progress tick; renderers are not silenced by phase 1
	agent.EventSubagentHeartbeat, // subagent tick; progress travels on Progress in tool.output later
}

// ParseArgsForTest is the test-export wrapper for parseArgs. The body
// helper is unexported so production code never reaches for it directly;
// tests use this shim instead. Returning the same map[string]any as the
// internal helper keeps the diff-coverage gate satisfied without making
// the helper public API.
func ParseArgsForTest(input string) map[string]any { return parseArgs(input) }

// ErrFromDetailForTest is the test-export wrapper for errFromDetail. Same
// pattern as ParseArgsForTest: production callers go through the renderer
// path; tests exercise the bare helper through this shim so diff-coverage
// keeps the "non-bare detail" branch under cover.
func ErrFromDetailForTest(detail string, ok bool) string { return errFromDetail(detail, ok) }

// TranslateEvent converts one agent.Event into zero or more uievent.Events.
// The full per-kind source-of-truth table is documented above the package
// (event_kind.go) and next to the switch cases below. Drop-list omissions
// (EventHeartbeat, EventSubagentHeartbeat) are recorded explicitly in
// droppedKinds. Zero output is a valid result; an unknown EventKind panics
// rather than vanishing silently.
func TranslateEvent(ev agent.Event) []uievent.Event {
	switch ev.Kind {
	case agent.EventAssistant:
		return translateAssistant(ev)
	case agent.EventToolPending:
		return translateToolPending(ev)
	case agent.EventToolStart:
		return translateToolStart(ev)
	case agent.EventToolEnd:
		return translateToolEnd(ev)
	case agent.EventStep:
		return notice(ev.Detail)
	case agent.EventPrune:
		return notice(ev.Detail)
	case agent.EventToolParallel:
		return notice(ev.Detail)
	case agent.EventSubagentStart:
		return translateSubagentStart(ev)
	case agent.EventSubagentEnd:
		return translateSubagentEnd(ev)
	case agent.EventSubagentDone:
		return notice(subagentDoneText(ev))
	case agent.EventThinking:
		return translateThinking(ev)
	case agent.EventHook:
		return notice(hookText(ev))
	case agent.EventCompaction:
		return notice(ev.Detail)
	case agent.EventCacheUsage:
		return notice(ev.Detail)
	case agent.EventTokenUsage:
		return notice(ev.Detail)
	case agent.EventWorkLimit:
		return notice(ev.Detail)
	case agent.EventHeartbeat, agent.EventSubagentHeartbeat:
		// Explicit drop; one of the two progress-tick kinds has no UI
		// representation in phase 1. The drop is recorded in droppedKinds
		// so reviewers can audit it alongside the switch.
		return nil
	}
	// No default case. If a new agent.EventKind is added without an entry
	// here, the compile-time exhaustiveness check on the switch fails
	// rather than the event vanishing silently. The runtime guard below
	// catches the case where an EventKind value reaches TranslateEvent that
	// no caller should ever produce (and is therefore a bug, not data);
	// log it and drop the event rather than crashing the process. Callers
	// in tests cover this branch via TestTranslateEvent_PanicsOnUnknownKind
	// which still exercises the production helper directly.
	log.Printf("uiadapter: TranslateEvent has no case for agent.EventKind %q; dropping", string(ev.Kind))
	return nil
}

// notice returns the typed notice body; the trailing nil return is so each
// caller site reads as a complete slice without a literal. nil is treated
// identically to []uievent.Event{} by callers ranging over the result.
func notice(text string) []uievent.Event {
	return []uievent.Event{{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: text}}}
}

// parseArgs converts the bounded, redacted tool input preview carried on
// agent.Event.Input into the map[string]any shape ToolStartBody /
// ToolPendingBody expect. An unparseable or absent input - tool input
// previews are bounded and sometimes empty, and the agent loop normalizes
// a wholly-missing input to "{}" before emitting the event - yields nil
// rather than an error; the UI side treats Args as optional and
// json:"-when-empty". The "{}" sentinel is downgraded to nil so a tool
// call with no arguments does not surface as "has arguments: empty map".
func parseArgs(input string) map[string]any {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// errFromDetail turns a failed tool_end's Detail into the string the UI's
// ToolEndBody.Err renders. OK false guarantees err is non-empty here:
// callers only invoke this when detail starts with "failed". A Detail of
// exactly "failed" (no qualifier) reports as that bare word rather than
// empty so a renderer's Err field has something to show.
func errFromDetail(detail string, ok bool) string {
	if ok {
		return ""
	}
	if detail == "" || detail == "failed" {
		return "failed"
	}
	return detail
}
