package coordinator

import (
	"context"
	"fmt"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// pendingQuestion tracks a parked child waiting for an answer.
type pendingQuestion struct {
	messageID string
	// answers is closed after the first deliver; capacity 1 for non-blocking send.
	answers chan string
}

// questionRegistry keys parked questions by runID/taskID.
type questionRegistry struct {
	mu    sync.Mutex
	byKey map[string]*pendingQuestion
}

func questionKey(runID, taskID string) string { return runID + "\x00" + taskID }

// ParkQuestion registers a pending question and returns its answer channel.
// unpark must be called when the wait ends (answer, timeout, or cancel).
func (c *coordinator) ParkQuestion(runID, taskID, messageID string) (<-chan string, func(), error) {
	q := &pendingQuestion{messageID: messageID, answers: make(chan string, 1)}
	key := questionKey(runID, taskID)
	c.questions.mu.Lock()
	defer c.questions.mu.Unlock()
	if _, exists := c.questions.byKey[key]; exists {
		return nil, nil, fmt.Errorf("task already has a pending question")
	}
	c.questions.byKey[key] = q
	unpark := func() {
		c.questions.mu.Lock()
		delete(c.questions.byKey, key)
		c.questions.mu.Unlock()
	}
	return q.answers, unpark, nil
}

// DeliverAnswer unblocks a parked question for the given task when inReplyTo
// matches the parked message ID (empty inReplyTo matches any - callers that
// care must pass the question id). Returns false when no matching park exists
// (caller may degrade to steer).
func (c *coordinator) DeliverAnswer(runID, taskID, inReplyTo, body string) bool {
	key := questionKey(runID, taskID)
	c.questions.mu.Lock()
	q := c.questions.byKey[key]
	c.questions.mu.Unlock()
	if q == nil {
		return false
	}
	if inReplyTo != "" && q.messageID != "" && inReplyTo != q.messageID {
		// Answer targets a different question; do not steal the live park.
		return false
	}
	select {
	case q.answers <- body:
		return true
	default:
		// Already answered or buffer full.
		return false
	}
}

// TransitionToAwaitingInput CAS-es running → awaiting_input for a parked question.
func (c *coordinator) TransitionToAwaitingInput(ctx context.Context, runID, taskID string) error {
	snap, err := c.repo.GetTask(ctx, runID, taskID)
	if err != nil {
		return err
	}
	if snap.Status == string(ledger.TaskStatusAwaitingInput) {
		return nil
	}
	if snap.Status != string(ledger.TaskStatusRunning) {
		return fmt.Errorf("cannot park task in status %q", snap.Status)
	}
	return c.repo.CompareAndSetTaskStatus(ctx, runID, taskID, snap.Version, string(ledger.TaskStatusAwaitingInput))
}

// TransitionFromAwaitingInput CAS-es awaiting_input → newStatus (usually running).
// Returns ErrConflict when another path (cancel) won the race.
func (c *coordinator) TransitionFromAwaitingInput(ctx context.Context, runID, taskID, newStatus string) error {
	snap, err := c.repo.GetTask(ctx, runID, taskID)
	if err != nil {
		return err
	}
	if snap.Status != string(ledger.TaskStatusAwaitingInput) {
		// Already left awaiting_input (cancel won, or double-unpark).
		if snap.Status == newStatus {
			return nil
		}
		return ledger.ErrConflict
	}
	return c.repo.CompareAndSetTaskStatus(ctx, runID, taskID, snap.Version, newStatus)
}

// MessageQuota tracks per-task upstream message counts for budget enforcement.
type messageQuota struct {
	mu    sync.Mutex
	count map[string]int // runID\0taskID → count
}

// ConsumeMessageQuota increments the per-task upstream message count and
// fails when max is exceeded. max <= 0 means unlimited.
func (c *coordinator) ConsumeMessageQuota(runID, taskID string, max int) error {
	if max <= 0 {
		return nil // unlimited
	}
	key := questionKey(runID, taskID)
	c.msgQuota.mu.Lock()
	defer c.msgQuota.mu.Unlock()
	n := c.msgQuota.count[key]
	if n >= max {
		return fmt.Errorf("max_messages_per_task (%d) exceeded", max)
	}
	c.msgQuota.count[key] = n + 1
	return nil
}

// CountPendingQuestions returns how many questions are currently parked for a task.
func (c *coordinator) CountPendingQuestions(runID, taskID string) int {
	key := questionKey(runID, taskID)
	c.questions.mu.Lock()
	defer c.questions.mu.Unlock()
	if c.questions.byKey[key] != nil {
		return 1
	}
	return 0
}
