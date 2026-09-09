package cliorchestrate

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// TaskRoute is the result of resolving the sole model-facing agent
// selector. It is copied into both execution and persisted work metadata.
// A route with oneshot set is the agent-less case: the task runs a bare
// LLM call on the calling session's own model and completer
// (cliorchestrate.HandlerOneshot), with no tools and no agent policy -
// agent/digest/skill all stay zero.
type TaskRoute struct {
	agent   agents.ResolvedAgent
	digest  string
	skill   string
	oneshot bool
}

// Digest returns the agent definition digest for this route. Empty for a
// Oneshot route.
func (r TaskRoute) Digest() string { return r.digest }

// Oneshot reports whether this route is the agent-less case: no named
// agent, no tools, dispatched to cliorchestrate.HandlerOneshot instead of a
// per-agent handler.
func (r TaskRoute) Oneshot() bool { return r.oneshot }

// routedTaskIdentity resolves a route into the dispatch Name and the
// AgentName/AgentDigest/ProviderName/Model fields for a subagents.Task. A
// Oneshot route dispatches to cliorchestrate.HandlerOneshot with everything
// else left zero: the already-registered OneShotHandler carries its own
// session-bound Completer/Model from dispatcher construction, not a
// per-task override (mirrors delegate's prior one-shot path).
func routedTaskIdentity(route TaskRoute, sessionProvider, sessionModel string) (name, agentName, digest, providerName, model string) {
	if route.oneshot {
		return HandlerOneshot, "", "", "", ""
	}
	providerName, model = resolvedTaskBinding(route, sessionProvider, sessionModel)
	return route.agent.Name, route.agent.Name, route.digest, providerName, model
}

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

