package coordinator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// parkAskForTerminalRace builds, registers and parks an ask destined for
// responderTask without sending it: the caller pins the MailboxSend itself so
// the send-between-record window is exercised deterministically. Returns the
// built ask, the parked answer channel and the unpark handle.
func parkAskForTerminalRace(t *testing.T, c Coordinator, runID, askerTask, responderTask, askID string) (agentmsg.Message, <-chan string, func()) {
	t.Helper()
	ask, err := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: askerTask, Role: "asker"},
		agentmsg.Party{Role: "responder"},
		"please verify", nil, agentmsg.Options{ID: askID})
	if err != nil {
		t.Fatal(err)
	}
	if !c.TryRegisterAsk(runID, askerTask, "asker", askID, nil, 4) {
		t.Fatal("register ask")
	}
	answerCh, unpark, err := c.ParkQuestion(runID, askerTask, askID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unpark)
	return ask, answerCh, unpark
}

// waitForMailboxHeld blocking-receives the ask off the responder's mailbox
// channel: the receive completes only after the MailboxSend Send wrote the
// message, and because the caller holds asks.mu the send goroutine is then
// guaranteed blocked in recordAskTarget on the held lock — the missed-decline
// window is pinned deterministically without polling.
func waitForMailboxHeld(t *testing.T, h *RunHandle, responderTask string) {
	t.Helper()
	h.mailboxes.mu.Lock()
	mb := h.mailboxes.getOrCreate(responderTask)
	h.mailboxes.mu.Unlock()
	select {
	case <-mb.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox send did not land while asks.mu was held")
	}
}

// waitForMailboxTerminal blocks until the fence goroutine has marked the
// responder's mailbox terminal. The fence closes marked right after
// Drain+MarkTerminal and before declineAsksForTerminalTask (which parks on the
// held asks.mu), so returning here guarantees the caller's release lands the
// byTarget record on an already-terminal mailbox.
func waitForMailboxTerminal(t *testing.T, marked chan struct{}) {
	t.Helper()
	select {
	case <-marked:
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox never became terminal")
	}
}

// assertDeclinedAskUnblocksAsker asserts a parked asker received exactly the
// terminal decline sentinel, the ask is sealed, and byTarget is pruned.
func assertDeclinedAskUnblocksAsker(t *testing.T, c Coordinator, coord *coordinator, runID, askID, responderTask string, answerCh <-chan string) {
	t.Helper()
	want := agentmsg.AskDeclinePrefix + agentmsg.DeclineReasonResponderTerminal
	select {
	case got := <-answerCh:
		if got != want {
			t.Fatalf("answer = %q, want decline %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked asker not unblocked by the terminal-window decline")
	}
	if !c.IsAskAnswered(askID) {
		t.Fatal("declined ask must be sealed")
	}
	if got := coord.asksTargeting(runID, responderTask); len(got) != 0 {
		t.Fatalf("asksTargeting = %v, want empty", got)
	}
}

// registerParkRacingAsk builds, registers (no quota) and parks one racing-race
// ask, returning the message for MailboxSend plus the park channel and unpark.
func registerParkRacingAsk(t *testing.T, c Coordinator, runID, askerTask, askID string) (agentmsg.Message, <-chan string, func()) {
	t.Helper()
	ask, err := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: askerTask, Role: "asker"},
		agentmsg.Party{Role: "responder"},
		"please verify", nil, agentmsg.Options{ID: askID})
	if err != nil {
		t.Fatal(err)
	}
	c.RegisterAsk(runID, askerTask, "asker", askID, nil)
	answerCh, unpark, err := c.ParkQuestion(runID, askerTask, askID)
	if err != nil {
		t.Fatal(err)
	}
	return ask, answerCh, unpark
}

// runTaskToTerminalAsync transitions rt through running to completed and then
// marks its mailbox terminal, racing the caller's MailboxSend. The first
// transition error (or nil) is reported on the returned channel.
func runTaskToTerminalAsync(ctx context.Context, repo ledger.LedgerRepository, h *RunHandle, runID, rt string) <-chan error {
	transErr := make(chan error, 1)
	go func() {
		err := func() error {
			for _, status := range []string{string(ledger.TaskStatusRunning), string(ledger.TaskStatusCompleted)} {
				snap, err := repo.GetTask(ctx, runID, rt)
				if err != nil {
					return err
				}
				if err := repo.CompareAndSetTaskStatus(ctx, runID, rt, snap.Version, status); err != nil {
					return err
				}
			}
			return nil
		}()
		if err == nil {
			h.MarkTaskMailboxTerminal(rt)
		}
		transErr <- err
	}()
	return transErr
}

