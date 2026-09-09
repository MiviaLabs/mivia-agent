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
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkcontextbudget "github.com/MiviaLabs/mivia-ai-sdk/context/budget"
	sdkplan "github.com/MiviaLabs/mivia-ai-sdk/context/plan"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
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
func adoptSDKRows(l *Loop, out *sdkagentloop.Options, opts Options, completer sdkshape.Completer, turn *sdkTurnState) error {
	adoptSDKUsage(out, opts, turn)
	adoptSDKBudget(out, opts)
	adoptSDKBounds(out, opts)
	adoptSDKTracer(out, turn)
	adoptSDKAudit(out, opts)
	// Row: Window + Summarizer + Calibrated (see sdkCompactionAdopted).
	if err := adoptSDKCompaction(l, out, completer, opts, turn); err != nil {
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
// row unset instead of failing the turn. The accumulator is parked on
// turn so recordSDKTurnTelemetry can read it back after the run; see
// that function for the reader.
func adoptSDKUsage(out *sdkagentloop.Options, opts Options, turn *sdkTurnState) {
	if opts.SessionID == "" {
		return
	}
	acc := sdkadapter.NewAccumulator()
	out.Usage = acc
	turn.setUsage(acc)
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

// adoptSDKBounds sets the failure-spiral hard stop beside the host
// reminder path's breaker (recordProgress), which fires its reminder
// at the same count. MaxTotalTokens deliberately stays unset: the SDK
// bound counts cumulative billed tokens across the whole run, which
// re-bills history every iteration, so the per-prompt context ceiling
// is the wrong scale and would hard-fail healthy long turns.
func adoptSDKBounds(out *sdkagentloop.Options, opts Options) {
	out.Bounds.MaxConsecutiveToolFailures = sdkFailureSpiralBound
}

// adoptSDKTracer gives the run a span tracer and parks it on the turn
// state, where recordSDKTurnTelemetry reads completed spans after the
// run and appends them to the operator audit dump when it is enabled.
// This is a new capability for the host (the legacy loop has no span
// surface), not a replacement for anything; no dedicated
// chatsync/session sink exists yet.
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
// summarizer, whose provider binding sdkSummarizerAdapter rides. A
// wired PreparationManager no longer unconditionally excludes
// adoption - the SDK's Window can own mid-run compaction instead,
// with the PreparationManager running only as bookkeeping through
// Options.ObserveRequest (sdkCompactionObserver), never compacting on
// its own (see applySDKTrim, which stands the host's per-request Trim
// pass down whenever this reports true) - but it DOES require the
// caller to opt in through Options.PreferSDKCompaction: every
// production call site already wires a PreparationManager, so making
// adoption automatic there would silently change every production
// turn's compaction mechanism in one release. The PreparationManager
// == nil row is unaffected by PreferSDKCompaction: that row already
// adopted before this field existed (it is the row
// docs/development/sdk-backend-field-mapping.md called test-only),
// and stays automatic since no PreparationManager-driven behavior
// exists there to regress.
func sdkCompactionAdopted(opts Options) bool {
	if opts.MaxContextTokens <= 0 || opts.SummaryConfig.Summarizer == nil {
		return false
	}
	if opts.PreparationManager == nil {
		return true
	}
	return opts.PreferSDKCompaction
}

// sdkContextWindowForwarded reports the context ceiling the completer
// advertises through the SDK's ContextAccountant. Non-zero only when
// the SDK compaction triple owns the window: on every other turn the
// host contract keeps the SDK's Window nil (host-side preparation or
// prompt-budget preflight owns compaction), and advertising a ceiling
// there could let the SDK derive a default Window on top of the
// host's own per-iteration Trim.
func sdkContextWindowForwarded(opts Options) int {
	if !sdkCompactionAdopted(opts) {
		return 0
	}
	return opts.MaxContextTokens
}

// adoptSDKCompaction wires the SDK's compaction triple directly,
// rather than through sdkagentloop.EnableCompaction: the Summarizer
// is sdkSummarizerAdapter, riding the host's own governed
// contextmgr.Summarizer (redaction, policy binding, evidence
// tracking), not the SDK's generic plan.NewSummarizer-over-completer
// fallback EnableCompaction would wire. This also fixes what used to
// be a known latent issue: the old EnableCompaction path handed
// agentLoopCompleter (the turn-aware wrapper that also advertises
// tools and bumps the shared turn's iteration counter as Chat side
// effects) to the SDK's generic summarizer, so a mid-run compaction
// inherited those side effects. sdkSummarizerAdapter never calls
// agentLoopCompleter at all for summarization - it calls
// opts.SummaryConfig.Summarizer.Summarize, bound to whatever
// completer the host wired it with at construction time, independent
// of agentLoopCompleter. agentLoopCompleter is still used here only
// for token estimation (EstimateTokens below), which has no side
// effects to avoid.
//
// TriggerPercent 100 with TargetTokens at MaxContextTokens/2 is the
// host-style mapping to an exact 80%/50% trigger/target of
// MaxContextTokens (Window.CompactTrigger/CompactTarget price a
// percent against Budget = MaxTokens - Reserve, four fifths of
// MaxTokens here, so a bare 80/50 TriggerPercent/TargetPercent pair
// prices at an effective 64%/40% of MaxContextTokens instead - see
// mivia-ai-sdk's docs/plans/agentloop.md, "Effective thresholds for
// host-style configs"). PreserveNames carries opts.PreparationInput's
// own list (set by internal/chat's prepareInputForContext, the same
// list the PreparationManager path already protects) so a compaction
// that crosses the core-memory frame does not drop it.
func adoptSDKCompaction(l *Loop, out *sdkagentloop.Options, completer sdkshape.Completer, opts Options, turn *sdkTurnState) error {
	if !sdkCompactionAdopted(opts) {
		return nil
	}
	est, ok := completer.(sdkshape.TokenEstimator)
	if !ok {
		return sdkagentloop.ErrNoTokenEstimator
	}
	// Reserve = MaxContextTokens/5 is non-negative by construction
	// (MaxContextTokens > 0 is sdkCompactionAdopted's first gate).
	window := sdkplan.Window{
		MaxTokens: opts.MaxContextTokens,
		Reserve:   opts.MaxContextTokens / 5,
		Compaction: sdkplan.Compaction{
			TriggerPercent: 100,
			TargetTokens:   opts.MaxContextTokens / 2,
			PreserveNames:  opts.PreparationInput.PreserveNames,
		},
	}
	out.Compaction.Window = &window
	out.Compaction.Summarizer = &sdkSummarizerAdapter{l: l, opts: opts}
	out.Compaction.Calibrated = sdkplan.Calibrate(est, 0.25)
	out.ObserveRequest = sdkCompactionObserver(l, opts, turn)
	return nil
}

// sdkCompactionObserver returns the Options.ObserveRequest hook an
// adopted turn wires. It confirms any pending SDK compaction first
// (confirmSDKCompaction), then runs the host's Prepare pass as
// bookkeeping only: Budget is math.MaxInt so Plan never crosses its
// compaction trigger, guaranteeing this call never itself compacts -
// the SDK's own compaction, through sdkSummarizerAdapter, is the only
// compaction path on an adopted turn. Prepare still records
// BeforeTokens/evidence/omitted-diff bookkeeping other host surfaces
// read (ContextUsage, captureOmittedEvidence), and reports the exact
// per-iteration request through opts.ObserveRequestHistory, matching
// what sdkPrepareTrim reports on a non-adopted turn. A Prepare error
// returns unwrapped: the SDK wraps it once more
// (agentloop: iteration N: observe request: %w) before it reaches
// finishErroredContextTurn, which already branches correctly on
// !loop.HasPreparation with no ErrCheckpointConflict masquerade,
// since HasPreparation only flips true inside recordPreparation,
// never called on this error path.
func sdkCompactionObserver(l *Loop, opts Options, turn *sdkTurnState) func(context.Context, sdkshape.Request) error {
	return func(ctx context.Context, req sdkshape.Request) error {
		cliMessages := sdkMessagesToCLI(req.Messages)
		// The bookkeeping Prepare pass only applies when a
		// PreparationManager is wired: the PreparationManager ==
		// nil adoption row (no PM, ceiling + summarizer set) is
		// deliberately kept reachable by sdkCompactionAdopted, and
		// has no PreparationManager to call.
		if opts.PreparationManager != nil {
			toolSpecs := l.initialToolSpecs(opts)
			if adv := turn.currentAdvertised(); adv != nil {
				toolSpecs = adv
			}
			input := l.buildPrepareInput(toolSpecs, opts)
			input.Messages = cliMessages
			input.Budget = math.MaxInt
			preparation, err := opts.PreparationManager.Prepare(ctx, input)
			if err != nil {
				l.PreparationErr = err
				return err
			}
			l.recordPreparation(preparation)
			l.captureOmittedEvidence(input, preparation)
		}
		// Confirmed after the bookkeeping Prepare above, not before:
		// this gives l.LastPreparation.Token a real, freshly-Prepared
		// value before confirmSDKCompaction reads it for the
		// synthetic Preparation it grounds through, so
		// EmitCompaction's SourceRange carries real provenance
		// instead of a first-iteration zero value.
		confirmSDKCompaction(ctx, l, opts)
		if opts.ObserveRequestHistory != nil {
			opts.ObserveRequestHistory(cliMessages)
		}
		return nil
	}
}

// confirmSDKCompaction drains l.sdkPendingCompaction, if set, and
// grounds it into durable turn state. Reaching this call at all is
// the proof the outcome was not abandoned: Options.ObserveRequest
// fires immediately before the SAME iteration's Chat call, whose
// request reflects whatever compactHistory just produced. An
// abandoned attempt (checkCompactedBudget failure, or a recovery
// path that gives up before ever calling reserveWork) never reaches
// this function, so a pending outcome from that attempt is simply
// overwritten by a later real compaction, or discarded with the Loop
// when the turn ends (resetTurnCompaction also clears it at the next
// turn's start, defensively, for any caller that reuses one Loop
// across multiple runs).
//
// recordPreparation already accumulates elided-message/byte counters
// across multiple compactions within one turn (context.go), the same
// mechanism the PreparationManager path uses; passing a synthetic
// Preparation through it here reuses that accumulation instead of
// duplicating it, so a turn with two real SDK compactions grounds and
// emits both, not just the last one.
func confirmSDKCompaction(ctx context.Context, l *Loop, opts Options) {
	pending := l.sdkPendingCompaction
	l.sdkPendingCompaction = nil
	if pending == nil || pending.key == "" || pending.key == l.lastEmittedCompactionKey {
		return
	}
	if pending.summarized {
		l.recordSDKInjectedSummary(pending.message)
	}
	l.recordPreparation(contextmgr.Preparation{
		Compacted:      true,
		Token:          l.LastPreparation.Token,
		ElidedMessages: pending.elidedMessages,
		ElidedBytes:    pending.elidedBytes,
		// The SDK path has no PreparationManager output to price the
		// compaction, and this synthetic record is the turn's first
		// Compacted one - recordPreparation latches its counts for the whole
		// turn. Without the adapter's own estimates the operator banner, the
		// bus event, and the durable usage record all read "0 -> 0 tokens".
		BeforeTokens: pending.beforeTokens,
		AfterTokens:  pending.afterTokens,
	})
	l.lastEmittedCompactionKey = pending.key
	EmitCompaction(ctx, opts, l.LastPreparation, pending.summarized, pending.reason)
	l.turnCompactionEmitted = true
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

// sdkRepeatedToolFailureError converts the SDK's graceful
// StopRepeatedToolFailures stop into the host's failure-spiral hard
// error. The stop arrives with a nil error and a normal-looking
// Result whose Final is the last model turn; returning it unchanged
// let a turn that never ran its work report success with stale text.
// Every other graceful stop stays a non-error.
func sdkRepeatedToolFailureError(res sdkagentloop.Result) error {
	if res.Stop != sdkagentloop.StopRepeatedToolFailures {
		return nil
	}
	return fmt.Errorf("agent: turn stopped by the failure spiral bound after %d iterations; last model text kept in history; see the failure-spiral reminders above each failing turn",
		res.Iterations)
}

// finishAgentLoopTurn is the SDK run's post-run epilogue: stamp the
// tool-message names, map the hard-error and bridge-failure paths,
// convert the repeated-failure stop into a failed turn (the SDK
// reports it as a graceful stop reason with a nil error; the host
// contract expects the turn to fail), and render the final result.
func finishAgentLoopTurn(ctx context.Context, l *Loop, opts Options, turn *sdkTurnState, res sdkagentloop.Result, msgs []provider.Message, err error) (sdkagentloop.Result, error) {
	stampSDKToolMessageNames(res.History)
	recordSDKTurnTelemetry(opts, turn)
	if err != nil {
		return handleSDKRunError(ctx, l, opts, turn, res, err)
	}
	// A surface-bridge failure (registry conversion at a mid-run
	// rotation) recorded in the turn state kept the prior surface so
	// the run could wind down gracefully; the turn still fails with
	// the recorded error, carried through the same partial-Result
	// path as a hard failure.
	if berr := turn.bridgeError(); berr != nil {
		return res, berr
	}
	if rerr := sdkRepeatedToolFailureError(res); rerr != nil {
		return res, rerr
	}
	return finishSDKResult(opts, res, msgs)
}

// recordSDKTurnTelemetry is the reader for the Tracer and Usage
// adoption rows (adoptSDKTracer, adoptSDKUsage): both park state on
// turn that nothing else in the host consumes. This appends one JSON
// line per turn to the operator audit directory (EnvProviderAuditDir;
// the same directory and disable-latch newSDKLoopAuditDump uses, under
// a sibling file name) summarizing the run's spans and usage total, so
// the rows are read at least once instead of accumulating write-only.
// A full consolidation into a dedicated chatsync/session sink (spans)
// and into l.emitTurnUsage (usage) is tracked as future work; see the
// package doc and adoptSDKUsage's comment.
func recordSDKTurnTelemetry(opts Options, turn *sdkTurnState) {
	if turn == nil || (turn.tracer == nil && turn.usage == nil) {
		return
	}
	dir := strings.TrimSpace(os.Getenv(EnvProviderAuditDir))
	if dir == "" {
		return
	}
	if auditDumpDisabled.Load() {
		return
	}
	payload := map[string]any{"session_id": opts.SessionID}
	if turn.tracer != nil {
		spans := turn.tracer.Spans()
		names := make([]string, 0, len(spans))
		for _, s := range spans {
			names = append(names, s.Name)
		}
		payload["span_count"] = len(spans)
		payload["span_names"] = names
	}
	if turn.usage != nil && opts.SessionID != "" {
		if total, ok := turn.usage.Total(opts.SessionID); ok {
			payload["usage_prompt_tokens"] = total.PromptTokens
			payload["usage_completion_tokens"] = total.CompletionTokens
			payload["usage_total_tokens"] = total.TotalTokens
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	path := filepath.Join(dir, "sdktelemetry-"+auditDumpFileName(opts.SessionID))
	if err := appendAuditDumpLine(dir, path, raw); err != nil {
		log.Printf("agent: sdk turn telemetry dump disabled for this process: %v", err)
		auditDumpDisabled.Store(true)
	}
}
