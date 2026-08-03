package coordinator

import (
	"fmt"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
)

// taskMailbox is a bounded, never-closed channel of parent→child messages.
// terminal rejects new sends without close() so retry attempts can reseed.
type taskMailbox struct {
	ch          chan agentmsg.Message
	interruptCh chan struct{} // buffered 1; signaled after an Interrupt steer is enqueued
	terminal    bool
}

// runMailboxes holds per-task mailboxes for one run, guarded by mu.
type runMailboxes struct {
	mu       sync.Mutex
	byTask   map[string]*taskMailbox
	capacity int
}

func newRunMailboxes(capacity int) *runMailboxes {
	if capacity <= 0 {
		capacity = 32
	}
	return &runMailboxes{byTask: map[string]*taskMailbox{}, capacity: capacity}
}

func (m *runMailboxes) getOrCreate(taskID string) *taskMailbox {
	mb, ok := m.byTask[taskID]
	if !ok {
		mb = &taskMailbox{
			ch:          make(chan agentmsg.Message, m.capacity),
			interruptCh: make(chan struct{}, 1),
		}
		m.byTask[taskID] = mb
	}
	return mb
}

// Send enqueues a message. Fails if the task is terminal or the mailbox is full.
func (m *runMailboxes) Send(taskID string, msg agentmsg.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mb := m.getOrCreate(taskID)
	if mb.terminal {
		return fmt.Errorf("task %q is terminal; message not delivered", taskID)
	}
	select {
	case mb.ch <- msg:
		// Signal after enqueue (still holding mu) so the task can wake for a
		// mid-step interrupt steer. Non-blocking: a pending signal suffices.
		if msg.Interrupt {
			select {
			case mb.interruptCh <- struct{}{}:
			default:
			}
		}
		return nil
	default:
		return fmt.Errorf("mailbox full for task %q (capacity %d)", taskID, m.capacity)
	}
}

// InterruptCh returns the task's interrupt signal channel, creating the
// mailbox if needed. A value is readable after an Interrupt steer is enqueued.
func (m *runMailboxes) InterruptCh(taskID string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getOrCreate(taskID).interruptCh
}

// Pending reports whether the task has queued messages (false if absent).
func (m *runMailboxes) Pending(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	mb, ok := m.byTask[taskID]
	return ok && len(mb.ch) > 0
}

// PendingInterrupt reports whether the task has at least one Interrupt-flagged
// steer queued (false if absent). It is the distinct 'interrupt pending' gate
// for the loop watcher's signal branch (plan 54 Step 5): a stale interrupt
// signal paired with a later NON-interrupt message must not count, so a
// len()-based check is not enough — the queued messages themselves are
// scanned.
//
// Channels cannot be peeked, so the scan drains the buffer into a slice and
// re-enqueues in order. The mailbox is bounded (cap 32) and this runs only on
// signal/tick events, so the copy is cheap. Send is the only writer and holds
// m.mu; Drain reads the mailbox under m.mu but receives without it, so the
// receives here are non-blocking to guarantee the lock is never held across a
// blocking receive. Messages a concurrent Drain steals are simply not part of
// the re-enqueue; none are lost or reordered.
func (m *runMailboxes) PendingInterrupt(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	mb, ok := m.byTask[taskID]
	if !ok || len(mb.ch) == 0 {
		return false
	}
	msgs := make([]agentmsg.Message, 0, len(mb.ch))
scan:
	for {
		select {
		case msg := <-mb.ch:
			msgs = append(msgs, msg)
		default:
			break scan
		}
	}
	found := false
	for _, msg := range msgs {
		if msg.Interrupt {
			found = true
		}
	}
	for _, msg := range msgs {
		mb.ch <- msg
	}
	return found
}

// Drain non-blocking removes all pending messages in order.
func (m *runMailboxes) Drain(taskID string) []agentmsg.Message {
	m.mu.Lock()
	mb := m.byTask[taskID]
	m.mu.Unlock()
	if mb == nil {
		return nil
	}
	var out []agentmsg.Message
	for {
		select {
		case msg := <-mb.ch:
			out = append(out, msg)
		default:
			return out
		}
	}
}

// MarkTerminal sets the terminal flag without closing the channel.
func (m *runMailboxes) MarkTerminal(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mb := m.getOrCreate(taskID)
	mb.terminal = true
}

// isTerminal reports whether the task's mailbox has been marked terminal.
func (m *runMailboxes) isTerminal(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	mb, ok := m.byTask[taskID]
	return ok && mb.terminal
}

// MailboxSend enqueues an already-persisted message to a task mailbox without
// re-writing the ledger (plan 53.04 ask delivery after PostTaskMessage).
// When a KindAsk message is successfully delivered, the target task is
// recorded in the ask registry so finalize can decline the ask if the target
// reaches terminal status without answering.
func (c *coordinator) MailboxSend(h *RunHandle, taskID string, msg agentmsg.Message) (delivered bool, err error) {
	if h == nil || h.mailboxes == nil {
		return false, nil
	}
	if err := h.mailboxes.Send(taskID, msg); err != nil {
		return false, nil
	}
	if msg.Kind == agentmsg.KindAsk {
		c.recordAskTarget(h.runID, taskID, msg.ID)
		// The mailbox send and the byTarget record are not atomic: if the task
		// went terminal in between, the finalize fence (declineAsksForTerminalTask)
		// already ran against an empty byTarget and skipped this ask. Decline it
		// now so the parked asker is unblocked at delivery time instead of waiting
		// out the full wait_seconds. Idempotent: a sealed/claimed ask no-ops.
		if h.mailboxes.isTerminal(taskID) {
			c.declineAskDeliveredToTerminal(h.runID, taskID, msg.ID)
		}
	}
	return true, nil
}

// Reseed replaces the mailbox for a retry attempt, draining undelivered first.
func (m *runMailboxes) Reseed(taskID string, pending []agentmsg.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Drain any leftover.
	if old, ok := m.byTask[taskID]; ok {
		for {
			select {
			case <-old.ch:
			default:
				goto reseed
			}
		}
	}
reseed:
	mb := &taskMailbox{
		ch:          make(chan agentmsg.Message, m.capacity),
		interruptCh: make(chan struct{}, 1),
	}
	for _, msg := range pending {
		select {
		case mb.ch <- msg:
		default:
			// Drop if over capacity; durable ledger still has them.
		}
	}
	m.byTask[taskID] = mb
}
