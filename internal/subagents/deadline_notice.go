package subagents

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// The compiled prompt tells a child to "wrap up with margin" when the brief
// states a timeout - but the child had no way to perceive the budget, so the
// instruction was unactionable (adversarial-review finding on abce735c).
// deadlineNoticeBeforeStep closes that: the loop owns the effective deadline
// (callCtx: req.Timeout clamped by TotalTimeout and the parent's deadline),
// and this hook injects ONE user-role notice at the first step boundary
// where the remaining budget drops to the threshold fraction of the whole.
// After the notice the child can act on its own judgment; a second notice
// would only spend its remaining steps on acknowledgements, so the hook
// fires exactly once per task attempt.

const (
	// deadlineNoticeFraction is the remaining-budget fraction at which the
	// notice fires. A quarter leaves three quarters of the budget for real
	// work and a full quarter to land the final report - late enough to be
	// worth waiting for, early enough to be actionable.
	deadlineNoticeFraction = 1.0 / 4.0
)

// deadlineNoticeBeforeStep builds a BeforeStep hook that injects the
// deadline notice once, at the first step boundary at or inside the
// threshold. A context without a deadline (no req.Timeout, no total, no
// parent deadline) yields a hook that never fires; the loop then behaves
// exactly as before this existed.
func deadlineNoticeBeforeStep(ctx context.Context, startedAt time.Time, now func() time.Time) func() []provider.Message {
	var fired atomic.Bool
	return func() []provider.Message {
		if fired.Load() {
			return nil
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil
		}
		nowT := time.Now()
		if now != nil {
			nowT = now()
		}
		remaining := deadline.Sub(nowT)
		if remaining <= 0 {
			return nil // the loop's own deadline checks own the expiry path
		}
		budget := deadline.Sub(startedAt)
		if budget <= 0 || float64(remaining) > deadlineNoticeFraction*float64(budget) {
			return nil
		}
		if !fired.CompareAndSwap(false, true) {
			return nil
		}
		return []provider.Message{{
			Role:    provider.RoleUser,
			Content: deadlineNoticeText(remaining.Round(time.Second)),
		}}
	}
}

// deadlineNoticeText renders the injected notice. Direct and imperative on
// purpose: this is a harness signal, not parent content, so it does not use
// the parent-message framing - but it states the stop condition and the
// partial-results contract explicitly, because the model reading it is
// usually mid-task with an unfinished plan.
func deadlineNoticeText(remaining time.Duration) string {
	return "DEADLINE: " + remaining.String() + " of this task's budget remain (hard stop at zero). " +
		"Wrap up now: produce your final report with the results you have, " +
		"and name explicitly what remains unfinished or unverified."
}

// applyDeadlineNotice composes the deadline notice into the loop's BeforeStep
// chain. The mailbox drain (if any) runs first so a just-arrived steer is
// processed before the wrap-up notice; the deadline hook is additive and must
// never displace parent-message delivery. now may be nil (real clock); it is
// an argument so tests can drive the threshold deterministically.
func applyDeadlineNotice(ctx context.Context, startedAt time.Time, now func() time.Time, opts *agent.Options) {
	notice := deadlineNoticeBeforeStep(ctx, startedAt, now)
	prev := opts.BeforeStep
	if prev == nil {
		opts.BeforeStep = notice
		return
	}
	opts.BeforeStep = func() []provider.Message {
		out := prev()
		out = append(out, notice()...)
		return out
	}
}
