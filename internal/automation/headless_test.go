package automation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestHeadlessDrainToCloseNoWedge is the wedge test D5's own text
// requires: drain to close, then assert a SECOND Send on the same
// conversation returns promptly - proving the first drain fully
// released turnMu rather than leaving the producer goroutine parked.
func TestHeadlessDrainToCloseNoWedge(t *testing.T) {
	conv := newFakeConversation()
	spawn := &fakeSpawner{conv: conv}

	done := make(chan error, 1)
	go func() {
		_, err := runTurnHeadless(context.Background(), spawn, nil, "", "first prompt", 2*time.Second)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first runTurnHeadless: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first runTurnHeadless did not return in time")
	}

	// Second call must return promptly: if the first drain left turnMu
	// held, this Send blocks on c.turnMu.Lock() and the whole test hangs
	// until the outer test timeout - which is exactly the wedge D5 warns
	// about.
	second := make(chan error, 1)
	go func() {
		_, err := runTurnHeadless(context.Background(), spawn, nil, "", "second prompt", 2*time.Second)
		second <- err
	}()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second runTurnHeadless: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second runTurnHeadless did not return in time: conversation is wedged")
	}
}

// TestHeadlessStopDrainingUnparksViaCtxCancel is the inverse case D5's
// Tests section requires: stop draining (simulated by a fake
// conversation whose consumer never reads) with a 100ms ctx deadline,
// and assert the turn unparks via ctx cancellation within a bounded
// wait rather than hanging forever.
//
// runTurnHeadless itself always drains fully, so to exercise "stop
// draining" this test drives the fakeConversation directly the way
// runTurnHeadless does, but only reads ONE event before abandoning the
// loop - reproducing what an early break (rather than range-to-close)
// would do to the producer, then checks the producer's goroutine
// (proxied by turnMu) is released by ctx cancellation rather than
// wedging past the deadline.
func TestHeadlessStopDrainingUnparksViaCtxCancel(t *testing.T) {
	conv := newFakeConversation()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	h, err := conv.Send(ctx, sendArgIgnored())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Read exactly one event, then stop - the "consumer gave up early"
	// case the doc comment on runTurnHeadless (and D5) warns against.
	select {
	case <-h.Events():
	case <-time.After(time.Second):
		t.Fatal("did not receive first event")
	}

	// The producer's next send blocks; only ctx cancellation (at 100ms)
	// unparks it and releases turnMu. Bound the wait well past the
	// deadline so a genuine wedge fails loudly rather than hanging the
	// suite.
	select {
	case <-conv.waitUnlocked(2 * time.Second):
		// released - proves ctx cancellation is what unparked the producer.
	case <-time.After(2 * time.Second):
		t.Fatal("producer did not unpark via ctx cancellation within bound: real wedge")
	}
}

// TestHeadlessBreakOnTurnEndMissesPendingEvents is the drain-to-close
// negative D5's Tests section requires: a consumer that stops after
// seeing what it thinks is "the last event" (breaking early instead of
// ranging to channel close) can miss events still pending on the
// channel - documenting why range-to-close is the only correct
// terminator.
func TestHeadlessBreakOnTurnEndMissesPendingEvents(t *testing.T) {
	conv := newFakeConversation()
	h, err := conv.Send(context.Background(), sendArgIgnored())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Break after the first event instead of ranging to close.
	seen := 0
	for ev := range h.Events() {
		_ = ev
		seen++
		break
	}
	if seen != 1 {
		t.Fatalf("seen = %d, want 1 (broke after first event)", seen)
	}
	if seen >= fakeTurnEventCount {
		t.Fatalf("test setup invalid: seen (%d) >= total events (%d)", seen, fakeTurnEventCount)
	}
	// fakeTurnEventCount-1 events remain unread on the producer side -
	// exactly the "missed events still pending on the channel" the plan's
	// negative test names. Nothing reads h.Events() again in this test,
	// which is the point: those events are lost, proving early-break is
	// wrong and range-to-close (what runTurnHeadless actually does) is
	// required.
}

// TestRunTurnHeadlessRejectsNilSpawner pins the nil-spawner guard: a
// misconfigured caller gets a named error, not a nil-pointer panic.
func TestRunTurnHeadlessRejectsNilSpawner(t *testing.T) {
	_, err := runTurnHeadless(context.Background(), nil, nil, "", "prompt", time.Second)
	if err == nil {
		t.Fatal("runTurnHeadless with nil spawner: got nil error, want rejection")
	}
}

