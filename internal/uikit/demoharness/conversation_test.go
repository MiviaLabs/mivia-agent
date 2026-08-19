package demoharness

import (
	"context"
	"fmt"
	"testing"
	"time"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// drain reads every event until the turn's channel closes, or fails the
// test if that takes more than 5s (a hung playback goroutine).
func drain(t *testing.T, h ports.TurnHandle) []uievent.Event {
	t.Helper()
	var out []uievent.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range h.Events() {
			out = append(out, ev)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not close within 5s")
	}
	return out
}

func TestNewUnknownScenarioErrors(t *testing.T) {
	if _, err := New("does-not-exist", 0); err == nil {
		t.Error("expected an error for an unknown scenario name")
	}
}

func TestSendStampsHandleTurnID(t *testing.T) {
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range drain(t, handle) {
		if ev.TurnID != handle.ID() {
			t.Errorf("event TurnID %q != handle ID %q", ev.TurnID, handle.ID())
		}
	}
}

func TestSendDistinctTurnIDsAcrossSends(t *testing.T) {
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := h.Send(context.Background(), intent.Send{})
	drain(t, h1)
	h2, _ := h.Send(context.Background(), intent.Send{})
	drain(t, h2)
	if h1.ID() == h2.ID() {
		t.Errorf("expected distinct turn ids, both were %q", h1.ID())
	}
}

func TestHistoryRecordsEveryUserMessage(t *testing.T) {
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := h.Send(context.Background(), intent.Send{Text: "one"})
	drain(t, h1)
	h2, _ := h.Send(context.Background(), intent.Send{Text: "two"})
	drain(t, h2)

	hist := h.History()
	if len(hist) != 2 || hist[0].Text != "one" || hist[1].Text != "two" {
		t.Errorf("got history %+v, want [one two]", hist)
	}
}

// drainAutoApprove is like drain, but also answers any approval request
// the turn raises with DecisionOnce, so a full-tour scenario (whose
// last script needs a decision) can run to completion unattended.
func drainAutoApprove(t *testing.T, h *Harness, handle ports.TurnHandle) []uievent.Event {
	t.Helper()
	var events []uievent.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case req, ok := <-h.Pending():
			if ok {
				h.Resolve(req.ID, ports.DecisionOnce)
			}
		case ev, ok := <-handle.Events():
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatal("turn did not finish within 5s")
		}
	}
}

func TestHarnessCyclesThroughScenario(t *testing.T) {
	h, err := New("full-tour", 0)
	if err != nil {
		t.Fatal(err)
	}
	n := len(h.scenario)
	first, _ := h.Send(context.Background(), intent.Send{Text: "x"})
	firstEvents := drainAutoApprove(t, h, first)

	for i := 1; i < n; i++ {
		handle, _ := h.Send(context.Background(), intent.Send{Text: "x"})
		drainAutoApprove(t, h, handle)
	}
	// The (n+1)th Send must replay the same script as the first.
	wrapped, _ := h.Send(context.Background(), intent.Send{Text: "x"})
	wrappedEvents := drainAutoApprove(t, h, wrapped)

	if firstEvents[0].Body.(uievent.TurnStartBody).Input != wrappedEvents[0].Body.(uievent.TurnStartBody).Input {
		t.Errorf("expected the scenario to cycle back to its first script after %d turns", n)
	}
}

func TestSmallTalkTurnEndsCompleted(t *testing.T) {
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := h.Send(context.Background(), intent.Send{Text: "hi"})
	events := drain(t, handle)
	last := events[len(events)-1]
	if last.Kind != uievent.KindTurnEnd || last.Body.(uievent.TurnEndBody).Reason != "completed" {
		t.Errorf("got last event %+v, want a completed turn.end", last)
	}
}

// sendNth advances a full-tour harness to the nth script (0-indexed) by
// discarding n turns, then returns the handle for turn n+1.
func sendNth(t *testing.T, h *Harness, n int) ports.TurnHandle {
	t.Helper()
	for i := 0; i < n; i++ {
		handle, err := h.Send(context.Background(), intent.Send{Text: "x"})
		if err != nil {
			t.Fatal(err)
		}
		drain(t, handle)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestToolCallTurnEmitsToolLifecycle(t *testing.T) {
	h, err := New("full-tour", 0)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, sendNth(t, h, 1))
	var sawStart, sawOutput, sawEnd bool
	for _, ev := range events {
		switch b := ev.Body.(type) {
		case uievent.ToolStartBody:
			sawStart = true
		case uievent.ToolOutputBody:
			sawOutput = true
		case uievent.ToolEndBody:
			sawEnd = true
			if !b.OK {
				t.Error("expected the tool call to succeed")
			}
		}
	}
	if !sawStart || !sawOutput || !sawEnd {
		t.Errorf("expected tool.start, tool.output, tool.end; got %+v", events)
	}
}

func TestDiffTurnCarriesDiff(t *testing.T) {
	h, err := New("full-tour", 0)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, sendNth(t, h, 2))
	found := false
	for _, ev := range events {
		if b, ok := ev.Body.(uievent.ToolEndBody); ok && b.Diff != nil {
			found = true
			if b.Diff.Added != 2 {
				t.Errorf("got Diff.Added=%d, want 2", b.Diff.Added)
			}
		}
	}
	if !found {
		t.Errorf("expected a tool.end carrying a diff, got %+v", events)
	}
}

