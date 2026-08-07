package coordinator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// parkTTL bounds how long a parked question may stay without an explicit
// unpark. If the asker's goroutine is killed before its deferred unpark runs
// (tool timeout, hard kill), the park would otherwise leak forever and make the
// task permanently unable to park again ("task already has a pending
// question"). The default comfortably exceeds the 900s tool-timeout ceiling so
// legitimately parked askers are never evicted early. Tests may override it.
var parkTTL = 30 * time.Minute

// parkSlack extends a parked question's expiry past the asker's effective max
// wait so a timer race can never evict a live park (the asker's unpark runs in
// the same instant a late answer may arrive).
const parkSlack = 15 * time.Second

// pendingQuestion tracks a parked child waiting for an answer.
type pendingQuestion struct {
	messageID string
	// answers is closed after the first deliver; capacity 1 for non-blocking send.
	answers chan string
	// expiresAt is when the park is considered abandoned (asker died without
	// unpark). Expired parks are lazily evicted so a new park for the same task
	// remains possible.
	expiresAt time.Time
}

// questionRegistry keys parked questions by runID/taskID.
type questionRegistry struct {
	mu    sync.Mutex
	byKey map[string]*pendingQuestion
}

func questionKey(runID, taskID string) string { return runID + "\x00" + taskID }

// evictExpiredQuestionsLocked removes parked questions whose TTL has elapsed
// so they no longer block a future ParkQuestion or count as pending. Caller
// must hold c.questions.mu.
func (c *coordinator) evictExpiredQuestionsLocked() {
	now := c.nowLocked()
	for key, q := range c.questions.byKey {
		if now.After(q.expiresAt) {
			delete(c.questions.byKey, key)
		}
	}
}

