package cli

import (
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// AgentSkillScope is an immutable per-instance skill policy snapshot for the
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
type AgentSkillScope struct {
	agentName string
	// restricted is true when Skills was explicitly set (including empty []).
	// Zero value (restricted=false) allows all skill names.
	restricted bool
	allowed    map[string]struct{} // used when restricted
	// enforceTools is true when a selected agent provided an EffectiveTools set.
	enforceTools bool
	agentTools   map[string]struct{}
	// liveTools, when non-nil, is the final post-disable/deny tool registry
	// snapshot (plan 43). Skill invocation requires every declared static tool
	// to be present here as well as in the agent's effective set.
	liveTools map[string]struct{}
	// origins maps skill name → bound origin from the resolved agent's
	// allowlist (plan 43). A runtime-resolved definition whose origin differs
	// is an authorization event (a project skill silently shadowing a
	// user-bound allowlist entry) and fails closed.
	origins map[string]string
}

// skillScopeFromAgent snapshots the selected agent's skill allowlist and tools.
// A nil agent means the compiled default root (all skills, no tool subset gate).
func skillScopeFromAgent(selected *agents.ResolvedAgent) AgentSkillScope {
	if selected == nil {
		return AgentSkillScope{}
	}
	tools := make(map[string]struct{}, len(selected.EffectiveTools))
	for _, n := range selected.EffectiveTools {
		tools[n] = struct{}{}
	}
	scope := AgentSkillScope{
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
	if len(selected.SkillOrigins) > 0 {
		scope.origins = make(map[string]string, len(selected.SkillOrigins))
		for name, origin := range selected.SkillOrigins {
			scope.origins[name] = origin
		}
	}
	return scope
}

// skillScopeFromAgentAndRegistry is skillScopeFromAgent plus the final live
// tool registry snapshot (after disable/deny filtering). A nil agent stays
// unrestricted (compiled default root owns the full catalogue); a nil registry
// leaves the live check disabled.
func skillScopeFromAgentAndRegistry(selected *agents.ResolvedAgent, reg *tools.Registry) AgentSkillScope {
	scope := skillScopeFromAgent(selected)
	if selected == nil || reg == nil {
		return scope
	}
	live := make(map[string]struct{}, len(scope.agentTools))
	for _, tool := range reg.List() {
		live[tool.Name()] = struct{}{}
	}
	scope.liveTools = live
	return scope
}

// checkSkill reports whether the scope may invoke the named skill with its
// declared tools. Built-in handlers (non-skills) are not passed here.
func (s AgentSkillScope) checkSkill(name string, skillTools []string) error {
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
		if s.liveTools != nil {
			if _, ok := s.liveTools[tool]; !ok {
				agent := s.agentName
				if agent == "" {
					agent = "(none)"
				}
				return fmt.Errorf("skill %q requires tool %q not present in the live tool registry for agent %q", name, tool, agent)
			}
		}
	}
	return nil
}

// CheckSkillDefinition enforces the full plan 43 policy for one skill
// definition: allowlist, declared-tool subset of the agent's effective tools
// and the live registry, and origin fail-closed (a runtime definition whose
// origin differs from the allowlist-bound origin for the same name is an
// authorization event).
func (s AgentSkillScope) CheckSkillDefinition(def skills.Definition) error {
	if err := s.checkSkill(def.Name, def.Tools); err != nil {
		return err
	}
	if len(s.origins) == 0 {
		return nil
	}
	want, bound := s.origins[def.Name]
	if !bound {
		return nil
	}
	wanted := skills.Origin(want)
	if want == string(config.AgentSourceWorkspace) {
		wanted = skills.OriginProject
	}
	if def.Origin != wanted {
		agent := s.agentName
		if agent == "" {
			agent = "(none)"
		}
		return fmt.Errorf("skill %q origin mismatch: allowlist bound %q origin for agent %q but runtime resolved %q", def.Name, want, agent, def.Origin)
	}
	return nil
}

// filterSkillsForScope returns a registry containing only skills the scope may
// invoke (name allowlist only; tool subset is checked at invocation).
func filterSkillsForScope(reg *skills.Registry, scope AgentSkillScope) *skills.Registry {
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
func skillAllowlistPtr(scope AgentSkillScope) *[]string {
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
