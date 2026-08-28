package coordinator

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestTaskProgressCountsSurviveCaps pins the reason progress lives outside
// the capped raw steps: a task that blasts past bufferMaxStepsPerTask (and
// poisons ToolCallIDs via the byte cap) must still report its full tool-call
// count and its NEWEST last-activity stamp - the newest event is exactly the
// one a full buffer drops, and a chatty task reading stale would defeat the
// stall detection the counters exist for.
func TestTaskProgressCountsSurviveCaps(t *testing.T) {
	b := newRunToolCallBuffer()
	sink := b.sinkFor("task-1")

	const calls = bufferMaxStepsPerTask + 50 // blow the step cap
	base := time.Now().Add(-time.Hour)
	for i := 0; i < calls; i++ {
		sink(subagents.ToolCallStep{ToolCallID: "call", Name: "grep", Kind: "start", At: base.Add(time.Duration(i) * time.Second)})
		sink(subagents.ToolCallStep{ToolCallID: "call", Name: "grep", Kind: "end", At: base.Add(time.Duration(i)*time.Second + 500*time.Millisecond)})
	}

	p := b.progressSnapshot()["task-1"]
	if p.ToolCalls != calls {
		t.Fatalf("ToolCalls = %d, want %d (caps must not bound progress counters)", p.ToolCalls, calls)
	}
	wantLast := base.Add(time.Duration(calls-1)*time.Second + 500*time.Millisecond)
	if !p.LastActivity.Equal(wantLast) {
		t.Fatalf("LastActivity = %v, want the newest event %v (a full buffer drops exactly that one)", p.LastActivity, wantLast)
	}
	if p.LastTool != "grep" {
		t.Fatalf("LastTool = %q, want grep", p.LastTool)
	}
}

// TestTaskProgressPerTaskIsolation checks one task's counters stay its own.
func TestTaskProgressPerTaskIsolation(t *testing.T) {
	b := newRunToolCallBuffer()
	now := time.Now()
	b.sinkFor("a")(subagents.ToolCallStep{ToolCallID: "1", Name: "read_file", Kind: "start", At: now})
	b.sinkFor("b")(subagents.ToolCallStep{ToolCallID: "2", Name: "grep", Kind: "start", At: now.Add(time.Second)})

	snap := b.progressSnapshot()
	if snap["a"].ToolCalls != 1 || snap["a"].LastTool != "read_file" {
		t.Fatalf("task a progress = %+v", snap["a"])
	}
	if snap["b"].ToolCalls != 1 || snap["b"].LastTool != "grep" {
		t.Fatalf("task b progress = %+v", snap["b"])
	}
}

// TestTaskProgressResetKeepsLifetimeView pins reset's documented boundary: a
// retry redispatch clears the prior attempt's raw trace but the progress
// counters describe the task's lifetime liveness and must survive it.
func TestTaskProgressResetKeepsLifetimeView(t *testing.T) {
	b := newRunToolCallBuffer()
	now := time.Now()
	b.sinkFor("task-1")(subagents.ToolCallStep{ToolCallID: "1", Name: "read_file", Kind: "start", At: now})
	b.reset("task-1")

	if len(b.steps["task-1"]) != 0 {
		t.Fatal("reset must clear the raw trace")
	}
	if p := b.progressSnapshot()["task-1"]; p.ToolCalls != 1 {
		t.Fatalf("progress after reset = %+v, want lifetime counters preserved", p)
	}
}

// TestProgressSnapshotNilSafe covers the hand-built RunHandle path.
func TestProgressSnapshotNilSafe(t *testing.T) {
	var b *runToolCallBuffer
	if got := b.progressSnapshot(); got != nil {
		t.Fatalf("nil buffer progressSnapshot = %v, want nil", got)
	}
	var h *RunHandle
	if got := h.TaskProgress(); got != nil {
		t.Fatalf("nil handle TaskProgress = %v, want nil", got)
	}
}
