package coordinator

// PendingInterrupt cannot peek a channel, so it drains the mailbox and puts
// every message back. It holds m.mu across that window. Drain releases m.mu
// before its own receive loop, so a Drain landing inside the window sees an
// empty channel and reports that the parent sent nothing.
//
// The interrupt poller runs this scan four times a second for a task's whole
// life, and Drain runs at every step boundary. Normally the steer just slips a
// step. If the boundary it slips is the last one, the child finishes and the
// mailbox is discarded with the steer still in it - while the parent was
// already told the message was delivered.

import (
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
)

func TestDrainSeesQueuedMessageDuringPendingInterruptScan(t *testing.T) {
	m := newRunMailboxes(8)

	// The production poller scans every 250ms for the task's whole life; this
	// one scans continuously so the window is hit in test time rather than in
	// a user's session.
	stop := make(chan struct{})
	var scanner sync.WaitGroup
	scanner.Add(1)
	go func() {
		defer scanner.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.PendingInterrupt("task-1")
			}
		}
	}()
	defer func() {
		close(stop)
		scanner.Wait()
	}()

	const iterations = 20000
	for i := 0; i < iterations; i++ {
		if err := m.Send("task-1", agentmsg.Message{
			ID: "msg-1", Kind: agentmsg.KindSteer, Body: "stop and reconsider", Interrupt: true,
		}); err != nil {
			t.Fatalf("seed send failed: %v", err)
		}
		if len(m.Drain("task-1")) == 0 {
			// Prove the message was really queued the whole time: it is still
			// there once the scan puts it back.
			recovered := m.Drain("task-1")
			t.Fatalf("iteration %d: Drain reported an empty mailbox while a steer was queued "+
				"(%d message(s) recovered immediately afterwards)", i, len(recovered))
		}
	}
}

// The guard the fix must preserve: PendingInterrupt still answers correctly
// and still leaves every message in the mailbox, in order.
func TestPendingInterruptPreservesQueueContents(t *testing.T) {
	m := newRunMailboxes(8)
	seed := []agentmsg.Message{
		{ID: "a", Kind: agentmsg.KindSteer, Body: "first"},
		{ID: "b", Kind: agentmsg.KindSteer, Body: "second", Interrupt: true},
		{ID: "c", Kind: agentmsg.KindSteer, Body: "third"},
	}
	for _, msg := range seed {
		if err := m.Send("task-1", msg); err != nil {
			t.Fatalf("send %s: %v", msg.ID, err)
		}
	}
	if !m.PendingInterrupt("task-1") {
		t.Fatal("an interrupt steer is queued; PendingInterrupt must report it")
	}
	drained := m.Drain("task-1")
	if len(drained) != len(seed) {
		t.Fatalf("drained %d messages, want %d", len(drained), len(seed))
	}
	for i, msg := range drained {
		if msg.ID != seed[i].ID {
			t.Fatalf("message %d = %q, want %q (order must survive the scan)", i, msg.ID, seed[i].ID)
		}
	}
}