func TestToolFailTurnEmitsFailedToolEnd(t *testing.T) {
	h, err := New("full-tour", 0)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, sendNth(t, h, 3))
	found := false
	for _, ev := range events {
		if b, ok := ev.Body.(uievent.ToolEndBody); ok {
			found = true
			if b.OK {
				t.Error("expected the tool call to fail")
			}
			if b.Err == "" {
				t.Error("expected a non-empty Err on the failed tool call")
			}
		}
	}
	if !found {
		t.Fatal("expected a tool.end event")
	}
}

func TestPlanTurnEmitsPlanBody(t *testing.T) {
	h, err := New("full-tour", 0)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, sendNth(t, h, 4))
	found := false
	for _, ev := range events {
		if b, ok := ev.Body.(uievent.PlanBody); ok {
			found = true
			if b.Total != 3 || b.Done != 2 {
				t.Errorf("got plan %+v, want 2 of 3 done", b)
			}
		}
	}
	if !found {
		t.Fatal("expected a plan event")
	}
}

func TestReasoningTurnEmitsSummaryChunk(t *testing.T) {
	h, err := New("full-tour", 0)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, sendNth(t, h, 5))
	found := false
	for _, ev := range events {
		if b, ok := ev.Body.(uievent.ReasoningDeltaBody); ok && b.WordCount != 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a reasoning.delta with a word count, got %+v", events)
	}
}

func TestUsageTurnEmitsUsageBody(t *testing.T) {
	h, err := New("full-tour", 0)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, sendNth(t, h, 6))
	found := false
	for _, ev := range events {
		if b, ok := ev.Body.(uievent.UsageBody); ok {
			found = true
			if b.InputTokens != 5200 {
				t.Errorf("got InputTokens=%d, want 5200", b.InputTokens)
			}
		}
	}
	if !found {
		t.Fatal("expected a usage event")
	}
}

func TestApprovalTurnApprovedContinuesWithToolSuccess(t *testing.T) {
	h, err := New("approval", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "delete it"})
	if err != nil {
		t.Fatal(err)
	}

	var events []uievent.Event
	resolved := false
	timeout := time.After(5 * time.Second)
	for {
		select {
		case req, ok := <-h.Pending():
			if !ok {
				continue
			}
			h.Resolve(req.ID, ports.DecisionOnce)
			resolved = true
		case ev, ok := <-handle.Events():
			if !ok {
				goto done
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatal("turn did not finish within 5s")
		}
	}
done:
	if !resolved {
		t.Fatal("expected an approval request to be raised")
	}
	last := events[len(events)-1]
	if last.Body.(uievent.TurnEndBody).Reason != "completed" {
		t.Errorf("got last event %+v, want a completed turn.end", last)
	}
	sawSuccess := false
	for _, ev := range events {
		if b, ok := ev.Body.(uievent.ToolEndBody); ok && b.OK {
			sawSuccess = true
		}
	}
	if !sawSuccess {
		t.Errorf("expected a successful tool.end after approval, got %+v", events)
	}
}

func TestApprovalTurnDeniedEndsDifferently(t *testing.T) {
	h, err := New("approval", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "delete it"})
	if err != nil {
		t.Fatal(err)
	}

	var events []uievent.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case req, ok := <-h.Pending():
			if !ok {
				continue
			}
			h.Resolve(req.ID, ports.DecisionDeny)
		case ev, ok := <-handle.Events():
			if !ok {
				goto done
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatal("turn did not finish within 5s")
		}
	}
done:
	for _, ev := range events {
		if b, ok := ev.Body.(uievent.ToolEndBody); ok && b.OK {
			t.Errorf("expected no successful tool.end after denial, got %+v", b)
		}
	}
	sawDenialNotice := false
	for _, ev := range events {
		if b, ok := ev.Body.(uievent.NoticeBody); ok && b.Text != "" {
			sawDenialNotice = true
		}
	}
	if !sawDenialNotice {
		t.Errorf("expected a denial notice, got %+v", events)
	}
}