// ResolveTaskRoute resolves an agent name and optional skill into a
// TaskRoute. This is the ONE production resolver for every dispatched
// task - dispatch_tasks, spawn_agent, and referral/messaging spawning all
// call this function, not a package-local duplicate, so agent/skill policy
// can never drift between callers.
//
// An empty agentName is valid: it resolves to the Oneshot route (a bare
// LLM call on the caller's own model, no tools, no policy). A skill
// requires an agent's policy scope to check against, so skillName set with
// an empty agentName is refused.
//
// See agents.Select and cliagents.SkillScopeFromAgent for validation rules.
func ResolveTaskRoute(reg *agents.AgentRegistry, skillReg *skills.Registry, agentName, skillName string) (TaskRoute, error) {
	agentName = strings.TrimSpace(agentName)
	skillName = strings.TrimSpace(skillName)
	if agentName == "" {
		if skillName != "" {
			return TaskRoute{}, fmt.Errorf("skill %q requires an agent; an agent-less task runs a bare one-shot call with no tools", skillName)
		}
		return TaskRoute{oneshot: true}, nil
	}
	agent, err := agents.Select(reg, agentName)
	if err != nil {
		return TaskRoute{}, err
	}
	digest, err := agent.DefinitionDigest()
	if err != nil {
		return TaskRoute{}, err
	}
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

// resolveDispatchTaskRoute applies dispatch_tasks' convenience default while
// leaving the shared resolver unchanged for referral and other callers.
func resolveDispatchTaskRoute(reg *agents.AgentRegistry, skillReg *skills.Registry, agentName, skillName string) (TaskRoute, error) {
	if strings.TrimSpace(agentName) == "" && reg != nil {
		if _, ok := reg.Get(agents.BuiltInGeneralPurposeName); ok {
			agentName = agents.BuiltInGeneralPurposeName
		}
	}
	return ResolveTaskRoute(reg, skillReg, agentName, skillName)
}

// taskItemSchema builds one task's schema, with the roster prose
// (agentRoutingDescription) on the agent property. dispatch_tasks is the only
// embedder; a second one that wanted the roster omitted would take a
// parameter back, but an unused knob no caller exercises is a knob nothing
// tests.
//
// "agent" is optional: when general-purpose is present, an omitted or blank
// agent selects it; otherwise it selects the bare one-shot route. A skill
// requires an agent. Only "id" and "prompt" are required.
func taskItemSchema(reg *agents.AgentRegistry) map[string]any {
	skillDescription := "Optional skill invoked under the effective agent's policy"
	if reg == nil {
		skillDescription += "; requires an explicit agent when no general-purpose default is available"
	} else if _, ok := reg.Get(agents.BuiltInGeneralPurposeName); !ok {
		skillDescription += "; requires an explicit agent when no general-purpose default is available"
	}
	agentDescription := agentRoutingDescription(reg)
	// No "enum" here, on purpose. An enum is enforced by the provider's
	// validator and by the SDK's compiled schema, both BEFORE the tool runs,
	// so a name the roster does not carry died as a pre-execution failure
	// counted toward the loop's failure-spiral bound - and all the model was
	// told is where it went wrong ("enum mismatch at /tasks/0/agent"), never
	// which names exist. Routed through the tool instead, the same call
	// reaches ResolveTaskRoute, whose error names the bad agent AND lists the
	// available ones, so the model can correct itself on the next turn. The
	// roster the model composes against still travels in the description
	// below (agentRoutingDescription).
	agentProp := map[string]any{
		"type":        "string",
		"description": agentDescription,
	}
	properties := map[string]any{
		"id":              map[string]any{"type": "string", "description": "Unique task identifier within this run"},
		"agent":           agentProp,
		"skill":           map[string]any{"type": "string", "description": skillDescription},
		"depends_on":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Task IDs that must complete first"},
		"prompt":          map[string]any{"type": "string", "description": "Natural language task description for the selected agent, or the prompt for the fallback one-shot call"},
		"timeout_seconds": map[string]any{"type": "integer", "description": "Per-task timeout override in seconds. " + TimeoutHint()},
		"budget":          map[string]any{"type": "integer", "minimum": 0, "description": "Budget for this task"},
		"output_schema": map[string]any{
			"type":        "object",
			"description": "Optional JSON Schema the agent's final reply must satisfy. Validated before the task completes; prefer this over re-parsing free prose",
		},
		"input_schema": map[string]any{
			"type":        "object",
			"description": "Optional JSON Schema validating this task's input at admission",
		},
	}
	// additionalProperties is TRUE on purpose. A false here is enforced by the
	// provider's own validator (and by the SDK's compiled schema) BEFORE the
	// tool runs, so one decorative field the model attached - "description",
	// "notes", "priority" - refused the whole batch as a pre-execution tool
	// failure, which counts toward the loop's failure-spiral bound. The tool
	// reads the fields it declares and ignores the rest; the fields that would
	// change ROUTING if ignored are refused by name in
	// validateDispatchTaskSelectors, where the error can say which one and why.
	return map[string]any{"type": "object", "properties": properties, "required": []string{"id", "prompt"}, "additionalProperties": true}
}

// agentRoutingBaseDescription states the contract of the task "agent" field
// without a roster. The field is OPTIONAL: general-purpose is the default
// when present; otherwise omission runs a tool-less one-shot call. A skill
// is checked against that effective agent when the default is available. The
// "always available" clause for the compiled built-in is
// appended by agentRoutingDescription only when the built-in actually
// resolved into the registry (a same-name skill collision can skip it), so
// the prose never promises a target the registry lacks.
const agentRoutingBaseDescription = "Optional authorized agent for this task: " +
	"name a listed agent; when general-purpose is available, omission or a blank " +
	"value selects it. If it is unavailable, omission uses a tool-less one-shot call " +
	"on the calling model; skills still require an available agent."

func agentRoutingDescription(reg *agents.AgentRegistry) string {
	description := agentRoutingBaseDescription
	if reg == nil {
		return description
	}
	if _, ok := reg.Get(agents.BuiltInGeneralPurposeName); ok {
		description += " Built-in general-purpose is always available."
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
	// The claim clause above already ends with a period; trim it so the
	// roster join does not double the punctuation.
	return strings.TrimSuffix(description, ".") + ". Available agents: " + strings.Join(hints, "; ")
}
