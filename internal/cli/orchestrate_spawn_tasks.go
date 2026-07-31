package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// spawnTaskParams is one entry of spawn_agent's "tasks" argument. It is named
// rather than inline so the task-building loop can live in its own function and
// keep Execute inside the function-length gate.
type spawnTaskParams struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	DependsOn      []string `json:"depends_on,omitempty"`
	Prompt         string   `json:"prompt"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	Budget         int      `json:"budget,omitempty"`
}

// buildSpawnTasks converts the model-supplied task list into pool tasks,
// resolving each task's timeout and skill permission and propagating the
// caller's identity and depth.
func (t *spawnAgentTool) buildSpawnTasks(params []spawnTaskParams, caller runtime.Caller) ([]subagents.Task, error) {
	batchTimeout := config.EffectiveTimeoutSec(t.cfg.DefaultTimeout, 0)
	subTasks := make([]subagents.Task, len(params))
	for i, pt := range params {
		input, err := json.Marshal(pt.Prompt)
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: marshal input: %w", err)
		}
		taskTimeout := batchTimeout
		if pt.TimeoutSeconds > 0 {
			taskTimeout = pt.TimeoutSeconds
		}
		permission := ""
		if t.skillReg != nil {
			if skill, ok := t.skillReg.Get(pt.Name); ok {
				// Skill selection via name must pass the selected root agent's
				// allowlist (no registry/handler bypass of agent policy).
				if err := t.skillScope.checkSkill(skill.Name, skill.Tools); err != nil {
					return nil, fmt.Errorf("spawn_agent: %w", err)
				}
				permission = skill.Permission
			}
		}
		subTasks[i] = subagents.Task{
			ID:         pt.ID,
			Name:       pt.Name,
			Owner:      defaultToolOwner,
			Input:      input,
			DependsOn:  pt.DependsOn,
			Timeout:    time.Duration(taskTimeout) * time.Second,
			Budget:     pt.Budget,
			Depth:      caller.Depth + 1,
			SessionID:  caller.SessionID,
			TurnID:     caller.TurnID,
			Role:       caller.Role,
			Permission: permission,
		}
	}
	return subTasks, nil
}
