package cliorchestrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// TaskRoute is the result of resolving the sole model-facing agent
// selector. It is copied into both execution and persisted work metadata.
type TaskRoute struct {
	agent  agents.ResolvedAgent
	digest string
	skill  string
}

// Digest returns the agent definition digest for this route.
func (r TaskRoute) Digest() string { return r.digest }

func resolvedTaskBinding(route TaskRoute, sessionProvider, sessionModel string) (string, string) {
	providerName := route.agent.Provider
	if providerName == "" {
		providerName = sessionProvider
	}
	model := route.agent.Model
	if model == "" {
		model = sessionModel
	}
	return strings.ToLower(strings.TrimSpace(providerName)), strings.TrimSpace(model)
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

// ResolveTaskRoute resolves an agent name and optional skill into a TaskRoute.
// See agents.Select and cliagents.SkillScopeFromAgent for validation rules.
func ResolveTaskRoute(reg *agents.AgentRegistry, skillReg *skills.Registry, agentName, skillName string) (TaskRoute, error) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return TaskRoute{}, fmt.Errorf("task agent is required")
	}
	agent, err := agents.Select(reg, agentName)
	if err != nil {
		return TaskRoute{}, err
	}
	digest, err := agent.DefinitionDigest()
	if err != nil {
		return TaskRoute{}, err
	}
	skillName = strings.TrimSpace(skillName)
	if skillName != "" {
		if skillReg == nil {
			return TaskRoute{}, fmt.Errorf("agent %q may not invoke skill %q", agent.Name, skillName)
		}
		skill, ok := skillReg.Get(skillName)
		if !ok {
			return TaskRoute{}, fmt.Errorf("unknown skill %q", skillName)
		}
		if err := cliagents.SkillScopeFromAgent(&agent).CheckSkillDefinition(skill); err != nil {
			return TaskRoute{}, err
		}
	}
	return TaskRoute{agent: agent, digest: digest, skill: skillName}, nil
}

// taskItemSchema builds one task's schema. includeRoster controls whether the
// agent property carries the full roster prose (agentRoutingDescription):
// dispatch_tasks and spawn_agent both embed this schema in every request, so
// the roster ships once - in dispatch_tasks, the primary router the compiled
// prompt orders - and spawn_agent keeps only the enum for validation.
func taskItemSchema(reg *agents.AgentRegistry, includeBudget, includeRoster bool) map[string]any {
	agentDescription := agentRoutingDescription(nil)
	if includeRoster {
		agentDescription = agentRoutingDescription(reg)
	}
	agentProp := map[string]any{
		"type":        "string",
		"description": agentDescription,
	}
	if names := agentNames(reg); len(names) > 0 {
		agentProp["enum"] = names
	}
	properties := map[string]any{
		"id":              map[string]any{"type": "string", "description": "Unique task identifier within this run"},
		"agent":           agentProp,
		"skill":           map[string]any{"type": "string", "description": "Optional skill invoked under the selected agent's policy"},
		"depends_on":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Task IDs that must complete first"},
		"prompt":          map[string]any{"type": "string", "description": "Natural language task description for the selected agent"},
		"timeout_seconds": map[string]any{"type": "integer", "description": "Per-task timeout override in seconds. " + TimeoutHint()},
		"output_schema": map[string]any{
			"type":        "object",
			"description": "Optional JSON Schema the agent's final reply must satisfy. Validated before the task completes; prefer this over re-parsing free prose",
		},
		"input_schema": map[string]any{
			"type":        "object",
			"description": "Optional JSON Schema validating this task's input at admission",
		},
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
	// AgentRegistry.Names() returns nil, not an empty slice, when zero
	// agents are registered (slices.Clone of a nil backing slice stays
	// nil). This value is marshaled straight into the dispatch_tasks tool
	// schema's "enum" field below, and encoding/json renders a nil []string
	// as JSON null rather than []. Some providers' function-schema
	// validators (DeepSeek's, confirmed) reject "enum": null outright,
	// failing every tool-enabled request in a workspace with no named
	// agents - the common case. Coalesce here rather than in Names()
	// itself, since this is the only caller that puts the result on the
	// wire as JSON; other callers only range over or Join() it, where nil
	// and empty behave identically.
	if names := reg.Names(); names != nil {
		return names
	}
	return []string{}
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
