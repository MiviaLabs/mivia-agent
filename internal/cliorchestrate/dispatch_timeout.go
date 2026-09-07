package cliorchestrate

import (
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// DispatchOrchestrationSlackSec is the headroom the whole-call budget gets
// over the longest task in the batch, so the call outlives the work it is
// waiting on.
const DispatchOrchestrationSlackSec = 15

// TimeoutHint is the model-facing guidance for timeout_seconds on the
// orchestration tools (dispatch_tasks, spawn_agent, delegate). It names the
// effective default so agents omit the parameter (or pass 0) instead of
// guessing a small budget, and states that an explicit value IS the budget.
func TimeoutHint() string {
	return fmt.Sprintf("Omit or pass 0 to use the configured default (%.0fh). An explicit positive value is the actual budget (not floored to the default).",
		float64(config.DefaultOrchestrationTimeoutSec)/3600)
}

// DispatchOrchestrationSec picks the wall-clock budget for the whole
// dispatch_tasks invocation from config, batch timeout_seconds, and any
// per-task timeout_seconds (max wins). Always positive.
//
// An explicit batch-level timeout_seconds is honored as the actual budget —
// it is not floored to the 12h default. Per-task timeout_seconds values can
// still raise it: a task that legitimately needs more than the batch budget
// extends the whole-call budget to accommodate it.
//
// This does NOT scale for dispatch waves (a batch bigger than the pool's
// worker capacity, which the coordinator's DAG runs across multiple
// sequential pool.Run calls); see DispatchOrchestrationSecForWorkers for
// that. Kept unscaled for existing callers that have no MaxWorkers to
// reason about.
func DispatchOrchestrationSec(defaultTimeout int, args json.RawMessage) int {
	base, _ := dispatchOrchestrationBase(defaultTimeout, args)
	return base + DispatchOrchestrationSlackSec
}

// DispatchOrchestrationSecForWorkers is DispatchOrchestrationSec, scaled for
// the number of sequential dispatch waves a batch this size needs against a
// pool capped at maxWorkers concurrent tasks.
//
// Without this scaling, a batch with more tasks than the pool's worker
// capacity got the SAME whole-call budget as a batch that fits in one wave:
// the agent loop's own tool-call deadline (armed from this Capability
// timeout) then fired partway through the second or third wave, killing
// every task that had not yet gotten a worker - the coordinator's DAG loop
// (internal/coordinator/dag.go) genuinely needs one task-budget's worth of
// wall clock PER WAVE, not once total. maxWorkers follows
// config.SubagentConfig.MaxWorkers' own contract: 0 means the pool's
// DefaultWorkers (subagents.go), subagents.Unlimited (-1) means no cap (one
// wave, since the pool sizes itself to the batch).
//
// The per-wave budget (not the fixed slack) is what scales: slack is
// headroom over the LAST wave's task budget, added once, not multiplied by
// wave count.
func DispatchOrchestrationSecForWorkers(defaultTimeout, maxWorkers int, args json.RawMessage) int {
	base, taskCount := dispatchOrchestrationBase(defaultTimeout, args)
	waves := dispatchWaveCount(maxWorkers, taskCount)
	scaled := base
	if waves > 1 {
		if base > config.MaxTimeoutSeconds/waves {
			// Overflow guard: base*waves would exceed the ceiling (or wrap
			// a machine int) before the +slack below even runs. Clamp to
			// the ceiling directly rather than let the multiplication
			// produce a nonsense (possibly negative) intermediate value.
			scaled = config.MaxTimeoutSeconds
		} else {
			scaled = base * waves
		}
	}
	total := scaled + DispatchOrchestrationSlackSec
	if total > config.MaxTimeoutSeconds || total < scaled {
		total = config.MaxTimeoutSeconds
	}
	return total
}

// dispatchOrchestrationBase computes the single-wave budget (config
// default, batch timeout_seconds, and any raising per-task overrides) and
// the task count, shared by DispatchOrchestrationSec and
// DispatchOrchestrationSecForWorkers so both apply IDENTICAL base-budget
// resolution and only diverge on wave scaling.
func dispatchOrchestrationBase(defaultTimeout int, args json.RawMessage) (base, taskCount int) {
	var params struct {
		TimeoutSeconds int `json:"timeout_seconds"`
		Tasks          []struct {
			TimeoutSeconds int `json:"timeout_seconds"`
		} `json:"tasks"`
	}
	_ = json.Unmarshal(args, &params)
	overrides := make([]int, 0, len(params.Tasks))
	for _, task := range params.Tasks {
		overrides = append(overrides, task.TimeoutSeconds)
	}
	// Headroom over the longest single task. Without it the whole-call budget and
	// each task's own budget are the same number, and the agent loop arms the
	// call's clock before the pool arms the task's - so the outer deadline always
	// fired first, Join returned ctx.Err() with no result, and a batch reported a
	// bare error instead of the per-task results it was about to produce.
	return config.RequestedTimeoutSec(defaultTimeout, params.TimeoutSeconds, overrides...), len(params.Tasks)
}

// dispatchWaveCount reports how many sequential pool.Run waves a batch of
// taskCount tasks needs against a pool capped at maxWorkers concurrent
// tasks - mirroring subagents.Pool's own New() defaulting contract exactly,
// so this always agrees with the pool the coordinator actually built:
//
//   - maxWorkers == 0 (unset in config) resolves to subagents.DefaultWorkers
//     (New()'s own "zero must not mean unlimited" default).
//   - maxWorkers == subagents.Unlimited (-1) means no cap: the pool sizes
//     itself to the batch (subagents.go's execute), so everything runs in
//     one wave.
//   - taskCount <= 0 is one (degenerate; never dispatches a wave at all,
//     but a caller must still get a positive result).
func dispatchWaveCount(maxWorkers, taskCount int) int {
	if taskCount <= 0 {
		return 1
	}
	workers := maxWorkers
	if workers == 0 {
		workers = subagents.DefaultWorkers
	}
	if workers == subagents.Unlimited || workers <= 0 || workers >= taskCount {
		return 1
	}
	waves := taskCount / workers
	if taskCount%workers != 0 {
		waves++
	}
	return waves
}
