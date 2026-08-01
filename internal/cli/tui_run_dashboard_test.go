// Package cli - TUI run dashboard tests.
package cli

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

func TestRunDashboard_DeriveRunStatus(t *testing.T) {
	d := newRunDashboard()

	tests := []struct {
		name  string
		tasks map[string]string
		want  string
	}{
		{"no tasks", map[string]string{}, "created"},
		{"all completed", map[string]string{"t1": "completed", "t2": "completed"}, "completed"},
		{"one running", map[string]string{"t1": "running"}, "running"},
		{"one queued", map[string]string{"t1": "queued"}, "running"},
		{"one running one failed", map[string]string{"t1": "running", "t2": "failed"}, "degraded"},
		{"all failed", map[string]string{"t1": "failed"}, "failed"},
		{"canceled wins", map[string]string{"t1": "canceled"}, "canceled"},
		{"retry pending counts as running", map[string]string{"t1": "retry_pending"}, "running"},
		{"retry queued counts as queued", map[string]string{"t1": "retry_queued"}, "running"},
		{"interrupted counts as failed", map[string]string{"t1": "interrupted_unrecoverable"}, "failed"},
		{"timed out counts as failed", map[string]string{"t1": "timed_out"}, "failed"},
		{"blocked counts as unknown", map[string]string{"t1": "blocked"}, "unknown"},
		{"mixed completed and queued", map[string]string{"t1": "completed", "t2": "queued", "t3": "running"}, "running"},
		{"degraded with retry", map[string]string{"t1": "running", "t2": "retry_pending", "t3": "failed"}, "degraded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.deriveRunStatus(tt.tasks)
			if got != tt.want {
				t.Errorf("deriveRunStatus(%v) = %q, want %q", tt.tasks, got, tt.want)
			}
		})
	}
}

func TestRunDashboard_HandleEvent(t *testing.T) {
	d := newRunDashboard()

	// Simulate a run_created event.
	d.handleEvent(ledger.LifecycleEvent{
		RunID: "run-1",
		Kind:  "run_created",
	})

	// Verify run was created.
	if d.activeCount() != 1 {
		t.Fatalf("expected 1 active run after run_created, got %d", d.activeCount())
	}
	if d.totalCount() != 1 {
		t.Fatalf("expected 1 total run, got %d", d.totalCount())
	}

	// Simulate task events.
	d.handleEvent(ledger.LifecycleEvent{
		RunID:  "run-1",
		TaskID: "t1",
		Kind:   "task_running",
	})
	d.handleEvent(ledger.LifecycleEvent{
		RunID:  "run-1",
		TaskID: "t2",
		Kind:   "task_created",
	})
	d.handleEvent(ledger.LifecycleEvent{
		RunID:  "run-1",
		TaskID: "t2",
		Kind:   "task_running",
	})
	d.handleEvent(ledger.LifecycleEvent{
		RunID:  "run-1",
		TaskID: "t1",
		Kind:   "task_completed",
	})
	d.handleEvent(ledger.LifecycleEvent{
		RunID:  "run-1",
		TaskID: "t2",
		Kind:   "task_completed",
	})

	// Verify run-level status was inferred correctly.
	d.mu.RLock()
	info := d.runs["run-1"]
	d.mu.RUnlock()

	if info == nil {
		t.Fatal("run-1 not found after events")
	}
	if info.Status != "completed" {
		t.Fatalf("expected run status 'completed', got %q", info.Status)
	}
	if info.TaskCount != 2 {
		t.Fatalf("expected 2 tasks, got %d", info.TaskCount)
	}
	if info.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must not be zero (Bug 2 regression)")
	}
}

func TestRunDashboard_HandleEventRunLevel(t *testing.T) {
	d := newRunDashboard()

	// Test run-level events update status.
	d.handleEvent(ledger.LifecycleEvent{
		RunID:  "run-complete",
		TaskID: "t1",
		Kind:   "task_running",
	})
	d.handleEvent(ledger.LifecycleEvent{
		RunID: "run-complete",
		Kind:  "run_completed",
	})

	d.mu.RLock()
	info := d.runs["run-complete"]
	d.mu.RUnlock()
	if info == nil {
		t.Fatal("run-complete not found")
	}
	if info.Status != "completed" {
		t.Fatalf("run_completed event should set status to 'completed', got %q (Bug 10 regression)", info.Status)
	}
}

func TestRunDashboard_HandleEventRunFailed(t *testing.T) {
	d := newRunDashboard()

	d.handleEvent(ledger.LifecycleEvent{
		RunID:  "run-fail",
		TaskID: "t1",
		Kind:   "task_failed",
	})
	d.handleEvent(ledger.LifecycleEvent{
		RunID: "run-fail",
		Kind:  "run_failed",
	})

	d.mu.RLock()
	info := d.runs["run-fail"]
	d.mu.RUnlock()
	if info == nil {
		t.Fatal("run-fail not found")
	}
	if info.Status != "failed" {
		t.Fatalf("run_failed event should set status to 'failed', got %q", info.Status)
	}
}

