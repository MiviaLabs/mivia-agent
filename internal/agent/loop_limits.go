package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

// DefaultToolTimeout is the agent-loop budget for tools that do not declare
// a Capability.Timeout. Finite so ordinary tools cannot hang the loop.
const DefaultToolTimeout = 60 * time.Second

// resolveToolCallTimeout chooses the wall-clock budget for one tool call.
//
// When capabilityTimeout > 0 it is authoritative: tools that need longer
// (run_command, dispatch_tasks, delegate) may set a higher budget, and tools
// that need a tighter cap may set a lower one. The default only applies when
// the tool does not declare a capability timeout. A non-positive default
// falls back to DefaultToolTimeout so the loop never waits unbounded.
//
// This is the DEFAULT resolution: a model-supplied per-call timeout_seconds in
// the call's own params outranks both (see prepareToolTasks /
// requestedToolTimeout), clamped to the enclosing step/task deadline.
func resolveToolCallTimeout(defaultTimeout, capabilityTimeout time.Duration) time.Duration {
	if capabilityTimeout > 0 {
		return capabilityTimeout
	}
	if defaultTimeout > 0 {
		return defaultTimeout
	}
	return DefaultToolTimeout
}

// requestedToolTimeout extracts a model-supplied per-call timeout_seconds from
// tool call params. It returns 0 when the call did not request one (absent,
// non-JSON, or non-positive), so the capability default applies unchanged.
//
// A seconds value too large for time.Duration is clamped to the largest safe
// duration first: without that, a huge model-supplied timeout_seconds (which
// parses fine and fits int64) wraps negative when multiplied by time.Second and
// would silently disable the per-call budget. The clamp is a conversion guard,
// not a policy cap - the enclosing step/task deadline bounds the real budget.
func requestedToolTimeout(raw json.RawMessage) time.Duration {
	var params struct {
		TimeoutSeconds int64 `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.TimeoutSeconds <= 0 {
		return 0
	}
	if params.TimeoutSeconds > maxDurationSeconds {
		return time.Duration(maxDurationSeconds) * time.Second
	}
	return time.Duration(params.TimeoutSeconds) * time.Second
}

// maxDurationSeconds is the largest whole seconds value that keeps
// time.Duration(secs)*time.Second inside int64 range.
const maxDurationSeconds = int64(^uint64(0)>>1) / int64(time.Second)

// clampToDeadline returns the tighter of budget and the time remaining before
// the parent ctx deadline, so a per-call extension can never outlive the step
// or task that owns it. A ctx without a deadline leaves the budget unchanged:
// the per-call budget itself is the bound then.
func clampToDeadline(ctx context.Context, budget time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return budget
	}
	if remaining := time.Until(deadline); remaining < budget {
		return remaining
	}
	return budget
}

// effectiveResultCap is the tighter of the loop-wide maxChars and the tool's
// own capabilityMaxBytes; 0 means uncapped.
//
// The batch shaper needs this number, not just its effect: a result it re-cuts
// must never come back LARGER than the cap its tool contracted for, however
// much room the batch budget happens to have (F3).
func effectiveResultCap(maxChars, capabilityMaxBytes int) int {
	maxResult := maxChars
	if capabilityMaxBytes > 0 && (maxResult <= 0 || capabilityMaxBytes < maxResult) {
		maxResult = capabilityMaxBytes
	}
	return maxResult
}

// capToolResult applies the tighter of maxChars and capabilityMaxBytes.
// When spool is non-nil and the body is truncated, the full original body is
// stored under a content ref granted to principal and the notice names that
// ref for read_output. A store failure omits the ref (INV-AG-10 / INV-CE-07-C).
func capToolResult(result string, maxChars, capabilityMaxBytes int, spool *remainder.Spool, principal string) (string, bool) {
	return remainder.CapWithSpool(spool, principal, result, effectiveResultCap(maxChars, capabilityMaxBytes))
}

// trimPartialRune drops a trailing incomplete UTF-8 sequence.
func trimPartialRune(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
