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
	"fmt"
	"time"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkcontextbudget "github.com/MiviaLabs/mivia-ai-sdk/contextbudget"
	sdkplan "github.com/MiviaLabs/mivia-ai-sdk/contextplan"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
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
func adoptSDKRows(out *sdkagentloop.Options, opts Options, completer sdkshape.Completer, turn *sdkTurnState) error {
	adoptSDKUsage(out, opts)
	adoptSDKBudget(out, opts)
	adoptSDKBounds(out, opts)
	adoptSDKTracer(out, turn)
	adoptSDKAudit(out, opts)
	// Row: Window + Summarizer + Calibrated (see sdkCompactionAdopted).
	if err := adoptSDKCompaction(out, completer, opts); err != nil {
		return fmt.Errorf("agent: adopt SDK compaction: %w", err)
	}
	return nil
}

// applySDKTrim installs the host Trim pass unless the SDK compaction
// triple owns the window: the SDK's Window and Trim are mutually
// exclusive, and an adopted turn stands the host trim down with the
// host summary injection.
func applySDKTrim(l *Loop, opts Options, turn *sdkTurnState, out *sdkagentloop.Options) {
	if sdkCompactionAdopted(opts) {
		return
	}
	out.Trim = sdkPrepareTrim(l, opts, turn)
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

// sdkCompactionAdopted reports whether a turn wires the SDK's
// compaction triple (Window + Summarizer + Calibrated): it needs a
// context ceiling to size the window from and a wired host
// summarizer, whose provider binding the SDK summarizer rides. The
// triple and the host's summary injection are complementary, not
// duplicates: the host injects once, after its own pre-run prepare
// compaction, while the SDK triple compacts mid-run growth inside the
// loop. Only the host Trim pass stands down on adopted turns, because
// the SDK's Window and Trim are mutually exclusive.
func sdkCompactionAdopted(opts Options) bool {
	return opts.MaxContextTokens > 0 && opts.SummaryConfig.Summarizer != nil &&
		opts.PreparationManager == nil
}

// adoptSDKCompaction wires the SDK's compaction triple. The wrapped
// completer implements provider.TokenEstimator (EstimateTokens
// below), so sdkagentloop.EnableCompaction can size the window from
// the host's context ceiling with the SDK's default hysteresis
// (trigger at 80%, target at 50%) and a conservative calibration
// factor.
func adoptSDKCompaction(out *sdkagentloop.Options, completer sdkshape.Completer, opts Options) error {
	if !sdkCompactionAdopted(opts) {
		return nil
	}
	window := sdkplan.Window{
		MaxTokens:  opts.MaxContextTokens,
		Reserve:    opts.MaxContextTokens / 5,
		Compaction: sdkplan.Compaction{TriggerPercent: 80, TargetPercent: 50},
	}
	if window.Reserve < 0 {
		window.Reserve = 0
	}
	return sdkagentloop.EnableCompaction(out, completer, window, 0.25)
}

// EstimateTokens implements provider.TokenEstimator for the wrapped
// CLI completer. It converts the SDK request back to the CLI shape
// and runs the host's own EstimatePromptCost with the loop's context
// accounting profile, so the SDK compaction trigger uses exactly the
// token semantics the host's context manager calibrated. The host's
// provider interface has no estimator capability, so this adapter is
// what makes the SDK's compaction triple usable with the CLI
// completer without a second provider round trip.
func (c *agentLoopCompleter) EstimateTokens(req sdkshape.Request) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("agent: nil completer for token estimation")
	}
	return provider.EstimatePromptCost(sdkMessagesToCLI(req.Messages), sdkToolDefsToCLI(req.Tools), c.ctxProfile)
}
