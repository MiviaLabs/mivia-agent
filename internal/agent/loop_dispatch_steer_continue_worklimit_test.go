package agent

// Regression tests for the turn-scoped steered-continue fix in
// loop_dispatch.go (runOnceSDK): before the fix, each steered re-run
// built a fresh sdkTurnState and a fresh WorkBudget meter (opts local
// to runOnceSDK never forced PreserveWorkLimits across re-runs), so a
// turn's cumulative token/tool-call budget and step count could
// multiply by (steerContinues+1) instead of being capped once across
// the whole turn. It also never bounded the steered re-run COUNT when
// both MaxSteps and WorkLimits.MaxTurns were unset, so a steer source
// that fired on every step could re-run a turn forever.
//
// The fix:
//   - stepsConsumed accumulates every re-run's res.Iterations, and a
//     positive opts.MaxSteps is shrunk to the remaining turn budget
//     before each re-run;
//   - opts.PreserveWorkLimits is forced true from the second re-run on
//     so the workLimitMeter carries its cumulative counts forward
//     instead of resetting;
//   - maxUnboundedSteerContinues (25) bounds the re-run COUNT when
//     effectiveSDKMaxIterations reports no cap.
//
// (a) pins the WorkLimits persistence half of the fix, (b) pins the
// step-budget clamp half, (c) pins the maxUnboundedSteerContinues
// fallback.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestSteeredContinuePreservesWorkLimitAcrossReRuns is the regression
// test for the WorkLimits-reset bug: with WorkLimits{MaxOutputTokens:
// 100} and MaxTokens pinned to 50 (X/2) per call, the turn is scripted
// so exactly two completed calls (run 1's first iteration, run 3's
// first iteration) each settle 50 output tokens - X exactly, with a
// steered (canceled) call bookending each of the first two runs so the
// turn re-runs twice before the third attempt.
//
// Pre-fix: opts.PreserveWorkLimits was never forced across re-runs, so
// newSDKWorkBudget rebuilt a FRESH workLimitMeter at the top of every
// RunAgentLoopOnce call (l.workLimits == nil, or !opts.PreserveWorkLimits
// on a caller that never set it). Every re-run would see remaining =
// 100 again, the completer's steps array would run past its scripted
// entries into the default "done" response (a graceful stop, not a
// steer), and the turn would return successfully with no error -
// silently discarding the shared budget's whole purpose. This is
// verified directly below by stashing the fix and re-running the test
// (see the chunk report for the RED confirmation).
//
// Post-fix: the meter carries 50 (net) out of run 1, unchanged through
// run 2 (a single canceled call, net refund to 0), and the second
// completed call in run 3 brings the meter to exactly 100 - exhausted.
// Run 3's second iteration (the canceled/steered one) then fails
// CLOSED at its WorkBudget.Reserve call ("work limit exceeded: output
// tokens") instead of gracefully steering again.
func TestSteeredContinuePreservesWorkLimitAcrossReRuns(t *testing.T) {
	const outputLimit = 100
	perCall := outputLimit / 2 // 50: the completer's fixed reported output usage per completed call
	maxTokens := perCall

	completedStep := func(id string) steerStep {
		return steerStep{resp: provider.Response{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc(id, "noop_tool", `{}`)},
			TokenUsage:   provider.TokenUsage{Reported: true, InputTokens: 5, OutputTokens: perCall},
		}}
	}
	canceledStep := steerStep{blockCtx: true}

	comp := &steerCompleter{
		steps: []steerStep{
			completedStep("1"), // idx0: run1 iter1 - completes, settles 50 (remaining 100->50)
			canceledStep,       // idx1: run1 iter2 - steered stop (net refund 0, remaining stays 50)
			canceledStep,       // idx2: run2's only call - steered stop (net refund 0, remaining stays 50)
			completedStep("2"), // idx3: run3 iter1 - completes, settles 50 (remaining 50->0)
			// idx4 (run3's would-be iter2) is never reached: its reserve
			// fails closed before ChatTurn is invoked.
		},
		started: make(chan struct{}, 8),
	}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}

	interrupt := make(chan struct{}, 4)
	var pending atomic.Bool
	stepCalls := 0

	go func() {
		<-comp.started // idx0 completed: no interrupt
		<-comp.started // idx1 canceled (run1 iter2)
		interrupt <- struct{}{}
		<-comp.started // idx2 canceled (run2's only call)
		interrupt <- struct{}{}
		<-comp.started // idx3 completed (run3 iter1): no interrupt
	}()

	text, err := runLoop(t, loop, context.Background(), "user task", Options{
		Model:                   "m",
		MaxSteps:                20, // ample headroom: isolates this test to the WorkLimits half of the fix
		MaxTokens:               &maxTokens,
		WorkLimits:              runtime.WorkLimits{MaxOutputTokens: outputLimit},
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending.Load() },
		SoftInterruptCooldown:   0,
		BeforeStep: func() []provider.Message {
			stepCalls++
			switch stepCalls {
			case 2, 3: // before the two canceled calls (idx1, idx2)
				pending.Store(true)
			default:
				pending.Store(false)
			}
			return nil
		},
	})

	if err == nil {
		t.Fatalf("err = nil, text = %q, want a work-limit-exceeded error on the 3rd re-run (pre-fix this silently succeeded on a fresh meter)", text)
	}
	if !strings.Contains(err.Error(), "work limit exceeded") {
		t.Fatalf("err = %v, want it to name the exhausted output-token work limit", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty (the turn hard-failed on the exhausted budget)", text)
	}
	if comp.calls != 4 {
		t.Fatalf("completer calls = %d, want 4 (idx0-3; idx4's reserve must fail BEFORE any 5th ChatTurn call)", comp.calls)
	}
	if got := loop.workLimits.outputTokens; got != outputLimit {
		t.Fatalf("loop.workLimits.outputTokens = %d, want %d (fully consumed by the two completed calls; a failed reserve must not mutate the meter)", got, outputLimit)
	}
}

