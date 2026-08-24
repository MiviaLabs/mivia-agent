package agent

// SDK-path WorkLimits wiring, tool-call budget: bridges the SDK
// agentloop's ToolBudget hook onto the SAME workLimitMeter the legacy
// loop uses (work_limits.go) and newSDKWorkBudget already resets for
// this turn (agentloop_budget.go). No policy is forked here:
// reserveToolBatch is the legacy method; this file only supplies the
// SDK's call point.

import (
	"context"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
)

// newSDKToolBudget bridges l's workLimitMeter onto the SDK's
// ToolBudget hook. It must run after newSDKWorkBudget (or
// newSDKWorkBudgetHook) has established l.workLimits for this turn, so
// both hooks share one meter instance and one set of cumulative
// counters.
//
// The SDK calls Reserve once per turn with the RAW resp.ToolCalls
// count - before per-call malformed-argument filtering or in-turn
// dedup, both of which happen later inside the SDK's own
// runToolCalls. This is a deliberate, documented approximation of the
// legacy loop's reserveToolBatch call (loop_tools.go's
// processToolCalls charged only the validated, batch-cap-clamped
// count via executedToolCallCount): it can only exhaust
// WorkLimits.MaxToolCalls SOONER than an exact accounting would,
// never later, so a caller relying on the cumulative cap as a safety
// valve is never under-protected by the difference.
func newSDKToolBudget(l *Loop) *sdkagentloop.ToolBudget {
	return &sdkagentloop.ToolBudget{
		Reserve: func(ctx context.Context, calls int) error {
			return l.workLimits.reserveToolBatch(calls)
		},
	}
}