// ParkQuestion registers a pending question and returns its answer channel.
// unpark must be called when the wait ends (answer, timeout, or cancel).
// One park per task is a structural invariant: the registry stores at most one
// pendingQuestion per runID/taskID key and the awaiting_input single-bit ledger
// status can only be held by one task at a time, so the config knob
// max_pending_questions is a no-op (effective value is always 1).
// maxWait, when provided, is the asker's effective maximum wait: the park
// expires at max(parkTTL, maxWait+parkSlack) so a legitimate long wait is never
// evicted early by a peer's DeliverAnswer, while an orphaned park (asker killed
// without unpark) still self-heals via the TTL. Absent maxWait behaves as 0
// (parkTTL floor only).
func (c *coordinator) ParkQuestion(runID, taskID, messageID string, maxWait ...time.Duration) (<-chan string, func(), error) {
	// Non-interactive parent: the run's parent is a controller that can never
	// answer child questions, so a real park would only burn the asker's full
	// wait_seconds before timing out. Decline immediately at park time with the
	// wire-format decline sentinel — the CLI wait site maps it to
	// {status:"no_answer"} with nil error, so the asker proceeds instead of
	// stalling. No registry entry is created (nothing to unpark, no TTL to
	// consume), and the task itself is never failed. A missing handle (run
	// already evicted) falls through to the existing park semantics.
	if h := c.HandleForRun(runID); h != nil && h.isNonInteractiveParent() {
		ch := make(chan string, 1)
		ch <- agentmsg.AskDeclinePrefix + agentmsg.DeclineReasonParentNonInteractive
		return ch, func() {}, nil
	}
	now := c.nowLocked()
	expiresAt := now.Add(parkTTL)
	if len(maxWait) > 0 && maxWait[0] > 0 {
		if w := maxWait[0] + parkSlack; w > parkTTL {
			expiresAt = now.Add(w)
		}
	}
	q := &pendingQuestion{
		messageID: messageID,
		answers:   make(chan string, 1),
		expiresAt: expiresAt,
	}
	key := questionKey(runID, taskID)
	c.questions.mu.Lock()
	defer c.questions.mu.Unlock()
	c.evictExpiredQuestionsLocked()
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
// (caller may degrade to steer). A parked task whose ledger status is already
// terminal is treated as an orphaned park (asker goroutine killed before its
// deferred unpark): it is evicted and false is returned so the caller surfaces
// the undelivered notice instead of reporting delivery to a dead asker. Repo
// read errors fail open - a buffered channel send to a dead goroutine is
// harmless and the park TTL heals it.
func (c *coordinator) DeliverAnswer(runID, taskID, inReplyTo, body string) bool {
	key := questionKey(runID, taskID)
	c.questions.mu.Lock()
	c.evictExpiredQuestionsLocked()
	q := c.questions.byKey[key]
	c.questions.mu.Unlock()
	if q == nil {
		return false
	}
	if inReplyTo != "" && q.messageID != "" && inReplyTo != q.messageID {
		// Answer targets a different question; do not steal the live park.
		return false
	}
	// Conservative liveness check: an orphaned park must not accept an answer
	// into a dead asker's channel (which would make handlePeerAnswer report
	// delivered=true and suppress the notice). Terminal statuses come from the
	// ledger package; any read error means we cannot tell and we fail open.
	if snap, err := c.repo.GetTask(context.Background(), runID, taskID); err == nil && IsTaskTerminal(snap.Status) {
		c.questions.mu.Lock()
		delete(c.questions.byKey, key)
		c.questions.mu.Unlock()
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
	c.evictExpiredQuestionsLocked()
	if c.questions.byKey[key] != nil {
		return 1
	}
	return 0
}

// RefundMessageQuota decrements the per-task upstream message count after a
// failed persist so a failed message never permanently burns a budget slot
// (messageQuota is otherwise increment-only — no refund existed before).
// Floored at zero: it only ever undoes a prior ConsumeMessageQuota and never
// grants credit that was not consumed.
func (c *coordinator) RefundMessageQuota(runID, taskID string) {
	if c.msgQuota == nil {
		return
	}
	key := questionKey(runID, taskID)
	c.msgQuota.mu.Lock()
	defer c.msgQuota.mu.Unlock()
	if c.msgQuota.count[key] > 0 {
		c.msgQuota.count[key]--
	}
}

// resetMessageQuota clears the per-task message quota count (FIX P3b). A
// retried task must get a fresh upstream message budget for its new attempt
// instead of inheriting attempt 1's count forever. Called at the retry attempt
// boundary (mintRetryAttempt) right after resetTaskAsks, mirroring its shape:
// nil-guarded, locks msgQuota.mu, and deletes the per-task key (which is the
// same questionKey helper the quota registry shares with parked questions).
func (c *coordinator) resetMessageQuota(runID, taskID string) {
	if c.msgQuota == nil || runID == "" || taskID == "" {
		return
	}
	key := questionKey(runID, taskID)
	c.msgQuota.mu.Lock()
	delete(c.msgQuota.count, key)
	c.msgQuota.mu.Unlock()
}

// ParkedQuestion is one live parked question surfaced by run inspection.
type ParkedQuestion struct {
	TaskID    string    `json:"task_id"`
	MessageID string    `json:"message_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ParkedQuestions returns the currently parked questions for a run, read under
// questions.mu. Expired parks are treated as absent via the existing eviction
// (same semantics as CountPendingQuestions / DeliverAnswer). The slice is
// non-nil (empty when none) so callers can render "parks": []. Order follows
// map iteration and is intentionally unspecified.
func (c *coordinator) ParkedQuestions(runID string) []ParkedQuestion {
	if runID == "" {
		return []ParkedQuestion{}
	}
	c.questions.mu.Lock()
	defer c.questions.mu.Unlock()
	c.evictExpiredQuestionsLocked()
	out := []ParkedQuestion{}
	for key, q := range c.questions.byKey {
		kRun, kTask, ok := splitQuestionKey(key)
		if !ok || kRun != runID {
			continue
		}
		out = append(out, ParkedQuestion{TaskID: kTask, MessageID: q.messageID, ExpiresAt: q.expiresAt})
	}
	return out
}

// splitQuestionKey reverses questionKey ("runID\x00taskID").
func splitQuestionKey(key string) (runID, taskID string, ok bool) {
	i := strings.IndexByte(key, 0)
	if i < 0 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}
