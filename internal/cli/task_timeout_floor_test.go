package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
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

// TestSpawnAgentCapabilityHonorsAllExplicitTimeouts is the regression test for
// spawn_agent's whole-call budget. Before the fix, Capability pre-seeded
// maxBudget with EffectiveTimeoutSec (the 12h default), so even when every task
// had an explicit timeout_seconds:5, the call ran with a 12h budget — the same
// root cause as dispatch_tasks. Now the call budget is the max resolved task
// timeout, not floored to the default.
func TestSpawnAgentCapabilityHonorsAllExplicitTimeouts(t *testing.T) {
	tool := &spawnAgentTool{
		cfg:      config.DefaultSubagentConfig, // DefaultTimeout 0 → 12h default
		agentReg: testAgentRegistry(t, "worker"),
	}
	// All tasks explicit and small: call budget must be 5+slack, NOT 43200+slack.
	cap := tool.Capability(json.RawMessage(`{"tasks":[{"id":"a","timeout_seconds":5},{"id":"b","timeout_seconds":3}]}`))
	want := (5 + dispatchOrchestrationSlackSec) * time.Second
	if cap.Timeout != want {
		t.Fatalf("all-explicit-small spawn capability=%s, want %s (was floored to 12h=%s before the fix)",
			cap.Timeout, want, (config.DefaultOrchestrationTimeoutSec+dispatchOrchestrationSlackSec)*time.Second)
	}
	// Mix of explicit and default: call budget must include the default.
	capMix := tool.Capability(json.RawMessage(`{"tasks":[{"id":"a","timeout_seconds":5},{"id":"b"}]}`))
	wantMix := (config.DefaultOrchestrationTimeoutSec + dispatchOrchestrationSlackSec) * time.Second
	if capMix.Timeout != wantMix {
		t.Fatalf("mixed spawn capability=%s, want %s (task b has no explicit timeout → default raises the call budget)", capMix.Timeout, wantMix)
	}
	// No tasks: falls back to default.
	capEmpty := tool.Capability(json.RawMessage(`{}`))
	if capEmpty.Timeout != wantMix {
		t.Fatalf("empty spawn capability=%s, want %s (no tasks → default)", capEmpty.Timeout, wantMix)
	}
}

// TestTaskTimeoutFloorSpawnPerTaskExplicit pins the new contract for
// spawn_agent's per-task timeout_seconds: an explicit positive value IS the
// task's budget (not floored to the configured default), 0 falls back to the
// configured default, and a huge value clamps to MaxTimeoutSeconds (R2B-1).
func TestTaskTimeoutFloorSpawnPerTaskExplicit(t *testing.T) {
	tool := &spawnAgentTool{
		cfg:      config.SubagentConfig{DefaultTimeout: 600},
		agentReg: testAgentRegistry(t, "worker"),
	}
	tasks, err := tool.buildSpawnTasks([]spawnTaskParams{
		{ID: "short", Agent: "worker", Prompt: "x", TimeoutSeconds: 5},
		{ID: "raised", Agent: "worker", Prompt: "y", TimeoutSeconds: 900},
		{ID: "plain", Agent: "worker", Prompt: "z"},
		// Same overflow-safety as dispatch_tasks (R2B-1): a value that would
		// wrap time.Duration negative must clamp to MaxTimeoutSeconds.
		{ID: "huge", Agent: "worker", Prompt: "w", TimeoutSeconds: 10_000_000_000},
	}, runtime.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Duration{
		"short":  5 * time.Second,   // explicit value honored directly
		"raised": 900 * time.Second, // explicit value honored directly
		"plain":  600 * time.Second, // 0 → configured default
		"huge":   time.Duration(config.MaxTimeoutSeconds) * time.Second,
	}
	for _, task := range tasks {
		if got := task.Timeout; got != want[task.ID] {
			t.Fatalf("spawn task %q timeout=%s, want %s", task.ID, got, want[task.ID])
		}
		if task.Timeout <= 0 {
			t.Fatalf("spawn task %q timeout=%s must be a positive duration, never a wrapped-negative one", task.ID, task.Timeout)
		}
	}
}
