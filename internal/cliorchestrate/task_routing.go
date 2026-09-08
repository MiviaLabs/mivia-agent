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

// decodeDispatchTaskJSON adds the presence checks that encoding/json cannot
// express for optional string selectors. It also rejects duplicate keys before
// DisallowUnknownFields decodes the request, so a duplicate cannot silently
// change the route selected by the model.
type dispatchTaskParams struct {
	Tasks          []dispatchTaskParam `json:"tasks"`
	TimeoutSeconds int                 `json:"timeout_seconds,omitempty"`
	Wait           string              `json:"wait,omitempty"`
	WaitTaskID     string              `json:"wait_task_id,omitempty"`
}

func decodeDispatchTaskJSON(args json.RawMessage, target *dispatchTaskParams) error {
	if err := rejectDuplicateJSONKeys(args); err != nil {
		return err
	}
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
	return validateDispatchTaskSelectors(args, target.Tasks)
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(dec); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func scanJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[name]; exists {
					return fmt.Errorf("duplicate JSON key %q", name)
				}
				seen[name] = struct{}{}
				if err := scanJSONValue(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := scanJSONValue(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
	}
	return nil
}

func validateDispatchTaskSelectors(args json.RawMessage, tasks []dispatchTaskParam) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(args, &root); err != nil {
		return err
	}
	rawTasks, ok := root["tasks"]
	if !ok || string(rawTasks) == "null" {
		return fmt.Errorf("tasks must be a non-empty array")
	}
	var taskObjects []json.RawMessage
	if err := json.Unmarshal(rawTasks, &taskObjects); err != nil || len(taskObjects) == 0 {
		return fmt.Errorf("tasks must be a non-empty array")
	}
	for _, field := range []string{"timeout_seconds", "wait", "wait_task_id"} {
		if value, present := root[field]; present && string(value) == "null" {
			return fmt.Errorf("%s must not be null", field)
		}
	}
	seenIDs := make(map[string]struct{}, len(tasks))
	for i, raw := range taskObjects {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for field, value := range fields {
			if string(value) == "null" {
				return fmt.Errorf("task %d: %s must not be null", i+1, field)
			}
		}
		if i < len(tasks) {
			id := strings.TrimSpace(tasks[i].ID)
			if id != "" {
				if _, exists := seenIDs[id]; exists {
					return fmt.Errorf("duplicate task id %q", id)
				}
				seenIDs[id] = struct{}{}
			}
		}
		for _, field := range []string{"agent", "skill"} {
			value, present := fields[field]
			if !present {
				continue
			}
			var text string
			if string(value) == "null" {
				return fmt.Errorf("task %d: %s must be a string when present", i+1, field)
			}
			if err := json.Unmarshal(value, &text); err != nil {
				return fmt.Errorf("task %d: %s must be a string when present: %w", i+1, field, err)
			}
		}
	}
	return nil
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

// taskItemSchema builds one task's schema. includeRoster controls whether the
// agent property carries the full roster prose (agentRoutingDescription):
// dispatch_tasks and spawn_agent both embed this schema in every request, so
// the roster ships once - in dispatch_tasks, the primary router the compiled
// prompt orders - and spawn_agent keeps only the enum for validation.
//
// "agent" is optional: when general-purpose is present, an omitted or blank
// agent selects it; otherwise it selects the bare one-shot route. A skill
// requires an agent. Only "id" and "prompt" are required.
func taskItemSchema(reg *agents.AgentRegistry, includeRoster bool) map[string]any {
	agentDescription := agentRoutingDescription(nil)
	skillDescription := "Optional skill invoked under the effective agent's policy"
	if reg == nil {
		skillDescription += "; requires an explicit agent when no general-purpose default is available"
	} else if _, ok := reg.Get(agents.BuiltInGeneralPurposeName); !ok {
		skillDescription += "; requires an explicit agent when no general-purpose default is available"
	}
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
	return map[string]any{"type": "object", "properties": properties, "required": []string{"id", "prompt"}, "additionalProperties": false}
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

// agentRoutingBaseDescription states the contract of the task "agent" field
// without a roster. The field is OPTIONAL: general-purpose is the default
// when present; otherwise omission runs a tool-less one-shot call. A skill
// is checked against that effective agent when the default is available. The
// "always available" clause for the compiled built-in is
// appended by agentRoutingDescription only when the built-in actually
// resolved into the registry (a same-name skill collision can skip it), so
// the prose never promises a target the enum lacks.
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
