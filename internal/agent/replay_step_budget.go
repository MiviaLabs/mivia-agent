// Package agent - the turn-wide step budget shared by the host-side replays.
//
// Two host functions re-run the whole SDK loop inside one turn:
// retryOnEmptyResponse and continueUnactedTurn (agentloop_run.go,
// unacted_turn.go). Each re-run builds a fresh sdkagentloop.Loop, and a
// fresh loop starts its iteration counter at zero. So a turn with
// max_steps = 5 and one replay could make ten provider calls, while
// docs/product/config.md says max_steps "bounds one turn's agent loop".
//
// The bound is documented as a per-turn total, so it is charged as one.
//
// This mirrors the schema-retry loop in internal/subagents/multi_step_schema.go,
// whose comment states the same rule ("re-entry must not extend step
// allowance") and which reads a live step counter for the same reason. The
// two differ on the exhausted branch, deliberately: a schema retry that runs
// out of budget FAILS, because its task never produced valid output, while a
// replay here simply stops and returns the turn it already has. A replay is a
// best-effort improvement on a completed turn, so turning a spent budget into
// a turn error would replace a useful answer, or a precise "turn produced no
// assistant text", with a budget message that says less.

package agent

import (
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
)

// remainingStepBudget returns the iteration bound a replay may run with,
// and whether the turn's budget is already spent.
//
// A non-positive bound is the unbounded contract (agentloop's
// unboundedOrSet maps 0 to MaxInt32) and passes through untouched: turning
// "unlimited" into a computed number would end a turn early, which is the
// zero-means-unlimited defect in reverse. It is returned verbatim rather
// than normalized to 0, so a caller that passed a negative bound keeps
// whatever the SDK's own Validate decides about it, on the replay exactly
// as on the first run.
func remainingStepBudget(bound, spent int) (int, bool) {
	if bound <= 0 {
		return bound, false
	}
	if spent >= bound {
		return 0, true
	}
	return bound - spent, false
}

// replayStepBudget divides one turn's iteration bound across its original
// run and every replay of it.
//
// Spend is read from the turn's own step counter rather than summed from
// each Result. The counter is incremented once per completer call
// (newSDKTurnCompleter's onChat) and the same sdkTurnState is reused across
// every re-run, so it counts calls the Results do not report - a run that
// went through the SDK's prompt-too-long recovery returns only its final
// attempt's Iterations, but every attempt it made billed a provider call.
//
// The bound comes from the built sdkagentloop.Options, not from
// Options.MaxSteps: buildAgentLoopOptions may already have clamped it down
// to WorkLimits.MaxTurns, and the effective bound is the one to divide.
type replayStepBudget struct {
	bound int
	turn  *sdkTurnState
}

func newReplayStepBudget(sdkOpts sdkagentloop.Options, turn *sdkTurnState) *replayStepBudget {
	return &replayStepBudget{bound: sdkOpts.MaxIterations, turn: turn}
}

// next returns the options for one more replay, and whether the budget
// permits it. A false return means the turn has already used every step it
// was given, so the replay must not run at all.
//
// A nil receiver or a nil turn state means no accounting is available; both
// allow the replay with the options unchanged, which is the behavior that
// shipped before the budget existed.
func (b *replayStepBudget) next(sdkOpts sdkagentloop.Options) (sdkagentloop.Options, bool) {
	if b == nil || b.turn == nil {
		return sdkOpts, true
	}
	remaining, exhausted := remainingStepBudget(b.bound, b.turn.currentStep())
	if exhausted {
		return sdkOpts, false
	}
	sdkOpts.MaxIterations = remaining
	return sdkOpts, true
}