// TestSteeredContinueTurnScopedStepBudgetNeverExceedsMaxSteps pins the
// step-budget half of the fix: with MaxSteps = 8 and a completer whose
// script forces two full steered re-runs (each consuming 3 real steps
// before a steered/canceled stop), the TURN's total completed-step
// count across every re-run must never exceed 8.
//
// Pre-fix, opts.MaxSteps was never shrunk across re-runs: every re-run
// restarted with the SAME MaxSteps = 8 budget, so this exact script
// would have consumed 3 real steps per re-run for as many re-runs as
// maxSteerContinues (effectiveSDKMaxIterations(8) = 8) allowed -
// dramatically exceeding 8 real provider steps for one turn.
//
// Post-fix, each re-run's MaxSteps is clamped to the remaining turn
// budget (8, then 5, then 2), so run 3's Bounds.MaxIterations = 2 is
// hit exactly by its two completed steps with no room for a further
// canceled call - the loop reports StopMaxIterations (a hard
// "exceeded max_steps" error) instead of continuing past the cap.
func TestSteeredContinueTurnScopedStepBudgetNeverExceedsMaxSteps(t *testing.T) {
	const turnMaxSteps = 8

	completedStep := func(id string) steerStep {
		return steerStep{resp: provider.Response{
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{tc(id, "noop_tool", `{}`)},
		}}
	}
	canceledStep := steerStep{blockCtx: true}

	comp := &steerCompleter{
		steps: []steerStep{
			completedStep("1"), completedStep("2"), completedStep("3"), canceledStep, // run1: 3 real steps + steered stop
			completedStep("4"), completedStep("5"), completedStep("6"), canceledStep, // run2: 3 real steps + steered stop
			completedStep("7"), completedStep("8"), // run3: exactly 2 real steps left in the turn budget; hits the cap with no room for a 3rd call
		},
		started: make(chan struct{}, 12),
	}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}

	interrupt := make(chan struct{}, 4)
	var pending atomic.Bool
	stepCalls := 0

	go func() {
		for block := 0; block < 2; block++ {
			<-comp.started // completed 1
			<-comp.started // completed 2
			<-comp.started // completed 3
			<-comp.started // canceled
			interrupt <- struct{}{}
		}
		<-comp.started // run3 completed 1
		<-comp.started // run3 completed 2 (turn budget exhausted; no 3rd call is ever attempted)
	}()

	text, err := runLoop(t, loop, context.Background(), "user task", Options{
		Model:                   "m",
		MaxSteps:                turnMaxSteps,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending.Load() },
		SoftInterruptCooldown:   0,
		BeforeStep: func() []provider.Message {
			stepCalls++
			switch stepCalls {
			case 4, 8: // before the two canceled calls (idx3, idx7)
				pending.Store(true)
			default:
				pending.Store(false)
			}
			return nil
		},
	})

	if err == nil {
		t.Fatalf("err = nil, text = %q, want an exceeded-max_steps error once the turn-wide budget is spent", text)
	}
	if !strings.Contains(err.Error(), "exceeded max_steps") {
		t.Fatalf("err = %v, want an exceeded max_steps error", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
	if comp.calls != 10 {
		t.Fatalf("completer calls = %d, want 10 (idx0-9; no 11th call once the turn budget is spent)", comp.calls)
	}
	if len(comp.canceled) != 2 {
		t.Fatalf("canceled calls = %v, want exactly 2 (run1 and run2's steered stops)", comp.canceled)
	}
	gotSteps := comp.calls - len(comp.canceled)
	if gotSteps > turnMaxSteps {
		t.Fatalf("total completed steps across all re-runs = %d, want <= %d (pre-fix this could reach steerContinues * MaxSteps)", gotSteps, turnMaxSteps)
	}
	if gotSteps != turnMaxSteps {
		t.Fatalf("total completed steps across all re-runs = %d, want exactly %d (the turn-scoped cap should be hit precisely, not undershot)", gotSteps, turnMaxSteps)
	}
}

// TestSteeredContinueUnboundedFallsBackToMaxUnboundedSteerContinues
// pins the common subagent-default configuration - MaxSteps = 0 and
// WorkLimits unset/zero - against both halves of the
// maxUnboundedSteerContinues fix:
//
//  1. A steered stop must actually soft-continue (BeforeStep drains
//     the mailbox and the loop proceeds) rather than immediately
//     returning errSteerInterrupt on the FIRST steered stop. Pre-fix,
//     effectiveSDKMaxIterations(opts) reported 0 (no cap) when both
//     MaxSteps and WorkLimits.MaxTurns are unset, and maxSteerContinues
//     was assigned that 0 directly - so steerContinues(0) < maxSteerContinues(0)
//     was false immediately and NO steered stop ever continued, however
//     many the mailbox actually queued.
//  2. Using a mock steer source that signals on EVERY step, the re-run
//     count must terminate at maxUnboundedSteerContinues (25), not
//     literally forever.
func TestSteeredContinueUnboundedFallsBackToMaxUnboundedSteerContinues(t *testing.T) {
	const totalRuns = maxUnboundedSteerContinues + 1 // the initial run + 25 continues

	steps := make([]steerStep, totalRuns)
	for i := range steps {
		steps[i] = steerStep{blockCtx: true}
	}
	comp := &steerCompleter{steps: steps, started: make(chan struct{}, totalRuns+2)}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	interrupt := make(chan struct{}, totalRuns+2)
	var pending atomic.Bool
	pending.Store(true) // the mock steer source signals on every step

	go func() {
		for i := 0; i < totalRuns; i++ {
			<-comp.started
			interrupt <- struct{}{}
		}
	}()

	text, err := runLoop(t, loop, context.Background(), "user task", Options{
		Model: "m",
		// MaxSteps left unset (0) and WorkLimits left zero: the common
		// subagent-default shape effectiveSDKMaxIterations reports as
		// "no cap".
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending.Load() },
		SoftInterruptCooldown:   0,
		BeforeStep:              func() []provider.Message { return nil },
	})

	if !errors.Is(err, errSteerInterrupt) {
		t.Fatalf("err = %v, want errSteerInterrupt once maxUnboundedSteerContinues is exhausted", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty (no assistant text survived the cancels)", text)
	}
	// The core soft-continue assertion: pre-fix, the very FIRST steered
	// stop would have returned errSteerInterrupt immediately (calls == 1).
	if comp.calls <= 1 {
		t.Fatalf("completer calls = %d, want > 1 (the first steered stop must soft-continue, not return immediately)", comp.calls)
	}
	if comp.calls != totalRuns {
		t.Fatalf("completer calls = %d, want %d (maxUnboundedSteerContinues + 1 initial run - bounded, not literally forever)", comp.calls, totalRuns)
	}
	if comp.canceledCount() != totalRuns {
		t.Fatalf("canceled calls = %d, want %d", comp.canceledCount(), totalRuns)
	}
}
