package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestJoinWatchdogEmitsStepHeartbeat pins the join progress emit: a join that
// stays alive past one watchdog tick must emit at least one
// ProgressStepHeartbeat carrying the step identity. The heartbeat makes a
// still-running join observable at the documented step_heartbeat cadence.
func TestJoinWatchdogEmitsStepHeartbeat(t *testing.T) {
	ResetStepHeartbeats()
	defer ResetStepHeartbeats()

	const taskID = "task-heartbeat-emit"
	// A short bound keeps the tick interval at the 100 ms floor, so the first
	// tick lands quickly without any test-only tick override.
	const watchdog = 600 * time.Millisecond

	coord, h, spec, stopHeartbeats := newJoinWatchdogHarness(t, taskID, "workflow-step/heartbeat-emit", "wfr-heartbeat-emit", 2, 0)

	var mu sync.Mutex
	var emitted []ProgressEvent
	runner := NewCoordinatorRunner(coord)
	runner.JoinWatchdog = watchdog
	done := make(chan error, 1)
	go func() {
		_, joinErr := runner.joinWithCancellation(context.Background(), spec, h, func(e ProgressEvent) {
			mu.Lock()
			emitted = append(emitted, e)
			mu.Unlock()
		})
		done <- joinErr
	}()

	// Wait past one watchdog tick (bound/8 floored at 100 ms): the join is
	// live, so the ticker must have reported at least one step heartbeat.
	// Poll with a ticker: time.After is allowed by the project's test policy,
	// time.Sleep is not.
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		count := len(emitted)
		mu.Unlock()
		if count > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no ProgressStepHeartbeat emitted while the join stayed live; want one per watchdog tick")
		case <-poll.C:
		}
	}
	mu.Lock()
	first := append([]ProgressEvent(nil), emitted...)
	mu.Unlock()
	if first[0].Kind != ProgressStepHeartbeat {
		t.Fatalf("emitted kind = %q, want step_heartbeat", first[0].Kind)
	}
	if first[0].StepID != spec.StepID || first[0].AttemptNo != spec.AttemptNo ||
		first[0].TaskID != spec.TaskID || first[0].CoordinatorRunID != spec.CoordinatorRunID {
		t.Fatalf("heartbeat identity = %+v, want step %s attempt %d task %s run %s",
			first[0], spec.StepID, spec.AttemptNo, spec.TaskID, spec.CoordinatorRunID)
	}
	if first[0].Detail != "running" {
		t.Fatalf("heartbeat detail = %q, want running", first[0].Detail)
	}

	// The child is still live: the join must not be canceled while the
	// heartbeats keep the reference inside the bound.
	select {
	case joinErr := <-done:
		t.Fatalf("join ended while the child was live: %v; want the watchdog to leave a live child alone", joinErr)
	case <-time.After(300 * time.Millisecond):
	}

	// The child goes silent: the watchdog must still cancel the join, proving
	// the emit did not disturb the liveness gate.
	close(stopHeartbeats)
	select {
	case joinErr := <-done:
		if joinErr == nil || !strings.Contains(joinErr.Error(), "join timed out") {
			t.Fatalf("join error = %v, want it to name the join timeout", joinErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("join did not end after the child went silent; want a watchdog timeout")
	}
}

// newJoinWatchdogHarness wires the coordinator, the run handle, and the step
// spec for a join-watchdog test, and starts a task heartbeat ticker that
// keeps the child live. heartbeatDelay delays the first heartbeat (a positive
// value models a fresh child whose first heartbeat lands after the first
// watchdog tick); a negative value disables the ticker. The returned stop
// channel ends the ticker.
func newJoinWatchdogHarness(t *testing.T, taskID, idempotencyKey, workflowRunID string, attemptNo int, heartbeatDelay time.Duration) (coordinator.Coordinator, *coordinator.RunHandle, AgentStepRequest, chan struct{}) {
	t.Helper()
	d := runtime.New(runtime.Policy{})
	handler := &neverSettlingHandler{}
	if err := d.Register(runtime.Subagent, "dev", handler); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coordRepo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordRepo, p).WithRetryPolicy(coordinator.NoRetry)

	h, err := coord.EnsureRun(context.Background(), coordinator.EnsureRunRequest{
		RunID:                coordinator.NewRunID(),
		Tasks:                []subagents.Task{{ID: taskID, Name: "dev", AgentName: "dev"}},
		IdempotencyKey:       idempotencyKey,
		NonInteractiveParent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := AgentStepRequest{WorkflowRunID: workflowRunID, StepID: "one", AttemptNo: attemptNo,
		TaskID: taskID, CoordinatorRunID: h.RunID(), AgentName: "dev"}

	var stopHeartbeats chan struct{}
	if heartbeatDelay >= 0 {
		stopHeartbeats = make(chan struct{})
		go func() {
			if heartbeatDelay > 0 {
				// Delay the first heartbeat without time.Sleep: time.After in a
				// select is the project-allowed wait pattern.
				select {
				case <-time.After(heartbeatDelay):
				case <-stopHeartbeats:
					return
				}
			}
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopHeartbeats:
					return
				case <-ticker.C:
					NoteStepHeartbeat(taskID)
				}
			}
		}()
	}
	return coord, h, spec, stopHeartbeats
}
