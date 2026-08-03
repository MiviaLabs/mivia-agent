package agent

// Soft-interrupt (steer) loop tests — plan 54 §7 (agent/loop list). Core
// interrupt behavior; the watchdog/cooldown/stale-signal tests live in
// loop_steer_watchdog_test.go and the shared scripted helpers in
// loop_steer_helpers_test.go.
//
// Behavioral contract under test (plan 54 §4.3):
//   - a soft interrupt cancels ONLY the in-flight LLM call (llmCtx); the loop
//     soft-continues, drains the steer at the next BeforeStep, and never
//     aborts; the final turn text (including an interrupted partial stream)
//     survives.
//   - provider timeouts (DeadlineExceeded) and hard turn-ctx cancellation keep
//     today's propagate-and-abort semantics (Defect 1 / hard-cancel invariant).
//   - a steer arriving while a tool batch runs is never a cancel: the watcher
//     is alive only during the LLM call, and tools run on the turn ctx.
//   - MailboxPendingInterrupt() (Interrupt-flagged steer queued) gates the
//     SIGNAL path, MailboxPending() (any message queued) gates the WATCHDOG
//     path — so a stale signal paired with a later non-interrupt message is
//     never a cancel — the watchdog fires only when pending,
//     SoftInterruptCooldown caps floods, and WatchdogInterval 0 / nil
//     InterruptCh are inert.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestSoftInterruptContinuesLoop: an urgent interrupt cancels only the LLM
// call; the loop soft-continues, drains the steer at the next BeforeStep, and
// the next call completes normally.
func TestSoftInterruptContinuesLoop(t *testing.T) {
	interrupt := make(chan struct{}, 1)
	var pending atomic.Bool
	pending.Store(true)
	stepCalls := 0

	comp := &steerCompleter{
		steps: []steerStep{
			{blockCtx: true}, // call 1: softly interrupted
			{resp: provider.Response{Content: "done", FinishReason: "stop"}},
		},
		started: make(chan struct{}, 1),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started // call 1 in flight
		interrupt <- struct{}{}
	}()

	text, err := runLoop(t, loop, context.Background(), "user task", Options{
		Model:                   "m",
		MaxSteps:                10,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending.Load() },
		SoftInterruptCooldown:   0,
		BeforeStep: func() []provider.Message {
			stepCalls++
			if stepCalls == 1 {
				return nil // step 1: steer has not arrived yet
			}
			msgs := []provider.Message{{Role: provider.RoleUser, Content: FrameParentMessage("steer body")}}
			pending.Store(false) // drain order: queue emptied once injected
			return msgs
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.requests) < 2 {
		t.Fatalf("calls=%d, want >=2", len(comp.requests))
	}
	if !messagesContain(comp.requests[1].Messages, "steer body") {
		t.Fatalf("call 2 request missing injected steer: %+v", comp.requests[1].Messages)
	}
}

// TestSoftInterruptPartialSurvivesAsFinalText: on the streaming path, the
// partial text an interrupted call already emitted must become the final turn
// text when the post-steer reply is empty (Defect 5).
func TestSoftInterruptPartialSurvivesAsFinalText(t *testing.T) {
	const partial = "partial answer"
	interrupt := make(chan struct{}, 1)
	var pending atomic.Bool
	pending.Store(true)
	stepCalls := 0

	var fw strings.Builder
	comp := &steerCompleter{
		steps: []steerStep{
			{partial: partial, blockCtx: true},                           // streams, then softly interrupted
			{resp: provider.Response{Content: "", FinishReason: "stop"}}, // empty reply
		},
		started: make(chan struct{}, 1),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started // call 1 in flight
		interrupt <- struct{}{}
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:                   "m",
		MaxSteps:                10,
		FinalWriter:             &fw,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending.Load() },
		SoftInterruptCooldown:   0,
		BeforeStep: func() []provider.Message {
			stepCalls++
			if stepCalls == 1 {
				return nil
			}
			msgs := []provider.Message{{Role: provider.RoleUser, Content: FrameParentMessage("steer body")}}
			pending.Store(false)
			return msgs
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != partial {
		t.Fatalf("text=%q, want partial %q (Defect 5)", text, partial)
	}
	if !strings.Contains(fw.String(), partial) {
		t.Fatalf("FinalWriter missing streamed partial: %q", fw.String())
	}
	if len(comp.requests) < 2 {
		t.Fatalf("calls=%d, want >=2", len(comp.requests))
	}
	if !messagesContain(comp.requests[1].Messages, partial) {
		t.Fatalf("partial not preserved in history: %+v", comp.requests[1].Messages)
	}
}

// TestSoftInterruptToolBatchNotCanceled: a steer arriving while a tool runs
// must never cancel the batch (safety invariant). The watcher is alive only
// during the LLM call; the interrupt lands at the next BeforeStep.
func TestSoftInterruptToolBatchNotCanceled(t *testing.T) {
	reg := tools.NewRegistry()
	tool := &gateTool{name: "gate", release: make(chan struct{}), started: make(chan struct{}, 1)}
	reg.Register(tool)

	comp := &steerCompleter{
		steps: []steerStep{
			{
				resp: provider.Response{
					FinishReason: "tool_calls",
					ToolCalls:    []provider.ToolCall{tc("1", "gate", `{}`)},
				},
			},
			{resp: provider.Response{Content: "done", FinishReason: "stop"}},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}

	interrupt := make(chan struct{}, 1)
	var pending atomic.Bool
	stepCalls := 0

	go func() {
		<-tool.started // tool in flight; the watcher is NOT alive here
		pending.Store(true)
		interrupt <- struct{}{}
		close(tool.release) // let the tool finish
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:                   "m",
		MaxSteps:                10,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending.Load() },
		SoftInterruptCooldown:   0,
		BeforeStep: func() []provider.Message {
			stepCalls++
			if stepCalls == 1 {
				return nil // step 1: steer has not arrived yet
			}
			msgs := []provider.Message{{Role: provider.RoleUser, Content: FrameParentMessage("steer body")}}
			pending.Store(false) // drain order: queue emptied once injected
			return msgs
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if !tool.executed.Load() {
		t.Fatal("tool did not complete")
	}
	if len(comp.requests) < 2 {
		t.Fatalf("calls=%d, want >=2", len(comp.requests))
	}
	msgs := comp.requests[1].Messages
	if !messagesContain(msgs, "secret-result") {
		t.Fatalf("call 2 history missing completed tool result: %+v", msgs)
	}
	if !messagesContain(msgs, "steer body") {
		t.Fatalf("call 2 history missing injected steer: %+v", msgs)
	}
}

// TestProviderErrorWithPendingSteerStillAborts: a pending steer fires the
// watcher (llmCtx canceled) but the completer surfaces its OWN provider error
// (e.g. an upstream 500), NOT context.Canceled. The provider error must
// propagate and abort the turn — the sentinel must never mask a real failure
// that merely coincides with a steer fire, and there must be NO soft-continue.
func TestProviderErrorWithPendingSteerStillAborts(t *testing.T) {
	interrupt := make(chan struct{}, 1)
	interrupt <- struct{}{} // steer signal ready; MailboxPendingInterrupt true
	comp := &steerCompleter{
		steps: []steerStep{
			{blockCtx: true, cancelErr: errors.New("upstream 500")},
		},
		started: make(chan struct{}, 1),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started // call in flight; the watcher fires on the signal
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:                   "m",
		MaxSteps:                5,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return true },
		SoftInterruptCooldown:   0,
	})
	if err == nil {
		t.Fatal("expected the provider error to abort the turn")
	}
	if !strings.Contains(err.Error(), "upstream 500") {
		t.Fatalf("err=%v, want the upstream provider error propagated (not the sentinel)", err)
	}
	if errors.Is(err, errSteerInterrupt) {
		t.Fatal("provider error must not surface the steer sentinel")
	}
	if text != "" {
		t.Fatalf("text=%q, want empty (no soft-continue on a provider error)", text)
	}
	if len(comp.canceled) != 1 {
		t.Fatalf("canceled calls=%v, want [0] (the watcher did fire, but the error is the provider's)", comp.canceled)
	}
}

// TestHardCancelRacingSteerReturnsCanceled: a hard turn-ctx cancel racing the
// steer fire must surface the REAL cause (context.Canceled), never the steer
// sentinel. Deterministic ordering: the watcher fires on the preloaded signal
// (steerFired + llmCancel), then the test cancels the turn ctx BEFORE the
// completer returns ctx.Err(). requestStep's sentinel guard therefore fails
// its ctx.Err()==nil check and the real cancel propagates; runStep's FIX 2
// re-check covers the same guarantee when the cancel lands between the two.
func TestHardCancelRacingSteerReturnsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupt := make(chan struct{}, 1)
	interrupt <- struct{}{} // steer signal ready; MailboxPendingInterrupt true
	release := make(chan struct{})
	comp := &steerCompleter{
		steps:   []steerStep{{waitGateOnly: true, gate: release}},
		started: make(chan struct{}, 1),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started // call in flight; the watcher fires on the preloaded signal
		cancel()       // hard cancel the turn BEFORE the completer returns
		close(release) // then let the completer return ctx.Err()
	}()

	_, err := runLoop(t, loop, ctx, "user", Options{
		Model:                   "m",
		MaxSteps:                5,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return true },
		SoftInterruptCooldown:   0,
	})
	if err == nil {
		t.Fatal("expected hard cancel to abort the turn")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if errors.Is(err, errSteerInterrupt) {
		t.Fatal("a hard cancel racing a steer fire must not surface the steer sentinel")
	}
}

// TestInterruptChNilNoOp: a nil InterruptCh disables the signal path entirely;
// a normal completion is unaffected.
func TestInterruptChNilNoOp(t *testing.T) {
	comp := &steerCompleter{
		steps: []steerStep{{resp: provider.Response{Content: "done", FinishReason: "stop"}}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	text, err := loop.Run(context.Background(), "user", Options{Model: "m", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.canceled) != 0 {
		t.Fatalf("unexpected cancellation: %v", comp.canceled)
	}
}

// TestSoftInterruptStress runs many interrupt → soft-continue → complete
// cycles (leak proxy): every watcher must come and go with its LLM call, the
// loop must never abort, and the final call must complete. Must pass under
// `go test -race`.
func TestSoftInterruptStress(t *testing.T) {
	const iters = 20
	interrupt := make(chan struct{}, iters+8)
	var pending atomic.Bool
	pending.Store(true)
	stepCalls := 0

	steps := make([]steerStep, 0, iters+1)
	for i := 0; i < iters; i++ {
		steps = append(steps, steerStep{blockCtx: true})
	}
	steps = append(steps, steerStep{resp: provider.Response{Content: "done", FinishReason: "stop"}})

	comp := &steerCompleter{
		steps:   steps,
		started: make(chan struct{}, 8),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started // loop is in flight
		for i := 0; i < iters; i++ {
			interrupt <- struct{}{}
		}
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:                   "m",
		MaxSteps:                iters + 5,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPending:          func() bool { return pending.Load() }, // watchdog gate
		MailboxPendingInterrupt: func() bool { return pending.Load() }, // signal gate
		WatchdogInterval:        10 * time.Millisecond,
		SoftInterruptCooldown:   0,
		BeforeStep: func() []provider.Message {
			stepCalls++
			if stepCalls > iters {
				pending.Store(false) // drained: the final call must complete
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.canceled) != iters {
		t.Fatalf("soft interrupts=%d, want %d", len(comp.canceled), iters)
	}
	if comp.calls != iters+1 {
		t.Fatalf("completer calls=%d, want %d (final call must not be interrupted)", comp.calls, iters+1)
	}
}

// TestWatcherNotSpawnedWhenInert pins PERF-1: with InterruptCh nil AND
// WatchdogInterval 0, requestStep must NOT spawn the watcher at all — pending
// gates alone are a check, never a cancel source. Positive outcome: the
// completer completes normally and no interrupt occurs; the sleep gives any
// hypothetically-spawned (or wrongly-canceling) path time to misbehave.
func TestWatcherNotSpawnedWhenInert(t *testing.T) {
	release := make(chan struct{})
	comp := &steerCompleter{
		steps:   []steerStep{{gate: release}},
		started: make(chan struct{}, 1),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started
		// Give a wrongly-spawned watcher with a cancel-capable path a bounded
		// window to misbehave before release (time.After, not time.Sleep).
		select {
		case <-time.After(40 * time.Millisecond):
		}
		close(release)
	}()

	text, err := runLoop(t, loop, context.Background(), "user", Options{
		Model:                   "m",
		MaxSteps:                5,
		InterruptCh:             nil,
		WatchdogInterval:        0,
		MailboxPending:          func() bool { return true },
		MailboxPendingInterrupt: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "done" {
		t.Fatalf("text=%q, want done", text)
	}
	if len(comp.canceled) != 0 {
		t.Fatalf("inert options canceled the call (watcher must not be spawned): %v", comp.canceled)
	}
}
