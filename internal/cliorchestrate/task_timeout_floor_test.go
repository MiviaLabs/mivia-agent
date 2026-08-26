package cliorchestrate

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestDispatchTasksPerTaskTimeoutRaiseOnly pins the raise-only contract for
// per-task timeout_seconds in dispatch_tasks: a task may extend the batch
// budget, never shrink it. The batch budget itself is already floored by
// EffectiveTimeoutSec at the configured default (or the 12h safety ceiling),
// so a short per-task override must resolve to the batch budget, not to its
// own smaller number.
func TestTaskTimeoutFloorDispatchPerTaskRaiseOnly(t *testing.T) {
	tool := &dispatchTasksTool{
		cfg:      config.SubagentConfig{DefaultTimeout: 600},
		agentReg: testAgentRegistry(t, "worker"),
	}
	batchTimeout := config.EffectiveTimeoutSec(tool.cfg.DefaultTimeout, 0) // 600
	tasks, err := tool.buildTasks([]dispatchTaskParam{
		{ID: "short", Agent: "worker", Prompt: "x", TimeoutSeconds: 5},
		{ID: "raised", Agent: "worker", Prompt: "y", TimeoutSeconds: 900},
		{ID: "plain", Agent: "worker", Prompt: "z"},
		// Overflow-safety: a model-supplied value that would wrap time.Duration
		// negative must be clamped to MaxTimeoutSeconds instead of becoming a
		// negative duration that subagents.go treats as "no timeout" and gets
		// killed at the operator default (R2B-1).
		{ID: "huge", Agent: "worker", Prompt: "w", TimeoutSeconds: 10_000_000_000},
	}, batchTimeout)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Duration{
		"short":  600 * time.Second, // floored up to the batch budget
		"raised": 900 * time.Second, // larger override still raises
		"plain":  600 * time.Second, // default = batch budget
		"huge":   time.Duration(config.MaxTimeoutSeconds) * time.Second,
	}
	for _, task := range tasks {
		if got := task.Timeout; got != want[task.ID] {
			t.Fatalf("task %q timeout=%s, want %s (raise-only vs batch budget %ds)", task.ID, got, want[task.ID], batchTimeout)
		}
		if task.Timeout <= 0 {
			t.Fatalf("task %q timeout=%s must be a positive duration, never a wrapped-negative one", task.ID, task.Timeout)
		}
	}
}

// TestTaskTimeoutFloorDispatchWithoutConfig pins the 12h safety floor
// when no default is configured: a short override must not shrink a task below
// DefaultOrchestrationTimeoutSec.
func TestTaskTimeoutFloorDispatchWithoutConfig(t *testing.T) {
	tool := &dispatchTasksTool{
		cfg:      config.DefaultSubagentConfig, // DefaultTimeout 0 → 12h floor
		agentReg: testAgentRegistry(t, "worker"),
	}
	batchTimeout := config.EffectiveTimeoutSec(tool.cfg.DefaultTimeout, 0)
	tasks, err := tool.buildTasks([]dispatchTaskParam{
		{ID: "tiny", Agent: "worker", Prompt: "x", TimeoutSeconds: 5},
	}, batchTimeout)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Duration(config.DefaultOrchestrationTimeoutSec) * time.Second
	if got := tasks[0].Timeout; got != want {
		t.Fatalf("task timeout=%s, want the %s safety floor", got, want)
	}
}
