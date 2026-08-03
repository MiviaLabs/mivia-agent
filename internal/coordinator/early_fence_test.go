package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// earlyFenceFixture bundles the REAL pool wiring for the early-fence test: a
// live dispatcher (Workers=2) with a responder whose handler returns without
// answering and an asker that parks until released. The run is kept alive by
// the parked asker so the responder's mid-run terminal status stays observable
// before the pool finishes.
type earlyFenceFixture struct {
	repo             ledger.LedgerRepository
	c                Coordinator
	h                *RunHandle
	runID            string
	responderStarted chan struct{}
	askDelivered     chan struct{}
	releaseAsker     chan struct{}
}

// buildEarlyFenceFixture wires a REAL coordinator + subagent pool (Workers=2)
// exactly like the production path: the responder handler returns promptly once
// the ask is in flight (and never answers it), while the asker blocks until
// released. Returns the live handle plus the channels that gate the run.
func buildEarlyFenceFixture(t *testing.T) earlyFenceFixture {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})

	responderStarted := make(chan struct{})
	askDelivered := make(chan struct{})
	_ = d.Register(runtime.Subagent, "responder", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(responderStarted)
		// Returns promptly once the ask is in flight — and never answers it.
		select {
		case <-askDelivered:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))

	releaseAsker := make(chan struct{})
	_ = d.Register(runtime.Subagent, "asker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		// Blocks until the test releases it, keeping the run alive after the
		// responder's handler has returned (mid-run assertion window).
		select {
		case <-releaseAsker:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))

	// Workers=2: responder and asker run concurrently, so the responder's
	// handler-done moment happens while the asker is still executing.
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "responder", Name: "responder", Timeout: 30 * time.Second},
		{ID: "asker", Name: "asker", Timeout: 30 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Cancel(context.Background(), h)
		_, _ = c.Join(context.Background(), h)
	})
	return earlyFenceFixture{
		repo:             repo,
		c:                c,
		h:                h,
		runID:            h.runID,
		responderStarted: responderStarted,
		askDelivered:     askDelivered,
		releaseAsker:     releaseAsker,
	}
}

// parkAndDeliverAsk mirrors deliverAskTo/deliverAskTracked (ask_decline_test.go):
// park the asker, register the ask, and mailbox-deliver it to the responder via
// the coordinator path. LONG wait semantics (30s) — the assertion bound in
// waitForDeclineSentinel is 2s, far below wait_seconds, so only the early fence
// can unblock it.
func parkAndDeliverAsk(t *testing.T, c Coordinator, h *RunHandle, runID string) (<-chan string, agentmsg.Message) {
	t.Helper()
	ask, err := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: "asker", Role: "asker"},
		agentmsg.Party{Role: "responder"},
		"please verify", nil, agentmsg.Options{ID: "ask-early-fence"})
	if err != nil {
		t.Fatal(err)
	}
	answerCh, unpark, err := c.ParkQuestion(runID, "asker", ask.ID, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unpark)
	if !c.TryRegisterAsk(runID, "asker", "asker", ask.ID, nil, 4) {
		t.Fatal("register ask")
	}
	delivered, err := c.MailboxSend(h, "responder", ask)
	if err != nil || !delivered {
		t.Fatalf("deliver ask to responder: delivered=%v err=%v", delivered, err)
	}
	return answerCh, ask
}

// waitForDeclineSentinel asserts the parked asker receives the responder-
// terminal decline within 2s — far below the asker's 30s wait semantics, so
// only the early fence can unblock it.
func waitForDeclineSentinel(t *testing.T, answerCh <-chan string) {
	t.Helper()
	want := agentmsg.AskDeclinePrefix + agentmsg.DeclineReasonResponderTerminal
	select {
	case got := <-answerCh:
		if got != want {
			t.Fatalf("answer = %q, want decline %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked asker not unblocked within 2s: decline never arrived (early fence missing)")
	}
}

// assertMidRunTerminalStatus checks the responder's ledger status is ALREADY
// terminal while the run is still executing (the asker has not been released).
func assertMidRunTerminalStatus(t *testing.T, repo ledger.LedgerRepository, runID string) {
	t.Helper()
	snap, err := repo.GetTask(context.Background(), runID, "responder")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("responder status mid-run = %q, want completed (early CAS visible before pool finish)", snap.Status)
	}
}

// assertSingleTerminalEvent lets the run finish, then counts terminal events
// for the responder: the early fence appends no events, and recordRunResults
// must not double-append or error on the already-terminal task.
func assertSingleTerminalEvent(t *testing.T, repo ledger.LedgerRepository, c Coordinator, h *RunHandle, runID string) {
	t.Helper()
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, evt := range events {
		if evt.Kind == "task_completed" && evt.TaskID == "responder" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("responder task_completed events = %d, want exactly 1 (no double-append)", completed)
	}
}

// TestEarlyFenceDeclinesAskWhenResponderHandlerDone pins R9 with a REAL pool
// timing test: a task whose handler returns early must be finalized (ledger
// status CAS + mailbox fence + ask decline) from the pool worker the moment
// its handler returns, NOT when the whole pool finishes.
//
// FAILS without the fix: the decline only fires in recordRunResults, which
// runs after the pool's wg.Wait — and the asker's task is still executing, so
// the pool never drains and the parked asker burns its full wait_seconds
// (live repro: 30s park, responder finished in 880ms, asker got
// no_answer/timed_out). Here the asker's park has 30s wait semantics, so the
// 2s assertion bound proves the early fence unblocked it.
func TestEarlyFenceDeclinesAskWhenResponderHandlerDone(t *testing.T) {
	fx := buildEarlyFenceFixture(t)
	<-fx.responderStarted

	answerCh, ask := parkAndDeliverAsk(t, fx.c, fx.h, fx.runID)
	close(fx.askDelivered) // responder's handler returns now, without answering.

	waitForDeclineSentinel(t, answerCh)
	if !fx.c.IsAskAnswered(ask.ID) {
		t.Fatal("declined ask must be sealed")
	}

	assertMidRunTerminalStatus(t, fx.repo, fx.runID)
	close(fx.releaseAsker)
	assertSingleTerminalEvent(t, fx.repo, fx.c, fx.h, fx.runID)
}
