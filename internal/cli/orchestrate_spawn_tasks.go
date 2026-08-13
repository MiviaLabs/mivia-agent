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
	ID             string         `json:"id"`
	Agent          string         `json:"agent"`
	Skill          string         `json:"skill,omitempty"`
	DependsOn      []string       `json:"depends_on,omitempty"`
	Prompt         string         `json:"prompt"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Budget         int            `json:"budget,omitempty"`
	OutputSchema   map[string]any `json:"output_schema,omitempty"`
	InputSchema    map[string]any `json:"input_schema,omitempty"`
}

// buildSpawnTasks converts the model-supplied task list into pool tasks,
// resolving each task's timeout and skill permission and propagating the
// caller's identity and depth.
func (t *spawnAgentTool) buildSpawnTasks(params []spawnTaskParams, caller runtime.Caller) ([]subagents.Task, error) {
	subTasks := make([]subagents.Task, len(params))
	for i, pt := range params {
		input, err := json.Marshal(pt.Prompt)
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: marshal input: %w", err)
		}
		// Per-task timeout: an explicit timeout_seconds IS the task's budget
		// (not floored to the 12h default); 0 falls back to the configured
		// default via EffectiveTimeoutSec. The MaxTimeoutSeconds clamp stops a
		// huge model-supplied timeout_seconds from wrapping time.Duration
		// negative (R2B-1).
		taskTimeout := config.RequestedTimeoutSec(t.cfg.DefaultTimeout, pt.TimeoutSeconds)
		route, err := resolveTaskRoute(t.agentReg, t.skillReg, pt.Agent, pt.Skill)
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: %w", err)
		}
		providerName, model := resolvedTaskBinding(route, t.providerName, t.model)
		outSchema, inSchema, err := resolveTaskSchemas(pt.OutputSchema, pt.InputSchema, route, t.skillReg)
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: task %q: %w", pt.ID, err)
		}
		if inSchema != nil {
			if err := validateTaskInput(inSchema, input); err != nil {
				return nil, fmt.Errorf("spawn_agent: task %q: %w", pt.ID, err)
			}
		}
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
			OutputSchema: outSchema,
			InputSchema:  inSchema,
		}
	}
	return subTasks, nil
}
