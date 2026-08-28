package cliorchestrate

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// TestTaskProgressInfoForTerminalTasks pins the omission half: terminal tasks
// never carry a progress block - their recorded outcome supersedes liveness,
// and a "3h since last tool call" reading on a completed task would be noise.
func TestTaskProgressInfoForTerminalTasks(t *testing.T) {
	now := time.Now()
	progress := map[string]coordinator.TaskProgress{
		"done": {ToolCalls: 5, LastTool: "grep", LastActivity: now.Add(-3 * time.Hour)},
	}
	for _, status := range []ledger.TaskStatus{
		ledger.TaskStatusCompleted, ledger.TaskStatusFailed,
		ledger.TaskStatusTimedOut, ledger.TaskStatusCanceled,
		ledger.TaskStatusBlocked,
	} {
		got := taskProgressInfoFor(ledger.TaskSnapshot{TaskID: "done", Status: string(status)}, progress, now)
		if got != nil {
			t.Errorf("status %q got progress %+v, want nil", status, got)
		}
	}
}

// TestTaskProgressInfoForRunningTasks pins the wedge-signal contract: a
// running task always carries a block, a zero block included (dispatched but
// never reached its first tool call), and LastActivity ages from the stamp
// the buffer recorded.
func TestTaskProgressInfoForRunningTasks(t *testing.T) {
	now := time.Now()
	progress := map[string]coordinator.TaskProgress{
		"chatty": {ToolCalls: 7, LastTool: "grep", LastActivity: now.Add(-95 * time.Second)},
		"silent": {},
	}
	got := taskProgressInfoFor(ledger.TaskSnapshot{TaskID: "chatty", Status: string(ledger.TaskStatusRunning)}, progress, now)
	if got == nil || got.ToolCalls != 7 || got.LastTool != "grep" {
		t.Fatalf("chatty progress = %+v", got)
	}
	if got.LastActivity != "1m35s" {
		t.Errorf("LastActivity = %q, want 1m35s", got.LastActivity)
	}

	got = taskProgressInfoFor(ledger.TaskSnapshot{TaskID: "silent", Status: string(ledger.TaskStatusRunning)}, progress, now)
	if got == nil {
		t.Fatal("running task with no activity must still carry a (zero) progress block")
	}
	if got.ToolCalls != 0 || got.LastTool != "" || got.LastActivity != "" {
		t.Fatalf("silent progress = %+v, want zero value", got)
	}
}

// TestTaskProgressInfoReportsTaskAge pins the clock half of the wedge
// contract: the consumer rule keys on time ("no tool call for 90s"), so the
// block must carry the task's own age - newest attempt start when one
// exists, creation time while no attempt has started (queued,
// retry_pending) - because a zero block alone cannot say whether the first
// provider turn is ten seconds or ten minutes old.
func TestTaskProgressInfoReportsTaskAge(t *testing.T) {
	now := time.Now()

	// Running task: age from the attempt start.
	got := taskProgressInfoFor(ledger.TaskSnapshot{
		TaskID: "r", Status: string(ledger.TaskStatusRunning),
		Attempts: []ledger.AttemptSnapshot{{StartedAt: now.Add(-2 * time.Minute)}},
	}, nil, now)
	if got == nil || got.TaskAge != "2m0s" {
		t.Fatalf("running progress = %+v, want task_age 2m0s", got)
	}

	// Never-started task: age from creation.
	got = taskProgressInfoFor(ledger.TaskSnapshot{
		TaskID: "q", Status: string(ledger.TaskStatusQueued),
		CreatedAt: now.Add(-5 * time.Minute),
	}, nil, now)
	if got == nil || got.TaskAge != "5m0s" {
		t.Fatalf("queued progress = %+v, want task_age 5m0s", got)
	}

	// A retried task ages from its newest attempt, not the first one.
	got = taskProgressInfoFor(ledger.TaskSnapshot{
		TaskID: "r2", Status: string(ledger.TaskStatusRunning),
		CreatedAt: now.Add(-30 * time.Minute),
		Attempts: []ledger.AttemptSnapshot{
			{StartedAt: now.Add(-20 * time.Minute)},
			{StartedAt: now.Add(-45 * time.Second)},
		},
	}, nil, now)
	if got == nil || got.TaskAge != "45s" {
		t.Fatalf("retried progress = %+v, want task_age 45s from the newest attempt", got)
	}

	// No clock known at all: the field stays empty, never a bogus value.
	got = taskProgressInfoFor(ledger.TaskSnapshot{TaskID: "z", Status: string(ledger.TaskStatusRunning)}, nil, now)
	if got == nil || got.TaskAge != "" {
		t.Fatalf("clock-less progress = %+v, want empty task_age", got)
	}
}

// TestTaskProgressInfoForUnknownTask covers a running task the buffer has no
// entry for (a hand-built handle, or a run resumed in another process): zero
// block, never a nil that would read as "terminal" downstream.
func TestTaskProgressInfoForUnknownTask(t *testing.T) {
	got := taskProgressInfoFor(ledger.TaskSnapshot{TaskID: "x", Status: string(ledger.TaskStatusRunning)}, nil, time.Now())
	if got == nil || got.ToolCalls != 0 {
		t.Fatalf("unknown-task progress = %+v, want zero block", got)
	}
}