func TestRunDashboard_HandleEventRunCanceled(t *testing.T) {
	d := newRunDashboard()

	d.handleEvent(ledger.LifecycleEvent{
		RunID:  "run-cancel",
		TaskID: "t1",
		Kind:   "task_running",
	})
	d.handleEvent(ledger.LifecycleEvent{
		RunID: "run-cancel",
		Kind:  "run_canceled",
	})

	d.mu.RLock()
	info := d.runs["run-cancel"]
	d.mu.RUnlock()
	if info == nil {
		t.Fatal("run-cancel not found")
	}
	if info.Status != "canceled" {
		t.Fatalf("run_canceled event should set status to 'canceled', got %q", info.Status)
	}
}

func TestRunDashboard_CreatedAtNotZero(t *testing.T) {
	// Regression test for Bug 2: CreatedAt must be set on creation.
	d := newRunDashboard()

	d.handleEvent(ledger.LifecycleEvent{
		RunID: "run-1",
		Kind:  "run_created",
	})

	d.mu.RLock()
	info := d.runs["run-1"]
	d.mu.RUnlock()

	if info == nil {
		t.Fatal("run-1 not found")
	}
	if info.CreatedAt.IsZero() {
		t.Fatal("MUTATION FAIL: CreatedAt is zero (Bug 2 regression)")
	}
	if time.Since(info.CreatedAt) > 5*time.Second {
		t.Fatalf("CreatedAt too old: %v", time.Since(info.CreatedAt))
	}
}

func TestRunDashboard_ActiveCount(t *testing.T) {
	d := newRunDashboard()

	// Empty -> 0.
	if d.activeCount() != 0 {
		t.Fatalf("expected 0 active runs on empty dashboard")
	}

	// Add running + completed runs.
	d.handleEvent(ledger.LifecycleEvent{RunID: "r1", Kind: "run_created"})
	d.handleEvent(ledger.LifecycleEvent{RunID: "r1", TaskID: "t1", Kind: "task_running"})
	d.handleEvent(ledger.LifecycleEvent{RunID: "r2", Kind: "run_created"})
	d.handleEvent(ledger.LifecycleEvent{RunID: "r2", TaskID: "t1", Kind: "task_completed"})
	d.handleEvent(ledger.LifecycleEvent{RunID: "r2", Kind: "run_completed"})

	// r1 is active (running), r2 is done.
	if d.activeCount() != 1 {
		t.Fatalf("expected 1 active run, got %d", d.activeCount())
	}
}

func TestRunDashboard_DismissRun(t *testing.T) {
	d := newRunDashboard()

	// Add a run directly (simulating backfillFromCoordinator path which sets HeldByAnotherExecutor).
	d.mu.Lock()
	d.runs["held-run-1"] = &dashRunInfo{
		RunID:                 "held-run-1",
		DisplayName:           "held-run",
		Status:                "running",
		HeldByAnotherExecutor: true,
	}
	d.runs["normal-run-1"] = &dashRunInfo{
		RunID:       "normal-run-1",
		DisplayName: "normal-run",
		Status:      "running",
	}
	d.mu.Unlock()

	// Confirm both runs exist.
	if d.totalCount() != 2 {
		t.Fatalf("expected 2 total runs before dismiss, got %d", d.totalCount())
	}

	// Dismiss the held-by-another-executor run.
	d.dismissRun("held-run-1")

	// Verify it's gone.
	if d.totalCount() != 1 {
		t.Fatalf("expected 1 run after dismiss, got %d", d.totalCount())
	}

	// Verify the normal run is still present.
	d.mu.RLock()
	_, heldExists := d.runs["held-run-1"]
	_, normalExists := d.runs["normal-run-1"]
	d.mu.RUnlock()

	if heldExists {
		t.Fatal("dismissed run (held-run-1) should not exist in dashboard")
	}
	if !normalExists {
		t.Fatal("normal-run-1 should still exist in dashboard after dismissing a different run")
	}

	// Dismiss a non-existent run should be a no-op (not panic).
	d.dismissRun("non-existent")
	if d.totalCount() != 1 {
		t.Fatalf("expected 1 run after dismissing non-existent ID, got %d", d.totalCount())
	}
}

func TestRunDashboard_TaskSummary(t *testing.T) {
	d := newRunDashboard()

	tests := []struct {
		name  string
		tasks map[string]string
		want  string
	}{
		{"empty", map[string]string{}, "0 task(s)"},
		{"single completed", map[string]string{"t1": "completed"}, "1/1 done"},
		{"mixed", map[string]string{"t1": "completed", "t2": "running", "t3": "queued"}, "1/3 done, 1 running, 1 queued"},
		{"retrying", map[string]string{"t1": "retry_pending"}, "1 retrying"},
		{"failed", map[string]string{"t1": "failed"}, "1 failed"},
		{"blocked", map[string]string{"t1": "blocked"}, "1 blocked"},
		{"all states", map[string]string{
			"t1": "completed", "t2": "running", "t3": "queued",
			"t4": "failed", "t5": "retry_pending", "t6": "blocked",
		}, "1/6 done, 1 running, 1 queued, 1 failed, 1 retrying, 1 blocked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.taskSummary(tt.tasks)
			if got != tt.want {
				t.Errorf("taskSummary(%v) = %q, want %q", tt.tasks, got, tt.want)
			}
		})
	}
}
