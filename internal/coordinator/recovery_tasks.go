package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// tasksFromSnapshots restores task work without persisted authority.
// The ledger is workspace data. It cannot grant permission or caller identity.
func (c *coordinator) tasksFromSnapshots(ctx context.Context, snaps []ledger.TaskSnapshot) ([]subagents.Task, map[string]subagents.Result, error) {
	return c.tasksFromSnapshotsWithAuthority(ctx, snaps, nil)
}

func (c *coordinator) tasksFromSnapshotsWithAuthority(ctx context.Context, snaps []ledger.TaskSnapshot, liveTasks []subagents.Task) ([]subagents.Task, map[string]subagents.Result, error) {
	out := make([]subagents.Task, 0, len(snaps))
	done := make(map[string]subagents.Result)
	liveByID := make(map[string]subagents.Task, len(liveTasks))
	for _, task := range liveTasks {
		liveByID[task.ID] = task
	}
	for _, snap := range snaps {
		if result, terminal := c.terminalTaskResultWithOutput(ctx, snap); terminal {
			done[snap.TaskID] = result
			continue
		}
		task, err := c.taskFromSnapshot(snap)
		if err != nil {
			return nil, nil, err
		}
		if live, ok := liveByID[task.ID]; ok {
			task.SessionID = live.SessionID
			task.TurnID = live.TurnID
			task.Role = live.Role
			task.Permission = live.Permission
		}
		if err := c.pool.ValidateTask(task); err != nil {
			return nil, nil, fmt.Errorf("resume: task %q routing authorization: %w", snap.TaskID, err)
		}
		out = append(out, task)
	}
	return out, done, nil
}

func (c *coordinator) taskFromSnapshot(snap ledger.TaskSnapshot) (subagents.Task, error) {
	name := snap.AgentName
	if snap.AgentName == "" || snap.AgentDigest == "" {
		if !subagents.IsReservedHandler(snap.HandlerName) {
			return subagents.Task{}, fmt.Errorf("resume: task %q has no agent routing snapshot (created by an older mivia version or an unresolvable handler; cannot dispatch)", snap.TaskID)
		}
		name = snap.HandlerName
	}
	if len(snap.Input) == 0 {
		return subagents.Task{}, fmt.Errorf("resume: task %q has no persisted input (created before task inputs were recorded; cannot resume this run)", snap.TaskID)
	}
	if (snap.ProviderName == "") != (snap.Model == "") {
		return subagents.Task{}, fmt.Errorf("resume: task %q has an incomplete provider/model binding", snap.TaskID)
	}
	return subagents.Task{
		ID: snap.TaskID, Name: name, AgentName: snap.AgentName, AgentDigest: snap.AgentDigest,
		Skill: snap.Skill, ProviderName: snap.ProviderName, Model: snap.Model, Scope: snap.Scope,
		OutputSchema: snap.OutputSchema, InputSchema: snap.InputSchema, DependsOn: snap.DependsOn,
		Input: append(json.RawMessage(nil), snap.Input...), Depth: clampInt(snap.Depth, c.pool.MaxDepth()),
		Budget: clampInt(snap.Budget, c.pool.MaxBudget()), Timeout: clampDuration(snap.Timeout, c.pool.Timeout(), time.Duration(config.DefaultOrchestrationTimeoutSec)*time.Second),
	}, nil
}