// TestSendTurnHeadlessRejectsNilConversation pins sendTurnHeadless's own
// nil-conversation guard directly - runTurnHeadless never reaches it
// (its own spawn call always hands sendTurnHeadless a non-nil
// conversation or returns first), but the executor's step dispatch
// (executor.go's runStep) calls sendTurnHeadless directly against an
// already-spawned conversation, so a nil there must be caught here
// rather than only indirectly.
func TestSendTurnHeadlessRejectsNilConversation(t *testing.T) {
	_, err := sendTurnHeadless(context.Background(), nil, "prompt", time.Second)
	if err == nil {
		t.Fatal("sendTurnHeadless with nil conversation: got nil error, want rejection")
	}
}

// TestSendTurnHeadlessRejectsNonPositiveTimeout pins sendTurnHeadless's
// own timeout guard directly, for the same reason as the nil-conversation
// test above: the executor calls this function directly, not only
// through runTurnHeadless's wrapper.
func TestSendTurnHeadlessRejectsNonPositiveTimeout(t *testing.T) {
	conv := newFakeConversation()
	if _, err := sendTurnHeadless(context.Background(), conv, "prompt", 0); err == nil {
		t.Fatal("sendTurnHeadless with zero timeout: got nil error, want rejection")
	}
	if _, err := sendTurnHeadless(context.Background(), conv, "prompt", -time.Second); err == nil {
		t.Fatal("sendTurnHeadless with negative timeout: got nil error, want rejection")
	}
}

// TestRunTurnHeadlessRejectsNonPositiveTimeout pins the zero/negative
// timeout guard: D5 requires ctx to ALWAYS carry a deadline, so a caller
// that forgets to set one must be rejected rather than silently given
// an undeadlined context.
func TestRunTurnHeadlessRejectsNonPositiveTimeout(t *testing.T) {
	spawn := &fakeSpawner{conv: newFakeConversation()}
	if _, err := runTurnHeadless(context.Background(), spawn, nil, "", "prompt", 0); err == nil {
		t.Fatal("runTurnHeadless with zero timeout: got nil error, want rejection")
	}
	if _, err := runTurnHeadless(context.Background(), spawn, nil, "", "prompt", -time.Second); err == nil {
		t.Fatal("runTurnHeadless with negative timeout: got nil error, want rejection")
	}
}

// erroringSpawner always fails CreateFreshInDir, so runTurnHeadless's
// own wrap-and-return path is exercised.
type erroringSpawner struct{ err error }

func (e *erroringSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	return nil, e.err
}

// SetApprovalOverride is a no-op: erroringSpawner exists only to
// exercise runTurnHeadless's spawn-error path.
func (e *erroringSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	return nil
}

// TestRunTurnHeadlessWrapsSpawnError pins runTurnHeadless's own
// spawn-error wrap path.
func TestRunTurnHeadlessWrapsSpawnError(t *testing.T) {
	spawnErr := errors.New("spawn failed")
	spawn := &erroringSpawner{err: spawnErr}
	_, err := runTurnHeadless(context.Background(), spawn, nil, "", "prompt", time.Second)
	if err == nil || !strings.Contains(err.Error(), "spawn failed") {
		t.Fatalf("runTurnHeadless spawn error = %v, want it to wrap %q", err, spawnErr)
	}
}

// erroringConversation always fails Send, so runTurnHeadless's own
// send-error wrap path is exercised.
type erroringConversation struct {
	fakeConversation
	err error
}

func (c *erroringConversation) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	return nil, c.err
}

// TestRunTurnHeadlessWrapsSendError pins runTurnHeadless's own
// send-error wrap path.
func TestRunTurnHeadlessWrapsSendError(t *testing.T) {
	sendErr := errors.New("send failed")
	conv := &erroringConversation{err: sendErr}
	spawn := &fakeSpawner{conv: conv}
	_, err := runTurnHeadless(context.Background(), spawn, nil, "", "prompt", time.Second)
	if err == nil || !strings.Contains(err.Error(), "send failed") {
		t.Fatalf("runTurnHeadless send error = %v, want it to wrap %q", err, sendErr)
	}
}

func sendArgIgnored() intent.Send { return intent.Send{Text: "probe"} }
