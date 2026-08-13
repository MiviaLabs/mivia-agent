package agent

import (
	"context"
	"time"
)

// Soft conclude thresholds: when a work bound is this close, the loop tells
// the model to wrap up (graceful degradation) instead of letting the bound
// hard-abort the run mid-work with no result. The injected instruction is
// host-generated, bounded, and ephemeral — it is never appended to
// l.Messages — so it costs one message per request while the bound stays close
// and cannot leak into history, checkpoints, or replays.
const (
	// concludeTimeThreshold is how much wall clock may remain before the loop
	// tells the model to conclude. The deadline is enforced by ctx, set from
	// WorkLimits.DeadlineAt or a parent context; concluding before it turns a
	// hard abort into a completed result.
	concludeTimeThreshold = 5 * time.Minute
	// concludeToolCallsLeft is how few tool-call reservations may remain
	// before the loop tells the model to conclude.
	concludeToolCallsLeft = 4
)

// concludeMessage is the host instruction injected when a work bound is close.
// It deliberately does not describe the bound (the model cannot act on meter
// internals): it only asks for the best final answer in the required format.
const concludeMessage = "Work-limit notice: you are close to your deadline or step budget. Do not start new work. Finish the current task now and return your best final answer in the required output format, even if some parts are incomplete."

// concludeInstruction returns the conclude message when the loop is
// approaching a bound it would otherwise hard-abort on, or "" when no bound is
// close. Checks, in order: the wall-clock deadline on ctx; the cumulative
// output budget (fewer than one full per-call allowance left, when both caps
// are set); and the tool-call budget (fewer than concludeToolCallsLeft
// reservations left). A model that heeds it returns its best valid result; a
// model that ignores it still hits the hard bound exactly as today. The
// instruction repeats per request only while the bound stays close.
func (l *Loop) concludeInstruction(ctx context.Context) string {
	if l == nil || l.workLimits == nil {
		return ""
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < concludeTimeThreshold {
			return concludeMessage
		}
	}
	limits := l.workLimits.limits
	if limits.MaxOutputTokens > 0 && limits.MaxOutputPerCall > 0 {
		if remaining := limits.MaxOutputTokens - l.workLimits.outputTokens; remaining < limits.MaxOutputPerCall {
			return concludeMessage
		}
	}
	if limits.MaxToolCalls > 0 {
		if remaining := limits.MaxToolCalls - l.workLimits.toolCalls; remaining < concludeToolCallsLeft {
			return concludeMessage
		}
	}
	return ""
}
