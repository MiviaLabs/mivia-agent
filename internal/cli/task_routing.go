package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// taskRoute is the private result of resolving the sole model-facing agent
// selector. It is copied into both execution and persisted work metadata.
type taskRoute struct {
	agent  agents.ResolvedAgent
	digest string
	skill  string
}

func decodeStrictTaskJSON(args json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func resolveTaskRoute(reg *agents.AgentRegistry, skillReg *skills.Registry, agentName, skillName string) (taskRoute, error) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return taskRoute{}, fmt.Errorf("task agent is required")
	}
	agent, err := agents.Select(reg, agentName)
	if err != nil {
		return taskRoute{}, err
	}
	digest, err := agent.DefinitionDigest()
	if err != nil {
		return taskRoute{}, err
	}
	skillName = strings.TrimSpace(skillName)
	if skillName != "" {
		if skillReg == nil {
			return taskRoute{}, fmt.Errorf("agent %q may not invoke skill %q", agent.Name, skillName)
		}
		skill, ok := skillReg.Get(skillName)
		if !ok {
			return taskRoute{}, fmt.Errorf("unknown skill %q", skillName)
		}
		if err := skillScopeFromAgent(&agent).checkSkill(skill.Name, skill.Tools); err != nil {
			return taskRoute{}, err
		}
	}
	return taskRoute{agent: agent, digest: digest, skill: skillName}, nil
}

func taskItemSchema(reg *agents.AgentRegistry, includeBudget bool) map[string]any {
	properties := map[string]any{
		"id": map[string]any{"type": "string", "description": "Unique task identifier within this run"},
		"agent": map[string]any{
			"type": "string", "enum": agentNames(reg),
			"description": agentRoutingDescription(reg),
		},
		"skill":           map[string]any{"type": "string", "description": "Optional skill invoked under the selected agent's policy"},
		"depends_on":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Task IDs that must complete first"},
		"prompt":          map[string]any{"type": "string", "description": "Natural language task description for the selected agent"},
		"timeout_seconds": map[string]any{"type": "integer", "description": "Per-task timeout override in seconds"},
	}
	if includeBudget {
		properties["budget"] = map[string]any{"type": "integer", "minimum": 0, "description": "Budget for this task"}
	}
	return map[string]any{"type": "object", "properties": properties, "required": []string{"id", "agent", "prompt"}, "additionalProperties": false}
}

func agentNames(reg *agents.AgentRegistry) []string {
	if reg == nil {
		return []string{}
	}
	return reg.Names()
}

func agentRoutingDescription(reg *agents.AgentRegistry) string {
	description := "Required authorized agent definition for this task"
	if reg == nil {
		return description
	}
	var hints []string
	for _, agent := range reg.List() {
		if agent.Description == "" {
			hints = append(hints, agent.Name)
			continue
		}
		hints = append(hints, agent.Name+": "+agents.SanitizeDescription(agent.Description))
	}
	if len(hints) == 0 {
		return description
	}
	return description + ". Available agents: " + strings.Join(hints, "; ")
}
