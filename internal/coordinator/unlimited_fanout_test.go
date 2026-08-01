package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// instantHandler is a test handler that returns immediately with a fixed payload.
type instantHandler struct{}

func (instantHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(`{"done":true}`), nil
}

// TestCoordinatorUnlimitedFanoutAcceptsTasks verifies that a Pool configured
// with MaxFanout=0 (zero means unlimited) accepts far more tasks than any
// finite limit. This guards the two guard clauses that skip the fan-out check
// when the configured bound is ≤ 0:
//
//   - coordinator.validateTasks:  c.pool.MaxFanout() > 0 && ...
//   - subagents.Pool.validate:   p.p.MaxFanout > 0 && ...
func TestCoordinatorUnlimitedFanoutAcceptsTasks(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "instant", &instantHandler{}); err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewMemoryLedgerRepository()
	pool := subagents.New(d, subagents.Policy{Workers: 1, MaxFanout: 0})
	c := New(repo, pool)

	// 20 tasks - well above any previous default limit.
	tasks := make([]subagents.Task, 20)
	for i := range tasks {
		tasks[i] = subagents.Task{
			ID:   fmt.Sprintf("t%d", i),
			Name: "instant",
		}
	}

	h, err := c.Spawn(context.Background(), tasks, "")
	if err != nil {
		t.Fatalf("Spawn with 20 tasks and MaxFanout=0 should succeed, got: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatalf("Join failed: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("run completed with error: %v", result.Err)
	}
	if len(result.Results) != 20 {
		t.Fatalf("expected 20 task results, got %d", len(result.Results))
	}

	for _, r := range result.Results {
		if r.Status != "completed" {
			t.Fatalf("task %s status = %q, want completed", r.TaskID, r.Status)
		}
	}
}
