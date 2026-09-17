package agent

// Pins for runOnceSDK's bounded steered-continue loop (the host side of
// the SDK v0.7.0 ContinueOnStop steered-stop gate): the exhaustion
// branch surfaces errSteerInterrupt instead of looping forever, and the
// turn's user message is never duplicated across steered re-runs.

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestSteeredContinueExhaustsBudgetSurfacesInterrupt drives two steered
// stops against MaxSteps: 1. The first steered stop consumes the one
// allowed continue; the second must surface sdkSteeredStopPartial's
// ("", errSteerInterrupt) instead of re-running forever. The user
// message pre-appended once before the loop must appear exactly once in
// the carried history after the re-run's write-back.
func TestSteeredContinueExhaustsBudgetSurfacesInterrupt(t *testing.T) {
	interrupt := make(chan struct{}, 8)
	pending := true
	comp := &steerCompleter{
		steps: []steerStep{
			{blockCtx: true}, // call 1: steered stop one
			{blockCtx: true}, // call 2: steered stop two, budget now spent
		},
		started: make(chan struct{}, 4),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}

	go func() {
		<-comp.started // call 1 in flight
		interrupt <- struct{}{}
		<-comp.started // call 2 in flight
		interrupt <- struct{}{}
	}()

	text, err := runLoop(t, loop, context.Background(), "turn text", Options{Model: "m",
		MaxSteps:                1,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending },
		SoftInterruptCooldown:   0,
		BeforeStep: func() []provider.Message {
			return nil
		},
	})
	if !errors.Is(err, errSteerInterrupt) {
		t.Fatalf("err = %v, want errSteerInterrupt: the steered-continue budget must exhaust and surface", err)
	}
	if text != "" {
		t.Fatalf("text=%q, want empty (no assistant text survived the cancels)", text)
	}
	if comp.canceledCount() != 2 {
		t.Fatalf("canceled calls=%d, want 2 (both steered stops cancel their in-flight call)", comp.canceledCount())
	}
	count := 0
	for _, m := range loop.Messages {
		if m.Content == "turn text" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("user message occurrences=%d, want 1 across steered re-runs: %+v", count, loop.Messages)
	}
}