// assertRacingDeclineOutcome verifies one racing-terminal iteration's outcome:
// a delivered ask must unblock the parked asker with the decline sentinel
// (sealed), an undelivered ask (already closed by the caller) must not unblock
// at all, and byTarget must be pruned either way. unpark is retired before any
// failure so the park registry never leaks from a failed iteration.
func assertRacingDeclineOutcome(t *testing.T, c Coordinator, coord *coordinator, runID, responderTask, askID string, answerCh <-chan string, unpark func(), delivered bool) {
	t.Helper()
	want := agentmsg.AskDeclinePrefix + agentmsg.DeclineReasonResponderTerminal
	if delivered {
		select {
		case got := <-answerCh:
			if got != want {
				unpark()
				t.Fatalf("answer = %q, want decline %q", got, want)
			}
		case <-time.After(time.Second):
			unpark()
			t.Fatalf("asker not unblocked after terminal race; answered=%v", c.IsAskAnswered(askID))
		}
	} else {
		select {
		case got := <-answerCh:
			unpark()
			t.Fatalf("undelivered ask unblocked the asker with %q", got)
		default:
		}
	}
	if got := coord.asksTargeting(runID, responderTask); len(got) != 0 {
		unpark()
		t.Fatalf("byTarget retains %v", got)
	}
}