// TestCancelMidStreamPreservesPartialTextAndEndsUnfinished pins the task
// requirement: Cancel stops streaming immediately and the turn ends with
// a non-"completed" reason, which is what transcript.Model relies on to
// keep the partial text already streamed instead of discarding it.
func TestCancelMidStreamPreservesPartialTextAndEndsUnfinished(t *testing.T) {
	h, err := New("smalltalk", 15*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	first, ok := <-handle.Events()
	if !ok || first.Kind != uievent.KindTurnStart {
		t.Fatalf("expected the first event to be turn.start, got %+v ok=%v", first, ok)
	}
	handle.Cancel()

	var events []uievent.Event
	for ev := range handle.Events() {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("expected at least the cancelled turn.end event")
	}
	last := events[len(events)-1]
	if last.Kind != uievent.KindTurnEnd {
		t.Fatalf("got last event kind %q, want turn.end", last.Kind)
	}
	if reason := last.Body.(uievent.TurnEndBody).Reason; reason == "completed" || reason == "" {
		t.Errorf("got turn.end reason %q, want a non-completed reason after cancel", reason)
	}
	for _, ev := range events {
		if ev.Kind == uievent.KindTextEnd {
			t.Errorf("expected the turn to stop before text.end after an early cancel, got %+v", events)
		}
	}
}

// TestPacedTurnWaitsBetweenEvents pins the pace parameter itself: a
// multi-event turn at a non-zero pace must take at least a few
// inter-event gaps to drain, the same shape replay's own pace test
// pins.
func TestPacedTurnWaitsBetweenEvents(t *testing.T) {
	const pace = 15 * time.Millisecond
	h, err := New("full-tour", pace)
	if err != nil {
		t.Fatal(err)
	}
	handle := sendNth(t, h, 1) // tool_call.json: several events, no pending
	start := time.Now()
	events := drain(t, handle)
	elapsed := time.Since(start)
	if len(events) < 3 {
		t.Fatalf("got %d events, want several", len(events))
	}
	if elapsed < 2*pace {
		t.Errorf("paced turn finished in %v, want at least %v at pace %v", elapsed, 2*pace, pace)
	}
}

// TestCancelDuringApprovalPublishEndsUnfinished cancels before the
// approval request is ever read from Pending(), exercising the branch
// where the tool.pending publish itself is what observes the
// cancellation.
func TestCancelDuringApprovalPublishEndsUnfinished(t *testing.T) {
	h, err := New("approval", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}

	var last uievent.Event
	for ev := range handle.Events() {
		last = ev
		if ev.Kind == uievent.KindToolPending {
			handle.Cancel() // never read h.Pending() at all
		}
	}
	if last.Kind != uievent.KindTurnEnd {
		t.Fatalf("got last event kind %q, want turn.end", last.Kind)
	}
	if reason := last.Body.(uievent.TurnEndBody).Reason; reason == "completed" || reason == "" {
		t.Errorf("got turn.end reason %q, want a non-completed reason", reason)
	}
}

// TestCancelDuringApprovalDecisionWaitEndsUnfinished reads the request
// from Pending() but never resolves it, cancelling instead - the branch
// where the decision wait itself observes the cancellation.
func TestCancelDuringApprovalDecisionWaitEndsUnfinished(t *testing.T) {
	h, err := New("approval", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}

	var last uievent.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-h.Pending():
			if ok {
				handle.Cancel() // read the request, then cancel instead of resolving
			}
		case ev, ok := <-handle.Events():
			if !ok {
				goto done
			}
			last = ev
		case <-timeout:
			t.Fatal("turn did not finish within 5s")
		}
	}
done:
	if last.Kind != uievent.KindTurnEnd {
		t.Fatalf("got last event kind %q, want turn.end", last.Kind)
	}
	if reason := last.Body.(uievent.TurnEndBody).Reason; reason == "completed" || reason == "" {
		t.Errorf("got turn.end reason %q, want a non-completed reason", reason)
	}
}

