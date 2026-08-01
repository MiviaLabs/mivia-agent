package agents

import (
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TraceField records the winning source for one resolved field. It contains
// presence only for prompts; it never contains prompt text or a digest.
type TraceField struct {
	Name         string
	Source       config.AgentSource
	Path         string
	ValuePresent bool
}

// TraceOperation records an authored tool operation without retaining content
// outside the bounded tool-name lists.
type TraceOperation struct {
	Kind  string
	Tools []string
}

// ResolutionTrace explains how a definition was resolved for CLI inspection.
// Paths are for an explicitly selected explain operation only; this type is
// not attached to public runtime events.
type ResolutionTrace struct {
	ParentChain       []string
	FinalSource       config.AgentSource
	FinalPath         string
	Fields            []TraceField
	ToolOperations    []TraceOperation
	GuardrailRemovals []string
	EffectiveDenylist []string
	SkillScope        string
	SkillNames        []string
}

func (t ResolutionTrace) clone() ResolutionTrace {
	out := t
	out.ParentChain = slices.Clone(t.ParentChain)
	out.Fields = slices.Clone(t.Fields)
	out.ToolOperations = make([]TraceOperation, len(t.ToolOperations))
	for i, op := range t.ToolOperations {
		out.ToolOperations[i] = TraceOperation{Kind: op.Kind, Tools: slices.Clone(op.Tools)}
	}
	out.GuardrailRemovals = slices.Clone(t.GuardrailRemovals)
	out.EffectiveDenylist = slices.Clone(t.EffectiveDenylist)
	out.SkillNames = slices.Clone(t.SkillNames)
	return out
}

func traceSkillScope(skills *[]string) string {
	if skills == nil {
		return "all trusted skills"
	}
	if len(*skills) == 0 {
		return "none"
	}
	return "explicit"
}

func traceFields(in ResolveInput, parent *ResolvedAgent, fields inheritedFields) []TraceField {
	field := func(name string, own bool, inherit bool, present bool) TraceField {
		if own {
			return TraceField{Name: name, Source: in.Source, Path: in.Path, ValuePresent: present}
		}
		if inherit && parent != nil {
			for _, inherited := range parent.Trace.Fields {
				if inherited.Name == name {
					return inherited
				}
			}
		}
		return TraceField{Name: name, ValuePresent: present}
	}
	return []TraceField{
		field("description", in.Spec.Description != nil, false, in.Spec.Description != nil),
		// provider and model are inherited as one binding, so a spec that
		// restates either key owns both rows.
		field("provider", in.Spec.Provider != nil || in.Spec.Model != nil, true, strings.TrimSpace(fields.provider) != ""),
		field("model", in.Spec.Provider != nil || in.Spec.Model != nil, true, strings.TrimSpace(fields.model) != ""),
		field("prompt", in.Spec.SystemPrompt != nil, true, fields.systemPrompt != ""),
		field("max_turns", in.Spec.MaxTurns != nil, true, fields.maxTurns != nil),
	}
}

func buildTrace(in ResolveInput, parent *ResolvedAgent, parentName string, fields inheritedFields, baseline, effective []string, deny []string, skills *[]string, opts ResolveOptions) ResolutionTrace {
	chain := []string{}
	if parent != nil {
		chain = append(chain, parent.Trace.ParentChain...)
		chain = append(chain, parent.Name)
	}
	operations := []TraceOperation{}
	if parent != nil {
		operations = append(operations, TraceOperation{Kind: "inherited baseline", Tools: slices.Clone(parent.EffectiveTools)})
	} else {
		operations = append(operations, TraceOperation{Kind: "default baseline", Tools: slices.Clone(baseline)})
	}
	if in.Spec.Tools != nil {
		operations = append(operations, TraceOperation{Kind: "replace", Tools: traceToolNames(*in.Spec.Tools)})
	}
	if in.Spec.ToolsAdd != nil {
		operations = append(operations, TraceOperation{Kind: "add", Tools: traceToolNames(*in.Spec.ToolsAdd)})
	}
	if in.Spec.ToolsRemove != nil {
		operations = append(operations, TraceOperation{Kind: "remove", Tools: traceToolNames(*in.Spec.ToolsRemove)})
	}
	if in.Spec.DisallowedTools != nil {
		operations = append(operations, TraceOperation{Kind: "deny", Tools: traceToolNames(*in.Spec.DisallowedTools)})
	}
	guardrail := []string{}
	mandatory := map[string]bool{}
	for _, n := range []string{"delegate", "dispatch_tasks", "spawn_agent", "inspect_agents", "join_run", "cancel_run"} {
		mandatory[n] = true
	}
	for _, n := range opts.Global.MandatoryToolDenylistAdditions {
		if tools.IsKnownToolName(strings.TrimSpace(n)) {
			mandatory[strings.TrimSpace(n)] = true
		}
	}
	for _, raw := range baseline {
		n := strings.TrimSpace(raw)
		if mandatory[n] && !slices.Contains(effective, n) {
			guardrail = append(guardrail, n)
		}
	}
	for _, n := range deny {
		if tools.IsKnownToolName(strings.TrimSpace(n)) {
			mandatory[strings.TrimSpace(n)] = true
		}
	}
	effectiveDeny := make([]string, 0, len(mandatory))
	for n := range mandatory {
		effectiveDeny = append(effectiveDeny, n)
	}
	slices.Sort(guardrail)
	slices.Sort(effectiveDeny)
	return ResolutionTrace{
		ParentChain: chain, FinalSource: in.Source, FinalPath: in.Path,
		Fields: traceFields(in, parent, fields), ToolOperations: operations,
		GuardrailRemovals: guardrail, EffectiveDenylist: effectiveDeny,
		SkillScope: traceSkillScope(skills), SkillNames: traceSkillNames(skills),
	}
}

func traceSkillNames(skills *[]string) []string {
	if skills == nil {
		return nil
	}
	out := make([]string, 0, len(*skills))
	for _, name := range *skills {
		name = strings.TrimSpace(name)
		if name != "" && len(name) <= 80 {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func traceToolNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if tools.IsKnownToolName(strings.TrimSpace(name)) {
			out = append(out, strings.TrimSpace(name))
		}
	}
	slices.Sort(out)
	return out
}
