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
