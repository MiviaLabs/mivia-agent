package agent

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// slowCompleter delays ChatTurn so the model-thinking heartbeat has time to
// fire while the provider request is still in flight.
type slowCompleter struct {
	delay time.Duration
}

func (s *slowCompleter) Name() string { return "slow" }
func (s *slowCompleter) Chat(ctx context.Context, _ provider.Request) (string, error) {
	select {
	case <-time.After(s.delay):
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (s *slowCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return s.Chat(ctx, req)
}
func (s *slowCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	select {
	case <-time.After(s.delay):
		return &provider.Response{Content: "done", FinishReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestLoopModelThinkingHeartbeatUsesEventHeartbeat verifies the wall-clock
// progress cadence during a slow provider request is EventHeartbeat, never
// EventStep. Only real loop steps (emitStep) may be EventStep, so schema-retry
// step budgets and step_count in internal/subagents are not inflated by the
// heartbeat (F5).
func TestLoopModelThinkingHeartbeatUsesEventHeartbeat(t *testing.T) {
	old := modelThinkingHeartbeatInterval
	modelThinkingHeartbeatInterval = 10 * time.Millisecond
	defer func() { modelThinkingHeartbeatInterval = old }()

	reg := tools.NewRegistry()
	// Completer blocks ~25x the heartbeat interval, so several heartbeats fire
	// while the provider request is in flight.
	loop := &Loop{Completer: &slowCompleter{delay: 250 * time.Millisecond}, Tools: reg}

	var mu sync.Mutex
	var events []Event
	_, err := loop.Run(context.Background(), "blocking request", Options{
		Model:    "test",
		MaxSteps: 3,
		OnEvent: func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	var heartbeatEvents, workingStepEvents, realStepEvents int
	for _, e := range events {
		switch e.Kind {
		case EventHeartbeat:
			heartbeatEvents++
			if e.Detail != "working" {
				t.Fatalf("heartbeat detail = %q, want %q", e.Detail, "working")
			}
		case EventStep:
			realStepEvents++
			if e.Detail == "working" {
				workingStepEvents++
			}
		default:
			// EventAssistant for the final answer; nothing else expected here.
		}
	}
	if heartbeatEvents == 0 {
		t.Fatal("expected EventHeartbeat events during the slow provider request, got none")
	}
	if workingStepEvents > 0 {
		t.Fatalf("model-thinking heartbeats must not be EventStep: %d EventStep events with detail %q", workingStepEvents, "working")
	}
	if realStepEvents != 1 {
		t.Fatalf("real EventStep events = %d, want 1 (the loop's initial emitStep)", realStepEvents)
	}
}

// TestModelThinkingHeartbeatIntervalIsOverridable verifies the package-level
// modelThinkingHeartbeatInterval is honored: with a ~10ms override the first
// heartbeat arrives in tens of milliseconds, not after the 2s default.
func TestModelThinkingHeartbeatIntervalIsOverridable(t *testing.T) {
	old := modelThinkingHeartbeatInterval
	modelThinkingHeartbeatInterval = 10 * time.Millisecond
	defer func() { modelThinkingHeartbeatInterval = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan Event, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		emitModelThinkingHeartbeat(ctx, Options{
			OnEvent: func(e Event) { events <- e },
		})
	}()

	start := time.Now()
	select {
	case e := <-events:
		if e.Kind != EventHeartbeat {
			t.Fatalf("heartbeat kind = %q, want %q", e.Kind, EventHeartbeat)
		}
		if e.Detail != "working" {
			t.Fatalf("heartbeat detail = %q, want %q", e.Detail, "working")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("heartbeat did not arrive at the overridden cadence")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("first heartbeat arrived after %s, want ~10ms (overridden interval)", elapsed)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("model-thinking heartbeat did not stop after cancellation")
	}
}
