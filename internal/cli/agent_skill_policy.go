package cli

import (
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// agentSkillScope is an immutable per-instance skill policy snapshot for the
// selected root agent. Built once at dispatcher construction; never shared
// across concurrent agents or model switches as a mutable registry.
//
// Zero value is unrestricted (backward compatible for dispatchers built without
// an agent). Explicit empty allowlists always set restricted=true with a
// non-nil allowed map.
//
// v1 scope is root fan-out only: nested multi_step agents do not receive
// privileged dispatch_tasks/spawn_agent, so they cannot synthesize skill tasks.
// Resume/retry re-enters skill handlers which re-check this scope.
type agentSkillScope struct {
	agentName string
	// restricted is true when Skills was explicitly set (including empty []).
	// Zero value (restricted=false) allows all skill names.
	restricted bool
	allowed    map[string]struct{} // used when restricted
	// enforceTools is true when a selected agent provided an EffectiveTools set.
	enforceTools bool
	agentTools   map[string]struct{}
}

// skillScopeFromAgent snapshots the selected agent's skill allowlist and tools.
// A nil agent means the compiled default root (all skills, no tool subset gate).
func skillScopeFromAgent(selected *agents.ResolvedAgent) agentSkillScope {
	if selected == nil {
		return agentSkillScope{}
	}
	tools := make(map[string]struct{}, len(selected.EffectiveTools))
	for _, n := range selected.EffectiveTools {
		tools[n] = struct{}{}
	}
	scope := agentSkillScope{
		agentName:    selected.Name,
		enforceTools: true,
		agentTools:   tools,
	}
	if selected.Skills == nil {
		return scope
	}
	allowed := make(map[string]struct{}, len(*selected.Skills))
	for _, n := range *selected.Skills {
		allowed[n] = struct{}{}
	}
	scope.restricted = true
	scope.allowed = allowed
	return scope
}

// checkSkill reports whether the scope may invoke the named skill with its
// declared tools. Built-in handlers (non-skills) are not passed here.
func (s agentSkillScope) checkSkill(name string, skillTools []string) error {
	if s.restricted {
		if _, ok := s.allowed[name]; !ok {
			agent := s.agentName
			if agent == "" {
				agent = "(none)"
			}
			return fmt.Errorf("agent %q may not invoke skill %q", agent, name)
		}
	}
	// Tools subset when a selected agent defined EffectiveTools. Zero-value
	// scope (no agent) skips the subset gate.
	if len(skillTools) == 0 || !s.enforceTools {
		return nil
	}
	for _, tool := range skillTools {
		if _, ok := s.agentTools[tool]; !ok {
			agent := s.agentName
			if agent == "" {
				agent = "(none)"
			}
			return fmt.Errorf("skill %q requires tool %q not allowed on agent %q", name, tool, agent)
		}
	}
	return nil
}

// filterSkillsForScope returns a registry containing only skills the scope may
// invoke (name allowlist only; tool subset is checked at invocation).
func filterSkillsForScope(reg *skills.Registry, scope agentSkillScope) *skills.Registry {
	if reg == nil || !scope.restricted {
		return reg
	}
	out := skills.NewRegistry()
	for _, def := range reg.List() {
		if _, ok := scope.allowed[def.Name]; !ok {
			continue
		}
		_ = out.Register(def)
	}
	return out
}

// skillAllowlistPtr returns the ListModelFacing allowlist pointer for the scope.
// nil = all; &empty = none; &names = those names.
func skillAllowlistPtr(scope agentSkillScope) *[]string {
	if !scope.restricted {
		return nil
	}
	names := make([]string, 0, len(scope.allowed))
	for n := range scope.allowed {
		names = append(names, n)
	}
	slices.Sort(names)
	return &names
}
