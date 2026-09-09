package subagents

// heartbeatDetail format tests. The sidebar panel parses this string
// (heartbeatStep/heartbeatToolCalls in internal/uiadapter/event_kind.go) to
// drive the per-subagent Step and Tool calls counters, so its shape is a
// contract between this package and the UI translation layer.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

func TestHeartbeatDetailIncludesToolCalls(t *testing.T) {
	got := heartbeatDetail(10*time.Minute+40*time.Second, 29, 142)
	want := "elapsed=10m40s steps=29 toolcalls=142"
	if got != want {
		t.Fatalf("heartbeatDetail() = %q, want %q", got, want)
	}
}

func TestHeartbeatDetailZeroToolCalls(t *testing.T) {
	got := heartbeatDetail(5*time.Second, 0, 0)
	want := "elapsed=5s steps=0 toolcalls=0"
	if got != want {
		t.Fatalf("heartbeatDetail() = %q, want %q", got, want)
	}
}

// TestStepOnEventEmitsLiveHeartbeatOnProgress pins the fix for the sidebar
// showing stale/zero Step and Tools counts for most of a run's lifetime:
// the ticker-driven heartbeat only fires every 30s (subagents.emitHeartbeat),
// so a run that finishes - or makes many steps - well inside that window
// never got a single count update. stepOnEvent must now emit an immediate
// EventSubagentHeartbeat alongside every real step or tool-call event, so
// the sidebar tracks actual progress instead of a 30-second-or-never tick.
func TestStepOnEventEmitsLiveHeartbeatOnProgress(t *testing.T) {
	var stepCount, toolCallCount atomic.Int64
	var got []agent.Event
	h := &MultiStepHandler{}
	taskStart := time.Now().Add(-5 * time.Second)
	onEvent := h.stepOnEvent(context.Background(), func(e agent.Event) { got = append(got, e) }, &stepCount, &toolCallCount, taskStart)

	onEvent(agent.Event{Kind: agent.EventStep})
	onEvent(agent.Event{Kind: agent.EventToolStart, Name: "read_file"})
	onEvent(agent.Event{Kind: agent.EventToolEnd, Name: "read_file"})

	var heartbeats []agent.Event
	for _, e := range got {
		if e.Kind == agent.EventSubagentHeartbeat {
			heartbeats = append(heartbeats, e)
		}
	}
	if len(heartbeats) != 2 {
		t.Fatalf("got %d live heartbeats, want 2 (one per step/tool-start event; tool-end is not progress)", len(heartbeats))
	}
	if !strings.Contains(heartbeats[0].Detail, "steps=1 toolcalls=0") {
		t.Errorf("first heartbeat = %q, want steps=1 toolcalls=0", heartbeats[0].Detail)
	}
	if !strings.Contains(heartbeats[1].Detail, "steps=1 toolcalls=1") {
		t.Errorf("second heartbeat = %q, want steps=1 toolcalls=1", heartbeats[1].Detail)
	}
}

// TestStepOnEventNoLiveHeartbeatWithoutProgress pins the negative case: an
// event that is neither a step nor a tool-start (e.g. tool.end) must not
// spam an extra heartbeat - only real progress does.
func TestStepOnEventNoLiveHeartbeatWithoutProgress(t *testing.T) {
	var stepCount, toolCallCount atomic.Int64
	var got []agent.Event
	h := &MultiStepHandler{}
	onEvent := h.stepOnEvent(context.Background(), func(e agent.Event) { got = append(got, e) }, &stepCount, &toolCallCount, time.Now())

	onEvent(agent.Event{Kind: agent.EventToolEnd, Name: "read_file"})

	for _, e := range got {
		if e.Kind == agent.EventSubagentHeartbeat {
			t.Fatalf("got an unexpected live heartbeat for a non-progress event: %+v", got)
		}
	}
}

// TestStepOnEventCountsQueuedAndRunningStartOnce pins the fix for the
// subagent panel reporting exactly twice the tools that ran ("Tools: N" in
// internal/ui/screen/conversation/filespanel_layout.go, fed by this
// heartbeat's toolcalls= field), and for inspect_agents'
// progress.tool_calls, fed by the same stream through the ToolCallSink.
//
// The loop emits TWO EventToolStart events for ONE tool call - Detail
// "queued" from the PointPreTool hook, then Detail "running" from the
// dispatcher shim, both carrying the same ToolCallID
// (internal/agent/sdk_tool_events.go; pinned by
// internal/agent/agentloop_maxconcurrent_test.go: 3 calls => 6 events).
// The exact pair below is what a real one-tool run emits.
//
// Both legs must still reach the operator stream unchanged - that wire shape
// is a pinned contract - so the assertions separate what is forwarded from
// what is counted and recorded.
func TestStepOnEventCountsQueuedAndRunningStartOnce(t *testing.T) {
	var stepCount, toolCallCount atomic.Int64
	var got []agent.Event
	var steps []ToolCallStep
	h := &MultiStepHandler{}
	ctx := ContextWithToolCallSink(context.Background(), func(s ToolCallStep) { steps = append(steps, s) })
	onEvent := h.stepOnEvent(ctx, func(e agent.Event) { got = append(got, e) }, &stepCount, &toolCallCount, time.Now())

	onEvent(agent.Event{Kind: agent.EventToolStart, ToolCallID: "call_1", Name: "read_file", Detail: "queued", Input: `{"path":"a.go"}`})
	onEvent(agent.Event{Kind: agent.EventToolStart, ToolCallID: "call_1", Name: "read_file", Detail: "running"})
	onEvent(agent.Event{Kind: agent.EventToolEnd, ToolCallID: "call_1", Name: "read_file", Detail: "completed"})

	if n := toolCallCount.Load(); n != 1 {
		t.Fatalf("toolCallCount = %d, want 1 (one tool call, two tool_start legs)", n)
	}
	var heartbeats []agent.Event
	var forwardedStarts int
	for _, e := range got {
		switch e.Kind {
		case agent.EventSubagentHeartbeat:
			heartbeats = append(heartbeats, e)
		case agent.EventToolStart:
			forwardedStarts++
		}
	}
	if forwardedStarts != 2 {
		t.Errorf("forwarded %d tool_start events, want both legs (the operator wire shape is unchanged)", forwardedStarts)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("got %d live heartbeats, want 1 (only the first leg is progress)", len(heartbeats))
	}
	if !strings.Contains(heartbeats[0].Detail, "toolcalls=1") {
		t.Errorf("heartbeat = %q, want toolcalls=1", heartbeats[0].Detail)
	}
	var recordedStarts int
	for _, s := range steps {
		if s.Kind == "start" {
			recordedStarts++
		}
	}
	if recordedStarts != 1 {
		t.Errorf("recorded %d start steps, want 1: %+v", recordedStarts, steps)
	}
	if recordedStarts == 1 && steps[0].Input == "" {
		t.Errorf("recorded start step lost its Input; the kept leg must be the one carrying the args: %+v", steps[0])
	}
}
