package subagents

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// ToolCallStep is one bounded, redacted tool-call step recorded for a
// coordinator-dispatched subagent task. Input/Output reuse the ALREADY
// preview-bounded agent.Event.Input/Output strings (see
// internal/agent/loop_tool_preview.go) - this type adds no new unbounded
// surface.
type ToolCallStep struct {
	ToolCallID string
	Name       string
	Kind       string // "start" | "end"
	Input      string
	Output     string
	At         time.Time
}

// ToolCallSink receives one step at a time; the caller (the coordinator)
// owns buffering and capping. A nil sink is a no-op, so direct/
// non-coordinator Pool.Run() callers never see this wiring.
type ToolCallSink func(ToolCallStep)

type toolCallSinkKey struct{}

// ContextWithToolCallSink associates a ToolCallSink with ctx.
func ContextWithToolCallSink(ctx context.Context, sink ToolCallSink) context.Context {
	return context.WithValue(ctx, toolCallSinkKey{}, sink)
}

// ToolCallSinkFrom returns the ToolCallSink on ctx, if any. A bare context,
// or one holding a nil sink, reports not-ok.
func ToolCallSinkFrom(ctx context.Context) (ToolCallSink, bool) {
	sink, ok := ctx.Value(toolCallSinkKey{}).(ToolCallSink)
	return sink, ok && sink != nil
}

// toolCallStepFromEvent builds a ToolCallStep from a tool_start/tool_end
// agent.Event, reporting ok=false for any other kind so the caller does not
// have to duplicate the kind switch.
func toolCallStepFromEvent(e agent.Event, at time.Time) (ToolCallStep, bool) {
	switch e.Kind {
	case agent.EventToolStart:
		return ToolCallStep{ToolCallID: e.ToolCallID, Name: e.Name, Kind: "start", Input: e.Input, At: at}, true
	case agent.EventToolEnd:
		return ToolCallStep{ToolCallID: e.ToolCallID, Name: e.Name, Kind: "end", Output: e.Output, At: at}, true
	default:
		return ToolCallStep{}, false
	}
}
