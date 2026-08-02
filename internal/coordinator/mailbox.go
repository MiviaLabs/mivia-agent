package coordinator

import (
	"fmt"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
)

// taskMailbox is a bounded, never-closed channel of parent→child messages.
// terminal rejects new sends without close() so retry attempts can reseed.
type taskMailbox struct {
	ch       chan agentmsg.Message
	terminal bool
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
		mb = &taskMailbox{ch: make(chan agentmsg.Message, m.capacity)}
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
		return nil
	default:
		return fmt.Errorf("mailbox full for task %q (capacity %d)", taskID, m.capacity)
	}
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

// MailboxSend enqueues an already-persisted message to a task mailbox without
// re-writing the ledger (plan 53.04 ask delivery after PostTaskMessage).
func (c *coordinator) MailboxSend(h *RunHandle, taskID string, msg agentmsg.Message) (delivered bool, err error) {
	if h == nil || h.mailboxes == nil {
		return false, nil
	}
	if err := h.mailboxes.Send(taskID, msg); err != nil {
		return false, nil
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
	mb := &taskMailbox{ch: make(chan agentmsg.Message, m.capacity)}
	for _, msg := range pending {
		select {
		case mb.ch <- msg:
		default:
			// Drop if over capacity; durable ledger still has them.
		}
	}
	m.byTask[taskID] = mb
}
