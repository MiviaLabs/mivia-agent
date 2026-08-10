package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// perCallTimeoutTool does fixed work and gives up early only when its context
// is canceled. Its Capability advertises a SHORTER default timeout than the
// work, so a successful run proves a model-supplied timeout_seconds override
// reached the loop's per-call budget.
type perCallTimeoutTool struct {
	name string
	cap  time.Duration
	work time.Duration
}

func (t *perCallTimeoutTool) Name() string               { return t.name }
func (t *perCallTimeoutTool) Description() string        { return "per-call timeout test tool" }
func (t *perCallTimeoutTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *perCallTimeoutTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, Timeout: t.cap, ResourceKey: "path:" + t.name}
}
func (t *perCallTimeoutTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	select {
	case <-time.After(t.work):
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestPrepareToolTasks_PerCallTimeoutSecondsOverridesCapability pins the budget
// site: a model-supplied timeout_seconds ABOVE the tool's capability default
// must raise the per-call budget (clamped to the enclosing ctx deadline), while
// a call without the param keeps the capability default.
func TestPrepareToolTasks_PerCallTimeoutSecondsOverridesCapability(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&perCallTimeoutTool{name: "slow", cap: 2 * time.Second, work: time.Millisecond})
	reg.Register(&perCallTimeoutTool{name: "plain", cap: 2 * time.Second, work: time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tasks := prepareToolTasks(ctx, []provider.ToolCall{
		tc("1", "slow", `{"timeout_seconds":5}`),
		tc("2", "plain", `{}`),
	}, reg, 60*time.Second, 0)
	defer func() {
		for _, task := range tasks {
			task.cancel()
		}
	}()

	if tasks[0].timeout != 5*time.Second {
		t.Fatalf("timeout_seconds=5 vs capability 2s: budget=%s, want 5s", tasks[0].timeout)
	}
	if tasks[1].timeout != 2*time.Second {
		t.Fatalf("call without timeout_seconds must keep the capability default: budget=%s, want 2s", tasks[1].timeout)
	}
}

// TestPrepareToolTasks_PerCallTimeoutClampedToTaskDeadline: a huge per-call
// request must never extend past the enclosing step/task deadline, and must
// stay a positive duration (no int64 wrap on the way in).
func TestPrepareToolTasks_PerCallTimeoutClampedToTaskDeadline(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&perCallTimeoutTool{name: "slow", cap: 10 * time.Second, work: time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tasks := prepareToolTasks(ctx, []provider.ToolCall{
		tc("1", "slow", `{"timeout_seconds":300}`),
	}, reg, 60*time.Second, 0)
	defer func() {
		for _, task := range tasks {
			task.cancel()
		}
	}()

	if tasks[0].timeout <= 0 || tasks[0].timeout > time.Second {
		t.Fatalf("budget=%s, want clamped to the 1s task deadline", tasks[0].timeout)
	}
}

// TestPrepareToolTasks_PerCallTimeoutBelowCapabilityKeepsCapability pins the
// raise-only rule: a model-supplied timeout_seconds BELOW the tool's own
// capability budget must not tighten the loop's per-call budget. Tools that
// declare a long budget (dispatch_tasks, run_command) contract their own hang
// bound; a small guessed value would kill long multi-step work the tool
// promised to support.
func TestPrepareToolTasks_PerCallTimeoutBelowCapabilityKeepsCapability(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&perCallTimeoutTool{name: "slow", cap: 2 * time.Second, work: time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tasks := prepareToolTasks(ctx, []provider.ToolCall{
		tc("1", "slow", `{"timeout_seconds":1}`),
	}, reg, 60*time.Second, 0)
	defer func() {
		for _, task := range tasks {
			task.cancel()
		}
	}()

	if tasks[0].timeout != 2*time.Second {
		t.Fatalf("timeout_seconds=1 vs capability 2s: budget=%s, want the capability 2s (raise-only)", tasks[0].timeout)
	}
}

// TestExecuteToolsParallel_PerCallTimeoutBelowCapabilityDoesNotKillWork drives
// real execution through the loop budget site: capability 3s, work 1.5s,
// explicit timeout_seconds=1. The call must COMPLETE; a tightening budget
// would kill it mid-work at 1s.
func TestExecuteToolsParallel_PerCallTimeoutBelowCapabilityDoesNotKillWork(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&perCallTimeoutTool{name: "slow", cap: 3 * time.Second, work: 1500 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := executeToolsParallel(ctx, []provider.ToolCall{
		tc("1", "slow", `{"timeout_seconds":1}`),
	}, reg, Options{ToolTimeout: 60 * time.Second, MaxConcurrentTools: 1})

	if len(results) != 1 {
		t.Fatalf("results=%d, want 1", len(results))
	}
	if results[0].err != nil {
		t.Fatalf("timeout_seconds=1 tightened below the 3s capability and killed the work: %v result=%q", results[0].err, results[0].result)
	}
	if !strings.Contains(results[0].result, "done") {
		t.Fatalf("result=%q, want completed work", results[0].result)
	}
}

// TestExecuteToolsParallel_PerCallTimeoutSecondsBeatsCapability drives real
// execution through the loop budget site: capability 2s, work 3s, explicit
// timeout_seconds=5, 30s task deadline. The call must COMPLETE; without the
// override the loop's 2s budget would kill it mid-work.
func TestExecuteToolsParallel_PerCallTimeoutSecondsBeatsCapability(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&perCallTimeoutTool{name: "slow", cap: 2 * time.Second, work: 3 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := executeToolsParallel(ctx, []provider.ToolCall{
		tc("1", "slow", `{"timeout_seconds":5}`),
	}, reg, Options{ToolTimeout: 60 * time.Second, MaxConcurrentTools: 1})

	if len(results) != 1 {
		t.Fatalf("results=%d, want 1", len(results))
	}
	if results[0].err != nil {
		t.Fatalf("timeout_seconds=5 killed by the 2s capability budget: %v result=%q", results[0].err, results[0].result)
	}
	if !strings.Contains(results[0].result, "done") {
		t.Fatalf("result=%q, want completed work", results[0].result)
	}
}
