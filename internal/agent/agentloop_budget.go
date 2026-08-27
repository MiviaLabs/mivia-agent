package agent

// SDK-path WorkLimits wiring (Item 8): bridges the SDK agentloop's
// WorkBudget hook onto the SAME workLimitMeter the legacy loop uses
// (work_limits.go). No policy is forked here: reserveProvider,
// refundProvider, and outputCap are the legacy methods; this file only
// supplies the reserve/refund amounts at the SDK's call points.

import (
	"context"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// sdkWorkBudget adapts one Loop's workLimitMeter onto the SDK's
// WorkBudget hook. Reservations stack because concurrent runs on one
// Loop interleave Reserve/Refund pairs; each Refund pops its own
// amounts so an outputCap recompute (whose remaining depends on meter
// state the Reserve already changed) can never mis-settle a refund.
type sdkWorkBudget struct {
	l    *Loop
	opts Options

	mu    sync.Mutex
	meter *workLimitMeter
	stack []sdkReservation
}

// sdkReservation is one Reserve call's amounts, replayed verbatim by
// the matching Refund.
type sdkReservation struct {
	promptTokens int
	outputTokens int
}

// newSDKWorkBudget returns the SDK WorkBudget hook over l's meter,
// applying the SAME reset rule the legacy path applies at loop.go's
// runOnceLegacy: a nil meter, a non-preserved run, or changed limits
// rebuild the meter; PreserveWorkLimits keeps the cumulative counters.
func newSDKWorkBudget(l *Loop, opts Options) *sdkWorkBudget {
	if l.workLimits == nil || !opts.PreserveWorkLimits || l.workLimits.limits != opts.WorkLimits {
		l.workLimits = &workLimitMeter{limits: opts.WorkLimits}
	}
	return &sdkWorkBudget{l: l, opts: opts, meter: l.workLimits}
}

// newSDKWorkBudgetHook builds one SDK turn's WorkBudget bridge and
// the outputCap-clamped MaxTokens: the same clamping the legacy
// stepRequest applies to Options.MaxTokens before each provider call.
func newSDKWorkBudgetHook(l *Loop, opts Options) (*sdkagentloop.WorkBudget, *int, error) {
	b := newSDKWorkBudget(l, opts)
	clamped, err := b.clampedMaxTokens()
	if err != nil {
		return nil, nil, err
	}
	return b.hook(), clamped, nil
}

// clampedMaxTokens mirrors the legacy stepRequest's outputCap over
// Options.MaxTokens: the per-call output ceiling clamped to
// MaxOutputPerCall and the meter's remaining MaxOutputTokens. The
// clamp runs once per RunAgentLoopOnce (option build time); a
// per-step re-clamp inside one SDK turn is an accepted gap.
func (b *sdkWorkBudget) clampedMaxTokens() (*int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.meter.outputCap(b.opts.MaxTokens)
}

// hook projects the bridge onto the SDK's WorkBudget shape.
func (b *sdkWorkBudget) hook() *sdkagentloop.WorkBudget {
	return &sdkagentloop.WorkBudget{
		Reserve: b.reserve,
		Refund:  b.refund,
	}
}

// reserve charges the meter exactly as the legacy requestStep does:
// the estimated prompt cost of the request plus the clamped output
// reserve, through meter.reserveProvider.
func (b *sdkWorkBudget) reserve(ctx context.Context, req sdkshape.Request) error {
	est, _ := provider.EstimatePromptCost(
		sdkMessagesToCLI(req.Messages), b.l.initialToolSpecs(b.opts), b.l.contextAccounting())
	out, err := b.clampedMaxTokens()
	if err != nil {
		return err
	}
	reserve := 0
	if out != nil {
		reserve = *out
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.meter.reserveProvider(est, reserve); err != nil {
		return err
	}
	b.stack = append(b.stack, sdkReservation{promptTokens: est, outputTokens: reserve})
	return nil
}

// refund settles one reservation. A zero Usage means the call never
// consumed it (the SDK's failed-call contract): refund the full
// reservation, mirroring the legacy refundProvider on a steer-canceled
// call. A real Usage settles the output side only: the unused part of
// the output reserve comes back, the prompt estimate stays consumed -
// the legacy consume-on-completion rule.
func (b *sdkWorkBudget) refund(ctx context.Context, req sdkshape.Request, used sdkshape.Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.stack) == 0 {
		return
	}
	res := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	if used.PromptTokens == 0 && used.CompletionTokens == 0 && used.TotalTokens == 0 {
		b.meter.refundProvider(res.promptTokens, res.outputTokens)
		return
	}
	if unused := res.outputTokens - used.CompletionTokens; unused > 0 {
		b.meter.refundProvider(0, unused)
	}
}
