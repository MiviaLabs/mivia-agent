package clichat

import (
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// totalTaskTimeout resolves the whole-subagent wall-clock budget from the
// [subagents] default_total_timeout_seconds knob. Companion to requestTimeout
// (agent_task_handler.go), which resolves the per-request knob the same way.
// A positive configured value is the budget. Unset (0) applies
// DefaultSubagentTotalTimeoutSec (3600s, 60 minutes): a provider connection
// that trickles bytes defeats every idle watchdog, so each handler
// construction site carries this total bound as the last-resort termination
// guarantee. A negative value switches the bound off (returns 0); that is an
// explicit operator opt-out, and a direct spawn with no per-task timeout then
// has no handler-level bound.
func totalTaskTimeout(configured int) time.Duration {
	switch {
	case configured < 0:
		return 0
	case configured == 0:
		return config.DefaultSubagentTotalTimeoutSec * time.Second
	default:
		return config.SaturatingSeconds(configured)
	}
}

// unboundedStepTimeout stands in for "no ceiling" where a consumer cannot
// express one. internal/automation's executor treats a non-positive
// TurnTimeout as "apply my own 10-minute fallback", so handing it the 0
// that [subagents] default_total_timeout_seconds uses for OFF gave the
// operator who disabled the bound the TIGHTEST one available. A century is
// unbounded for every practical purpose and stays far inside the
// saturation ceiling.
const unboundedStepTimeout = 100 * 365 * 24 * time.Hour

// StepTimeout resolves a whole-agent-run budget for a consumer that reads
// a non-positive duration as "unset" rather than as "unbounded".
//
// It is TotalTaskTimeout with the sentinel translated: a configured
// negative value means the operator opted out of the ceiling
// (subagents_types.go), and that must reach the consumer as an enormous
// bound, never as zero.
func StepTimeout(configured int) time.Duration {
	if d := totalTaskTimeout(configured); d > 0 {
		return d
	}
	return unboundedStepTimeout
}

// TotalTaskTimeout is the exported view of totalTaskTimeout, for hosts
// outside this package that bound a whole agent run rather than one
// provider request.
//
// internal/cliautomations and the TUI's automation backend use it for
// automation.Config.TurnTimeout: an automation STEP is a whole agent run -
// every request, tool call, delegated subagent and wait added together -
// which is exactly what [subagents] default_total_timeout_seconds bounds.
// Left unset, the executor falls back to a hardcoded 10 minutes, which no
// operator knob could reach.
func TotalTaskTimeout(configured int) time.Duration {
	return totalTaskTimeout(configured)
}
