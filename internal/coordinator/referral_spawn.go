package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// referralTracker counts in-flight referral tasks on a run handle so
// executeRun does not release the claim while they still mutate the ledger.
// Uses an active counter under RunHandle.mu (not WaitGroup) so Add cannot
// race Wait when referrals start after the DAG begins draining.
type referralTracker struct {
	active int
	cond   *sync.Cond // optional; set when first waiters appear
}

func (h *RunHandle) referralAdd() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.referrals == nil {
		h.referrals = &referralTracker{}
	}
	h.referrals.active++
	h.mu.Unlock()
}

func (h *RunHandle) referralDone() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.referrals != nil && h.referrals.active > 0 {
		h.referrals.active--
		if h.referrals.active == 0 && h.referrals.cond != nil {
			h.referrals.cond.Broadcast()
		}
	}
	h.mu.Unlock()
}

func (h *RunHandle) waitReferrals() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.referrals == nil {
		h.mu.Unlock()
		return
	}
	if h.referrals.cond == nil {
		h.referrals.cond = sync.NewCond(&h.mu)
	}
	for h.referrals.active > 0 {
		h.referrals.cond.Wait()
	}
	h.mu.Unlock()
}

// SpawnReferral creates one same-run task and starts it concurrently (plan 53.04).
// When askID is non-empty it is bound to the new task ID before the goroutine
// starts so failed referrals always CloseAsk.
func (c *coordinator) SpawnReferral(ctx context.Context, runID string, task subagents.Task, askID string) (taskID string, err error) {
	if runID == "" {
		return "", fmt.Errorf("spawn referral: run_id required")
	}
	h := c.HandleForRun(runID)
	if h == nil {
		return "", fmt.Errorf("spawn referral: run %q is not active", runID)
	}
	if task.Name == "" && task.AgentName == "" {
		return "", fmt.Errorf("spawn referral: agent name required")
	}
	if task.AgentName == "" {
		task.AgentName = task.Name
	}
	if task.Name == "" {
		task.Name = task.AgentName
	}
	task.DependsOn = nil

	now := c.nowLocked()
	named, err := c.createTask(ctx, runID, task, now)
	if err != nil {
		return "", err
	}
	taskID = named.taskID
	named.task.ID = taskID
	h.setAttempt(taskID, named.attemptID)

	// Bind before go so takeReferralAsk cannot miss a fast failure.
	if askID != "" {
		c.bindReferralAsk(taskID, askID)
	}
	h.mu.RLock()
	baseCtx := h.poolCtx
	h.mu.RUnlock()
	h.referralAdd()
	go func() {
		defer h.referralDone()
		c.runReferralTask(h, named.task, baseCtx)
	}()
	return taskID, nil
}

func (c *coordinator) runReferralTask(h *RunHandle, task subagents.Task, baseCtx context.Context) {
	if err := c.transitionTask(h, task, string(ledger.TaskStatusRunning)); err != nil {
		_ = c.transitionTaskToStatus(h, task.ID, string(ledger.TaskStatusFailed))
		h.MarkTaskMailboxTerminal(task.ID)
		if askID := c.takeReferralAsk(task.ID); askID != "" {
			c.CloseAsk(askID)
		}
		return
	}
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	execCtx := contextWithRunExec(baseCtx, h.runID, []subagents.Task{task}, h.mailboxes)
	results, _ := c.pool.Run(execCtx, []subagents.Task{task})
	result := subagents.Result{TaskID: task.ID, Status: "failed", Err: fmt.Errorf("missing referral result")}
	if len(results) > 0 {
		result = results[0]
	}
	status := mapStatus(result)
	// Best-effort terminal: if CAS fails (cancel race), still fence mailbox.
	_ = c.transitionTaskToStatus(h, task.ID, status)
	h.MarkTaskMailboxTerminal(task.ID)
	// Failed/canceled referrals must close the open ask so quotas are not orphaned.
	if askID := c.takeReferralAsk(task.ID); askID != "" {
		if status != string(ledger.TaskStatusCompleted) {
			c.CloseAsk(askID)
		}
	}
	finished := c.nowLocked()
	persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.repo.SetTaskAttempt(persistCtx, h.runID, task.ID, h.getAttempt(task.ID), status, &finished)
}

// ReferralSpawnMeta carries optional agent-routing fields for production agents
// (digest/provider/model). Zero values leave Task defaults empty.
type ReferralSpawnMeta struct {
	AgentDigest  string
	ProviderName string
	Model        string
}

// SpawnReferralFromAsk builds a referral task from a persisted ask and starts it.
// Input is a JSON string prompt (production multi_step/oneshot handlers require
// string input, not an object). The ask_id is embedded so the target can answer.
func (c *coordinator) SpawnReferralFromAsk(ctx context.Context, runID, toRole string, ask agentmsg.Message, meta ...ReferralSpawnMeta) (taskID string, err error) {
	// Prompt shape matches live inject framing so models see the same ask_id field.
	prompt := "ask_id: " + ask.ID + "\n" + strings.TrimSpace(ask.Body)
	if strings.TrimSpace(ask.Body) == "" {
		prompt = "ask_id: " + ask.ID
	}
	// string marshal cannot fail.
	input, _ := json.Marshal(prompt)
	task := subagents.Task{
		Name:      toRole,
		AgentName: toRole,
		Input:     input,
	}
	if len(meta) > 0 {
		task.AgentDigest = meta[0].AgentDigest
		task.ProviderName = meta[0].ProviderName
		task.Model = meta[0].Model
	}
	return c.SpawnReferral(ctx, runID, task, ask.ID)
}

// referralAsk binds spawned task IDs to open ask IDs so a failed referral can CloseAsk.
func (c *coordinator) bindReferralAsk(taskID, askID string) {
	if taskID == "" || askID == "" {
		return
	}
	reg := c.ensureAsks()
	reg.mu.Lock()
	reg.referralTaskAsk[taskID] = askID
	reg.mu.Unlock()
}

func (c *coordinator) takeReferralAsk(taskID string) string {
	if c.asks == nil || taskID == "" {
		return ""
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.referralTaskAsk == nil {
		return ""
	}
	id := c.asks.referralTaskAsk[taskID]
	delete(c.asks.referralTaskAsk, taskID)
	return id
}
