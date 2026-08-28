package controller

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// childRunRecorder collects RegisterChildRun hook calls under a mutex: the
// runner invokes the hook from dispatch, and tests read it after RunStep
// returns.
type childRunRecorder struct {
	mu      sync.Mutex
	runIDs  []string
	handles []*coordinator.RunHandle
}

func (r *childRunRecorder) hook(_ context.Context, runID string, handle *coordinator.RunHandle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runIDs = append(r.runIDs, runID)
	r.handles = append(r.handles, handle)
}

func (r *childRunRecorder) calls() (int, string, *coordinator.RunHandle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.runIDs) == 0 {
		return 0, "", nil
	}
	return len(r.runIDs), r.runIDs[0], r.handles[0]
}

// TestCoordinatorRunnerHooksValidatedChildRun pins the invoke-site contract:
// the runner calls RegisterChildRun exactly once per ensured run, after the
// returned handle's run ID passed the identity check, with the run ID and the
// live handle. This is the one seam the host uses to make workflow child runs
// inspectable and cancelable through the standard tools.
func TestCoordinatorRunnerHooksValidatedChildRun(t *testing.T) {
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)})
	rec := &childRunRecorder{}
	runner.RegisterChildRun = rec.hook
	spec := validStepRequest()

	if _, err := runner.RunStep(context.Background(), spec); err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	n, runID, handle := rec.calls()
	if n != 1 {
		t.Fatalf("hook calls = %d, want 1", n)
	}
	if runID != spec.CoordinatorRunID {
		t.Fatalf("hook run ID = %q, want %q", runID, spec.CoordinatorRunID)
	}
	if handle == nil || handle.RunID() != spec.CoordinatorRunID {
		t.Fatalf("hook handle = %v, want the live handle for %q", handle, spec.CoordinatorRunID)
	}
}

// TestCoordinatorRunnerSkipsHookOnRunIDMismatch pins the guard placement: a
// coordinator that returns a foreign run ID fails the step BEFORE the hook
// fires, so a mismatched run can never reach the registry under this step's
// identity.
func TestCoordinatorRunnerSkipsHookOnRunIDMismatch(t *testing.T) {
	base := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)}).Coordinator
	runner := NewCoordinatorRunner(&inspectingCoordinator{Coordinator: base, rewriteRunID: true})
	rec := &childRunRecorder{}
	runner.RegisterChildRun = rec.hook
	spec := validStepRequest()

	result, err := runner.RunStep(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "coordinator returned run") {
		t.Fatalf("result = %+v, error = %v, want the run-identity error", result, err)
	}
	if n, _, _ := rec.calls(); n != 0 {
		t.Fatalf("hook calls = %d, want 0 on a run-ID mismatch", n)
	}
}

// TestCoordinatorRunnerNilHookIsSafe pins nil-safety: a runner without a host
// hook runs steps unchanged (every non-workflow caller's shape).
func TestCoordinatorRunnerNilHookIsSafe(t *testing.T) {
	runner := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)})
	if runner.RegisterChildRun != nil {
		t.Fatal("RegisterChildRun = non-nil by default, want nil")
	}
	got, err := runner.RunStep(context.Background(), validStepRequest())
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if got.CoordinatorRunID == "" {
		t.Fatalf("result = %+v, want a resolved coordinator run ID", got)
	}
}
