package cli

import (
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// dispatchOrchestrationSlackSec is the headroom the whole-call budget gets over
// the longest task in the batch, so the call outlives the work it is waiting on.
const dispatchOrchestrationSlackSec = 15

// timeoutHint is the model-facing guidance for timeout_seconds on the
// orchestration tools (dispatch_tasks, spawn_agent, delegate). It names the
// effective default so agents omit the parameter (or pass 0) instead of
// guessing a small budget, and states that an explicit value IS the budget.
func timeoutHint() string {
	return fmt.Sprintf("Omit or pass 0 to use the configured default (%.0fh). An explicit positive value is the actual budget (not floored to the default).",
		float64(config.DefaultOrchestrationTimeoutSec)/3600)
}

// dispatchOrchestrationSec picks the wall-clock budget for the whole
// dispatch_tasks invocation from config, batch timeout_seconds, and any
// per-task timeout_seconds (max wins). Always positive.
//
// An explicit batch-level timeout_seconds is honored as the actual budget —
// it is not floored to the 12h default. Per-task timeout_seconds values can
// still raise it: a task that legitimately needs more than the batch budget
// extends the whole-call budget to accommodate it.
func dispatchOrchestrationSec(defaultTimeout int, args json.RawMessage) int {
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
	return config.RequestedTimeoutSec(defaultTimeout, params.TimeoutSeconds, overrides...) + dispatchOrchestrationSlackSec
}
