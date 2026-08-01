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
	Agent          string   `json:"agent"`
	Skill          string   `json:"skill,omitempty"`
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
		route, err := resolveTaskRoute(t.agentReg, t.skillReg, pt.Agent, pt.Skill)
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: %w", err)
		}
		providerName, model := resolvedTaskBinding(route, t.providerName, t.model)
		subTasks[i] = subagents.Task{
			ID:           pt.ID,
			Name:         route.agent.Name,
			AgentName:    route.agent.Name,
			AgentDigest:  route.digest,
			ProviderName: providerName,
			Model:        model,
			Skill:        route.skill,
			Owner:        defaultToolOwner,
			Input:        input,
			DependsOn:    pt.DependsOn,
			Timeout:      time.Duration(taskTimeout) * time.Second,
			Budget:       pt.Budget,
			Depth:        caller.Depth + 1,
			SessionID:    caller.SessionID,
			TurnID:       caller.TurnID,
			Role:         caller.Role,
		}
	}
	return subTasks, nil
}
