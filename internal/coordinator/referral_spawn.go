package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// referralWG tracks in-flight referral tasks on a run handle so executeRun
// does not release the claim while they still mutate the ledger.
type referralTracker struct {
	mu sync.Mutex
	wg sync.WaitGroup
}

func (h *RunHandle) referralAdd() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.referrals == nil {
		h.referrals = &referralTracker{}
	}
	rt := h.referrals
	h.mu.Unlock()
	rt.wg.Add(1)
}

func (h *RunHandle) referralDone() {
	if h == nil {
		return
	}
	h.mu.RLock()
	rt := h.referrals
	h.mu.RUnlock()
	if rt != nil {
		rt.wg.Done()
	}
}

func (h *RunHandle) waitReferrals() {
	if h == nil {
		return
	}
	h.mu.RLock()
	rt := h.referrals
	h.mu.RUnlock()
	if rt != nil {
		rt.wg.Wait()
	}
}

// SpawnReferral creates one same-run task and starts it concurrently (plan 53.04).
func (c *coordinator) SpawnReferral(ctx context.Context, runID string, task subagents.Task) (taskID string, err error) {
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
		return
	}
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	execCtx := contextWithRunExec(baseCtx, h.runID, []subagents.Task{task}, h.mailboxes)
	results, _ := c.pool.Run(execCtx, []subagents.Task{task})
	var result subagents.Result
	if len(results) > 0 {
		result = results[0]
	} else {
		result = subagents.Result{TaskID: task.ID, Status: "failed", Err: fmt.Errorf("missing referral result")}
	}
	status := mapStatus(result)
	if err := c.transitionTaskToStatus(h, task.ID, status); err != nil {
		h.MarkTaskMailboxTerminal(task.ID)
		return
	}
	h.MarkTaskMailboxTerminal(task.ID)
	finished := c.nowLocked()
	persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.repo.SetTaskAttempt(persistCtx, h.runID, task.ID, h.getAttempt(task.ID), status, &finished)
}

// SpawnReferralFromAsk builds a referral task from a persisted ask and starts it.
func (c *coordinator) SpawnReferralFromAsk(ctx context.Context, runID, toRole string, ask agentmsg.Message) (taskID string, err error) {
	brief, err := json.Marshal(map[string]any{
		"kind":      "ask",
		"ask_id":    ask.ID,
		"body":      ask.Body,
		"from_task": ask.From.TaskID,
		"from_role": ask.From.Role,
		"refs":      ask.Refs,
	})
	if err != nil {
		return "", err
	}
	return c.SpawnReferral(ctx, runID, subagents.Task{
		Name:      toRole,
		AgentName: toRole,
		Input:     brief,
	})
}