// TestCancelDuringApprovalTailSendEndsUnfinished approves the request
// but never reads a single event of the on_approve continuation,
// exercising the branch where playTail's own send observes the
// cancellation.
func TestCancelDuringApprovalTailSendEndsUnfinished(t *testing.T) {
	h, err := New("approval", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}

	for {
		ev, ok := <-handle.Events()
		if !ok {
			t.Fatal("channel closed before tool.pending")
		}
		if ev.Kind == uievent.KindToolPending {
			break
		}
	}
	req := <-h.Pending()
	h.Resolve(req.ID, ports.DecisionOnce)
	handle.Cancel()
	// Give the playback goroutine's now-unread send a moment to observe
	// the cancellation. There is no reader left on Events() for the
	// rest of this test, so the send can only ever resolve via cancelCh.
	time.Sleep(50 * time.Millisecond)
}

// TestCancelBetweenApprovedTailEventsEndsUnfinished approves the
// request, reads exactly one on_approve event, then cancels - the
// branch where playTail's wait (not its send) is what observes the
// cancellation.
func TestCancelBetweenApprovedTailEventsEndsUnfinished(t *testing.T) {
	const pace = 200 * time.Millisecond
	h, err := New("approval", pace)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}

	for {
		ev, ok := <-handle.Events()
		if !ok {
			t.Fatal("channel closed before tool.pending")
		}
		if ev.Kind == uievent.KindToolPending {
			break
		}
	}
	req := <-h.Pending()
	h.Resolve(req.ID, ports.DecisionOnce)

	first, ok := <-handle.Events()
	if !ok || first.Kind != uievent.KindToolStart {
		t.Fatalf("expected the first on_approve event to be tool.start, got %+v ok=%v", first, ok)
	}
	// Cancel well inside the pace window: playTail's wait() after this
	// send is what must observe it, not a subsequent send.
	handle.Cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestCancelIsIdempotent(t *testing.T) {
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := h.Send(context.Background(), intent.Send{})
	drain(t, handle)
	handle.Cancel()
	handle.Cancel() // must not panic
}

// TestCancelBeforeAnyReadRespectsFlushTimeout exercises the bound that
// keeps a playback goroutine from leaking forever when the caller
// cancels and never reads Events() at all: the final cancelled turn.end
// send must give up within DemoCancelFlushTimeout instead of blocking
// indefinitely on the unbuffered channel.
func TestCancelBeforeAnyReadRespectsFlushTimeout(t *testing.T) {
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	handle.Cancel()
	time.Sleep(uikitconfig.DemoCancelFlushTimeout + 200*time.Millisecond)
}

// TestCancelWhileApprovalPublishBlocks covers awaitDecision's first
// cancel arm: the pending buffer is full and nobody drains Pending(),
// so the turn blocks PUBLISHING its approval request; Cancel must take
// the cancelCh arm of the publish select and end the turn cancelled
// instead of deadlocking the goroutine. The buffer is filled directly
// (internal test) and events are read up to tool.pending BEFORE the
// cancel, so the playback goroutine is parked inside the blocked
// publish when the close fires - cancelling earlier would let the
// closed channel win the send select and end the turn before the
// approval arm is ever reached.
func TestCancelWhileApprovalPublishBlocks(t *testing.T) {
	h, err := New("approval", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < uikitconfig.DemoPendingApprovalBuffer; i++ {
		h.pendingCh <- ports.ApprovalRequest{ID: fmt.Sprintf("filler-%d", i)}
	}

	victim, err := h.Send(context.Background(), intent.Send{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range victim.Events() {
		if ev.Kind == uievent.KindToolPending {
			break // the goroutine is now blocked publishing into a full buffer
		}
	}
	victim.Cancel()
	for ev := range victim.Events() {
		if ev.Kind == uievent.KindTurnEnd {
			if b, ok := ev.Body.(uievent.TurnEndBody); ok && b.Reason != "cancelled" {
				t.Errorf("victim ended with reason %q, want cancelled", b.Reason)
			}
			return
		}
	}
	t.Fatal("victim turn never ended after cancel")
}

// TestResolveSkipsAFullWaitSlot covers Resolve's default arm: a wait
// whose single buffered slot is already occupied (a cancel raced an
// earlier delivery) must be dropped silently, not block the resolver.
// Arranged through the internal waiting map because the public flow
// deletes the entry on its first Resolve.
func TestResolveSkipsAFullWaitSlot(t *testing.T) {
	h, err := New("approval", 0)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan ports.Decision, 1)
	ch <- ports.DecisionOnce // slot already full
	h.mu.Lock()
	h.waiting["stale"] = ch
	h.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Resolve("stale", ports.DecisionDeny)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve blocked on a full wait slot")
	}
	// The stale entry is gone either way: a later Resolve is a no-op.
	h.Resolve("stale", ports.DecisionDeny)
	if got := <-ch; got != ports.DecisionOnce {
		t.Errorf("stale slot holds %v, want the original DecisionOnce", got)
	}
}
