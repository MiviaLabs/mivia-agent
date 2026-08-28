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
