package cliorchestrate

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// TestDispatchWaveCount pins dispatchWaveCount's mapping from a batch size
// and the configured worker cap onto the number of sequential dispatch
// waves the coordinator's DAG will actually run - mirroring
// subagents.Pool's own zero/unlimited semantics (subagents.go New).
func TestDispatchWaveCount(t *testing.T) {
	cases := []struct {
		name       string
		maxWorkers int
		taskCount  int
		want       int
	}{
		{"zero tasks", 3, 0, 1},
		{"single wave under capacity", 3, 2, 1},
		{"exact capacity one wave", 3, 3, 1},
		{"two waves", 3, 4, 2},
		{"two waves exact multiple", 3, 6, 2}, // 6/3 = exactly 2
		{"six tasks two workers", 2, 6, 3},
		{"config zero defaults to DefaultWorkers", 0, 6, 2}, // 6 / DefaultWorkers(3)
		{"unlimited sentinel is one wave", -1, 50, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchWaveCount(tc.maxWorkers, tc.taskCount); got != tc.want {
				t.Fatalf("dispatchWaveCount(%d, %d) = %d, want %d", tc.maxWorkers, tc.taskCount, got, tc.want)
			}
		})
	}
}

// dispatchWaveTestTool builds a dispatchTasksTool with the given MaxWorkers,
// for Capability() wave-scaling assertions.
func dispatchWaveTestTool(t *testing.T, maxWorkers int) *dispatchTasksTool {
	t.Helper()
	return &dispatchTasksTool{
		cfg:      config.SubagentConfig{DefaultTimeout: 100, MaxWorkers: maxWorkers},
		repo:     ledger.NewMemoryLedgerRepository(),
		agentReg: testAgentRegistry(t, "worker"),
	}
}

// sixTaskArgs builds a 6-task dispatch_tasks call body naming no explicit
// timeout_seconds, so the batch budget resolves purely from cfg.DefaultTimeout.
func sixTaskArgs() json.RawMessage {
	tasks := make([]map[string]string, 6)
	for i := range tasks {
		tasks[i] = map[string]string{"id": "t", "agent": "worker", "prompt": "x"}
	}
	raw, _ := json.Marshal(map[string]any{"tasks": tasks})
	return raw
}

// TestDispatchTasksCapabilityTimeoutScalesWithWaves is the regression: a
// batch that needs 3 sequential dispatch waves against a 2-worker pool must
// get a Capability().Timeout roughly 3x a single task's budget, not the
// single-wave budget the pre-fix code handed every batch regardless of its
// size relative to worker capacity. Without this, the agent-loop's own
// tool-call deadline (armed from Capability().Timeout) fires before later
// waves ever get a worker, killing the whole batch mid-flight and losing
// every task that had not yet been dispatched.
func TestDispatchTasksCapabilityTimeoutScalesWithWaves(t *testing.T) {
	single := dispatchWaveTestTool(t, 6) // capacity >= task count: one wave
	multi := dispatchWaveTestTool(t, 2)  // 6 tasks / 2 workers = 3 waves

	args := sixTaskArgs()
	singleTimeout := single.Capability(args).Timeout
	multiTimeout := multi.Capability(args).Timeout

	if multiTimeout <= singleTimeout {
		t.Fatalf("3-wave Capability timeout (%s) must exceed the 1-wave timeout (%s); "+
			"a batch bigger than worker capacity needs proportionally more wall clock",
			multiTimeout, singleTimeout)
	}
	// Expect roughly 3x the single-wave work budget (100s default, minus
	// the fixed slack which is not re-multiplied) plus one slack margin.
	perWave := 100 * time.Second
	slack := time.Duration(DispatchOrchestrationSlackSec) * time.Second
	wantApprox := perWave*3 + slack
	if diff := multiTimeout - wantApprox; diff < -5*time.Second || diff > 5*time.Second {
		t.Fatalf("3-wave Capability timeout = %s, want approximately %s (3 waves x %s + %s slack)",
			multiTimeout, wantApprox, perWave, slack)
	}
}

// TestDispatchTasksCapabilityTimeoutUnaffectedBySingleWaveBatch proves the
// fix does not regress the common case: a batch that fits inside one wave
// keeps exactly today's budget (base + slack), not an inflated one.
func TestDispatchTasksCapabilityTimeoutUnaffectedBySingleWaveBatch(t *testing.T) {
	tool := dispatchWaveTestTool(t, 3)
	args := json.RawMessage(`{"tasks":[{"id":"a","agent":"worker","prompt":"x"},{"id":"b","agent":"worker","prompt":"y"}]}`)
	got := tool.Capability(args).Timeout
	want := time.Duration(100+DispatchOrchestrationSlackSec) * time.Second
	if got != want {
		t.Fatalf("single-wave Capability timeout = %s, want unchanged %s", got, want)
	}
}

// TestDispatchTasksCapabilityTimeoutClampsOverflow pins the overflow guard
// on the wave-scaled path: a huge task count against a tiny worker cap must
// still clamp to MaxTimeoutSeconds instead of overflowing time.Duration.
func TestDispatchTasksCapabilityTimeoutClampsOverflow(t *testing.T) {
	tool := dispatchWaveTestTool(t, 1)
	tasks := make([]map[string]string, 16)
	for i := range tasks {
		tasks[i] = map[string]string{"id": "t", "agent": "worker", "prompt": "x", "timeout_seconds": "100000000"}
	}
	raw, _ := json.Marshal(map[string]any{"tasks": tasks, "timeout_seconds": 100000000})
	got := tool.Capability(raw).Timeout
	if got <= 0 {
		t.Fatalf("Capability timeout must stay positive after overflow clamp, got %s", got)
	}
	if got > time.Duration(config.MaxTimeoutSeconds)*time.Second {
		t.Fatalf("Capability timeout %s exceeds MaxTimeoutSeconds ceiling", got)
	}
}
