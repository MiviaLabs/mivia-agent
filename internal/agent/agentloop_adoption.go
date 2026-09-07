// Package agent - adoption slices for the SDK loop's own knobs.
//
// Each adopt* function sets one row of the adoption table
// (docs/development/sdk-backend-field-mapping.md): a knob the SDK
// loop already implements that the host previously lacked or ran a
// host-side counterpart of. Setting the field here keeps the
// projection in one place, and the tests in agentloop_adapter_test.go
// pin every row so a future edit cannot silently drop one.
package agent

import (
	"context"
	"time"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkcontextbudget "github.com/MiviaLabs/mivia-ai-sdk/contextbudget"
	sdktrace "github.com/MiviaLabs/mivia-ai-sdk/trace"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// Adoption-table constants. sdkSessionBudgetMaxEvents matches the
// SDK example's session budget; sdkFailureSpiralBound matches the
// host reminder path's own failure-spiral threshold, so the SDK's
// hard stop and the host's degrade-with-notice agree on the number.
const (
	sdkSessionBudgetMaxBytes  = 64 << 20
	sdkSessionBudgetMaxEvents = 4096
	sdkFailureSpiralBound     = 3
)

// adoptSDKRows sets every adoption row in one call; see the per-row
// functions below and the projection test in agentloop_adapter_test.go.
func adoptSDKRows(out *sdkagentloop.Options, opts Options, turn *sdkTurnState) {
	adoptSDKUsage(out, opts)
	adoptSDKBudget(out, opts)
	adoptSDKBounds(out, opts)
	adoptSDKTracer(out, turn)
}

// adoptSDKObservabilityRows sets the audit and conclude rows;
// split from adoptSDKRows to keep both under the function-size
// budget. The heartbeat row is set by the caller once the events
// bridge has installed the bus (agentloop_run.go), because a
// positive HeartbeatInterval without a Bus fails Validate.
func adoptSDKObservabilityRows(out *sdkagentloop.Options, opts Options) {
	adoptSDKAudit(out, opts)
}

// adoptSDKUsage rides the run on the SDK's per-session accumulator,
// so the adapter can read token/cache totals the SDK loop already
// recorded. The host's durable UsageWriter path (l.emitTurnUsage per
// Chat call) stays the system of record; the accumulator is the
// in-process view the SDK loop keeps consistent on its own. The SDK
// rejects Usage without a SessionID, so a blank session ID leaves the
// row unset instead of failing the turn.
func adoptSDKUsage(out *sdkagentloop.Options, opts Options) {
	if opts.SessionID == "" {
		return
	}
	out.Usage = sdkadapter.NewAccumulator()
}

// adoptSDKBudget gives the SDK run a generous runaway bound (bytes
// and events). It is deliberately far above any healthy session: the
// host's own context pruning and batch shaping stay the binding
// budgets, and this bound only stops a loop that has lost its head.
// Deriving it from MaxContextTokens instead would double-bound
// history below the ceiling the operator set, because the SDK's
// budget counts message-history bytes while the ceiling counts
// prompt tokens.
func adoptSDKBudget(out *sdkagentloop.Options, opts Options) {
	out.Budget = &sdkcontextbudget.Limits{
		MaxBytes:  sdkSessionBudgetMaxBytes,
		MaxEvents: sdkSessionBudgetMaxEvents,
	}
}

// adoptSDKBounds sets the two Bounds rows the host has no knob for:
// MaxTotalTokens becomes the per-run billing ceiling derived from the
// host's context-token ceiling, and MaxConsecutiveToolFailures adds a
// hard stop beside the host reminder path's failure-spiral breaker
// (recordProgress), which fires its reminder at the same count. A
// turn that exhausts the bound fails with the SDK's sentinel;
// handleSDKRunError maps it onto the host's loop-breaker vocabulary.
func adoptSDKBounds(out *sdkagentloop.Options, opts Options) {
	if opts.MaxContextTokens > 0 {
		out.Bounds.MaxTotalTokens = opts.MaxContextTokens
	}
	out.Bounds.MaxConsecutiveToolFailures = sdkFailureSpiralBound
}

// adoptSDKTracer gives the run a span tracer and parks it on the turn
// state, where the chatsync/session sink can read completed spans
// after the run. This is a new capability for the host (the legacy
// loop has no span surface), not a replacement for anything.
func adoptSDKTracer(out *sdkagentloop.Options, turn *sdkTurnState) {
	t := sdktrace.New()
	out.Tracer = t
	turn.setTracer(t)
}

// sdkHeartbeatInterval is the SDK loop's progress-tick cadence. It
// matches the controller's durable heartbeat cadence so operator
// surfaces see one tick rhythm whether the tick came from the SDK
// loop or the workflow layer.
const sdkHeartbeatInterval = 15 * time.Second

// sdkConcludeMargin is how close to the iteration bound the SDK loop
// starts nudging the model toward a final answer, and
// sdkConcludeDeadline is the wall-clock term for turns that name one.
// adoptSDKAudit feeds the SDK loop's structured per-call audit
// records into the operator's audit-dump sink when it is enabled. It
// deliberately does NOT replace the completer-seam wire dump: the SDK
// audit record carries the SDK-shaped request, whose reasoning,
// max_tokens, and temperature fields are merged later inside the
// completer, so the two sinks answer different questions (see
// audit_dump.go's package comment).
func adoptSDKAudit(out *sdkagentloop.Options, opts Options) {
	dump := newSDKLoopAuditDump(opts.SessionID)
	if dump == nil {
		return
	}
	out.Audit = func(_ context.Context, rec sdkagentloop.AuditRecord) error {
		dump(rec)
		return nil
	}
}

// adoptSDKHeartbeat turns on the SDK loop's progress ticks next to
// the bridged bus; see bridgeAgentLoopEvents for the tick-to-event
// translation. A positive interval without a Bus fails Validate, so
// the caller sets this only where it installed the bus.
func adoptSDKHeartbeat(out *sdkagentloop.Options) {
	out.HeartbeatInterval = sdkHeartbeatInterval
}
