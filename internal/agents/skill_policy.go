package agents

import (
	"fmt"
	"slices"
)

// SkillAllowed reports whether the resolved agent may invoke skillName.
// When agent is nil or Skills is nil, all skills are allowed (root omit / no agent).
// When Skills is non-nil empty, none are allowed. Otherwise only listed names.
func SkillAllowed(agent *ResolvedAgent, skillName string) bool {
	if agent == nil || agent.Skills == nil {
		return true
	}
	return slices.Contains(*agent.Skills, skillName)
}

// SkillToolsCovered reports whether agent.EffectiveTools is a superset of
// skillTools. An empty skillTools list always passes (nothing required).
// When agent is nil, coverage is treated as unrestricted (compiled default root).
func SkillToolsCovered(agent *ResolvedAgent, skillTools []string) bool {
	if len(skillTools) == 0 {
		return true
	}
	if agent == nil {
		return true
	}
	have := make(map[string]struct{}, len(agent.EffectiveTools))
	for _, n := range agent.EffectiveTools {
		have[n] = struct{}{}
	}
	for _, n := range skillTools {
		if _, ok := have[n]; !ok {
			return false
		}
	}
	return true
}

// CheckSkillInvocation enforces allowlist + tools-superset for one skill call.
// Returns a non-nil error when the selected agent may not invoke the skill.
func CheckSkillInvocation(agent *ResolvedAgent, skillName string, skillTools []string) error {
	if !SkillAllowed(agent, skillName) {
		name := "(none)"
		if agent != nil {
			name = agent.Name
		}
		return fmt.Errorf("agent %q may not invoke skill %q", name, skillName)
	}
	if !SkillToolsCovered(agent, skillTools) {
		name := "(none)"
		if agent != nil {
			name = agent.Name
		}
		missing := firstMissingTool(agent, skillTools)
		return fmt.Errorf("skill %q requires tool %q not allowed on agent %q", skillName, missing, name)
	}
	return nil
}

func firstMissingTool(agent *ResolvedAgent, skillTools []string) string {
	if agent == nil {
		return ""
	}
	have := make(map[string]struct{}, len(agent.EffectiveTools))
	for _, n := range agent.EffectiveTools {
		have[n] = struct{}{}
	}
	for _, n := range skillTools {
		if _, ok := have[n]; !ok {
			return n
		}
	}
	return ""
}
