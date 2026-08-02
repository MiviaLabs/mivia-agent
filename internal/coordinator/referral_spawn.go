package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// SpawnReferral creates one same-run task and starts it concurrently (plan 53.04
// referral-as-spawn). The task is ledger-visible under runID and receives the
// same mailbox + TaskIdentity stamping as normal DAG tasks. It does not extend
// the original DAG's pending set; callers that need the referral outcome poll
// task status or join via run_messages answers.
//
// task.Name / task.AgentName should be the target role (handler registration).
// task.Input should already carry the ask brief. Empty task.ID mints a new id.
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
	// No DependsOn for referral tasks — they start immediately.
	task.DependsOn = nil

	now := c.nowLocked()
	named, err := c.createTask(ctx, runID, task, now)
	if err != nil {
		return "", err
	}
	taskID = named.taskID
	named.task.ID = taskID
	h.setAttempt(taskID, named.attemptID)

	// Snapshot poolCtx under lock so we do not race executeResumedRun's rewrite.
	h.mu.RLock()
	baseCtx := h.poolCtx
	h.mu.RUnlock()
	go c.runReferralTask(h, named.task, baseCtx)
	return taskID, nil
}

func (c *coordinator) runReferralTask(h *RunHandle, task subagents.Task, baseCtx context.Context) {
	if err := c.transitionTask(h, task, string(ledger.TaskStatusRunning)); err != nil {
		_ = c.transitionTaskToStatus(h, task.ID, string(ledger.TaskStatusFailed))
		h.MarkTaskMailboxTerminal(task.ID)
		return
	}
	// Stamp identity + mailbox for this task only (shared mailbox store).
	// baseCtx is a snapshot; cancel still propagates via its parent cancel.
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
// Brief is a JSON object with ask_id, body, from_task, from_role for the child.
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
