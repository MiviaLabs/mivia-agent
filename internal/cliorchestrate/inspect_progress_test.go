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

// TestTaskProgressInfoForUnknownTask covers a running task the buffer has no
// entry for (a hand-built handle, or a run resumed in another process): zero
// block, never a nil that would read as "terminal" downstream.
func TestTaskProgressInfoForUnknownTask(t *testing.T) {
	got := taskProgressInfoFor(ledger.TaskSnapshot{TaskID: "x", Status: string(ledger.TaskStatusRunning)}, nil, time.Now())
	if got == nil || got.ToolCalls != 0 {
		t.Fatalf("unknown-task progress = %+v, want zero block", got)
	}
}