// TestDeclineAskWhenMailboxAlreadyTerminal pins the missed-decline fix (window
// B) at the component level: an ask whose byTarget record lands after the
// finalize fence ran (mailbox already terminal) is declined at delivery time so
// the parked asker is unblocked promptly instead of waiting out wait_seconds.
func TestDeclineAskWhenMailboxAlreadyTerminal(t *testing.T) {
	c, repo, h, runID := newAskDeclineFixture(t)
	createTestTask(t, repo, runID, "asker", "asker", string(ledger.TaskStatusAwaitingInput))
	createTestTask(t, repo, runID, "responder", "responder", string(ledger.TaskStatusQueued))
	coord := c.(*coordinator)
	askerTask, responderTask := "asker", "responder"

	askID := "ask-terminal-window"
	ask, err := agentmsg.NewMessage(runID, agentmsg.KindAsk,
		agentmsg.Party{TaskID: askerTask, Role: "asker"},
		agentmsg.Party{Role: "responder"},
		"please verify", nil, agentmsg.Options{ID: askID})
	if err != nil {
		t.Fatal(err)
	}
	if !c.TryRegisterAsk(runID, askerTask, "asker", askID, nil, 4) {
		t.Fatal("register ask")
	}
	answerCh, unpark, err := c.ParkQuestion(runID, askerTask, askID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unpark)

	// 1. The mailbox send happened while the responder was live.
	if err := h.mailboxes.Send(responderTask, ask); err != nil {
		t.Fatal(err)
	}
	// 2. The responder finalizes: the fence runs against an EMPTY byTarget (the
	//    record has not landed) → the ask is missed by the fence.
	completeQueuedTask(t, repo, runID, responderTask)
	h.MarkTaskMailboxTerminal(responderTask)
	select {
	case got := <-answerCh:
		t.Fatalf("fence before record must not decline (got %q)", got)
	default:
	}
	// 3. The MailboxSend post-send bookkeeping lands: record + isTerminal →
	//    the single-ask decline path unblocks the asker at delivery time.
	coord.recordAskTarget(h.runID, responderTask, askID)
	if !h.mailboxes.isTerminal(responderTask) {
		t.Fatal("mailbox must be terminal")
	}
	coord.declineAskDeliveredToTerminal(h.runID, responderTask, askID)

	want := agentmsg.AskDeclinePrefix + agentmsg.DeclineReasonResponderTerminal
	select {
	case got := <-answerCh:
		if got != want {
			t.Fatalf("answer = %q, want decline %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked asker not unblocked by the terminal-window decline")
	}
	if !c.IsAskAnswered(askID) {
		t.Fatal("declined ask must be sealed")
	}
	if got := coord.asksTargeting(runID, responderTask); len(got) != 0 {
		t.Fatalf("asksTargeting = %v, want empty", got)
	}
}

// TestMailboxSendDeclinesAskDeliveredToTerminalMailbox is a deterministic
// MailboxSend-level test of window B: the mailbox send is pinned between the
// Send and the byTarget record (asks.mu held), the responder is fenced to
// terminal, and only then is the record allowed to land. Whatever ordering the
// release produces, the parked asker must be unblocked with the decline
// sentinel promptly — never left to wait out wait_seconds.
func TestMailboxSendDeclinesAskDeliveredToTerminalMailbox(t *testing.T) {
	c, repo, h, runID := newAskDeclineFixture(t)
	createTestTask(t, repo, runID, "asker", "asker", string(ledger.TaskStatusAwaitingInput))
	createTestTask(t, repo, runID, "responder", "responder", string(ledger.TaskStatusQueued))
	coord := c.(*coordinator)
	askerTask, responderTask := "asker", "responder"

	askID := "ask-terminal-pinned"
	ask, answerCh, _ := parkAskForTerminalRace(t, c, runID, askerTask, responderTask, askID)

	// Responder's LEDGER status is terminal before the fence so the decline
	// gates pass when they run.
	completeQueuedTask(t, repo, runID, responderTask)

	// Hold asks.mu: MailboxSend's Send succeeds, then the call blocks inside
	// recordAskTarget — pinning the missed-decline window exactly.
	coord.asks.mu.Lock()
	mailboxDone := make(chan struct{})
	go func() {
		defer close(mailboxDone)
		_, _ = c.MailboxSend(h, responderTask, ask)
	}()
	waitForMailboxHeld(t, h, responderTask)

	// Fence the responder while the record is still pending: Drain+MarkTerminal
	// complete, then declineAsksForTerminalTask blocks on asks.mu (held). The
	// fence replicates MarkTaskMailboxTerminal's three steps so the
	// terminalMarked signal can fire between the mark and the decline step — the
	// composite call cannot signal there because the decline step parks on the
	// held asks.mu. Waiting on terminalMarked guarantees the release below lands
	// the byTarget record on an already-terminal mailbox, pinning the
	// missed-decline window (window B) deterministically.
	fenceDone := make(chan struct{})
	terminalMarked := make(chan struct{})
	go func() {
		defer close(fenceDone)
		h.mailboxes.Drain(responderTask)
		h.mailboxes.MarkTerminal(responderTask)
		close(terminalMarked)
		if h.owner != nil {
			h.owner.declineAsksForTerminalTask(h.runID, responderTask)
		}
	}()
	waitForMailboxTerminal(t, terminalMarked)

	// Release: the record lands, isTerminal is true → the single-ask decline
	// fires (or the unblocked fence wins — both are idempotent; exactly one
	// sentinel reaches the park).
	coord.asks.mu.Unlock()
	<-mailboxDone
	<-fenceDone

	assertDeclinedAskUnblocksAsker(t, c, coord, runID, askID, responderTask, answerCh)
}

// TestMailboxSendDeclinesAskRacingTerminal stress-drives MailboxSend against a
// responder that concurrently reaches terminal: whatever interleaving wins, a
// delivered ask must unblock its parked asker with the decline sentinel promptly.
func TestMailboxSendDeclinesAskRacingTerminal(t *testing.T) {
	c, repo, h, runID := newAskDeclineFixture(t)
	createTestTask(t, repo, runID, "asker", "asker", string(ledger.TaskStatusAwaitingInput))
	coord := c.(*coordinator)
	askerTask := "asker"
	ctx := context.Background()

	const n = 20
	for i := 0; i < n; i++ {
		responderTask := fmt.Sprintf("responder-%d", i)
		createTestTask(t, repo, runID, responderTask, "responder", string(ledger.TaskStatusQueued))
		askID := fmt.Sprintf("ask-terminal-race-%d", i)
		ask, answerCh, unpark := registerParkRacingAsk(t, c, runID, askerTask, askID)

		transErr := runTaskToTerminalAsync(ctx, repo, h, runID, responderTask)

		delivered, err := c.MailboxSend(h, responderTask, ask)
		if err != nil {
			unpark()
			t.Fatalf("iter %d: MailboxSend err: %v", i, err)
		}
		if terr := <-transErr; terr != nil {
			unpark()
			t.Fatalf("iter %d: transition: %v", i, terr)
		}
		if !delivered {
			// Send was rejected by the terminal fence: the CLI closes the ask
			// and reports decline directly. The parked asker must NOT be
			// unblocked by a late sentinel.
			c.CloseAsk(askID)
		}
		assertRacingDeclineOutcome(t, c, coord, runID, responderTask, askID, answerCh, unpark, delivered)
		unpark()
	}
}
