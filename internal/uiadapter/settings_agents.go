package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// initAgentsFromConfig seeds SettingsStore.agents from the resolved agent
// registry built at session setup, split out alongside settingsAgents so
// the Agents section's read/write surface lives in one file the same way
// settingsSkills' does in settings_skills.go.
func (s *SettingsStore) initAgentsFromConfig() {
	if s.agentState == nil || s.agentState.Registry == nil {
		return
	}
	for _, a := range s.agentState.Registry.List() {
		var skills []string
		if a.Skills != nil {
			skills = *a.Skills
		}
		s.agents = append(s.agents, ports.AgentView{
			Name:              a.Name,
			Description:       a.Description,
			Provider:          a.Provider,
			Model:             a.Model,
			Tools:             a.EffectiveTools,
			Skills:            skills,
			MCPServers:        a.EffectiveMCPServers,
			SystemPromptChars: len(a.SystemPrompt),
			Scope:             agentSourceToScope(a.Provenance.Source),
		})
	}
}

func agentViewToSettings(v ports.AgentView) config.AgentFileSettings {
	return config.AgentFileSettings{
		Name:        v.Name,
		Description: v.Description,
		Provider:    v.Provider,
		Model:       v.Model,
		Tools:       v.Tools,
		Skills:      v.Skills,
		MCPServers:  v.MCPServers,
	}
}

// settingsAgents implements ports.AgentSettings.
type settingsAgents struct{ *SettingsStore }

func (a settingsAgents) Agents() []ports.AgentView {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.AgentView, len(a.agents))
	copy(out, a.agents)
	return out
}

func (a settingsAgents) Apply(_ context.Context, scope ports.Scope, e ports.AgentEdit) (ports.SaveHandle, error) {
	return a.newSaveHandle(func() error { return a.applyAgent(scope, e) }), nil
}

// agentSourceToScope maps a resolved agent's trust origin to the ports.Scope
// its settings-screen row groups under: config.AgentSourceUser (loaded from
// ~/.mivia/agents/) is ScopeUser, config.AgentSourceWorkspace (loaded from
// <workspace>/.agents/agents/) is ScopeProject. Mirrors how settingsSkills
// already keys its Global/Project split on skills.Origin.
func agentSourceToScope(src config.AgentSource) ports.Scope {
	if src == config.AgentSourceWorkspace {
		return ports.ScopeProject
	}
	return ports.ScopeUser
}

// agentsDirForScope resolves the on-disk agents directory a scope writes to
// or removes from: ScopeUser is ~/.mivia/agents/, ScopeProject is
// <workspace>/.agents/agents/. Mirrors settingsSkills.skillsDirectory.
func agentsDirForScope(scope ports.Scope) string {
	if scope == ports.ScopeUser {
		return config.UserAgentsDir()
	}
	return config.WorkspaceAgentsDir("")
}

func (s *SettingsStore) findAgent(name string) int {
	for i := range s.agents {
		if s.agents[i].Name == name {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) applyAgent(scope ports.Scope, e ports.AgentEdit) error {
	switch v := e.(type) {
	case ports.UpsertAgent:
		v.Agent.Scope = scope
		if i := s.findAgent(v.Agent.Name); i >= 0 {
			s.agents[i] = v.Agent
		} else {
			s.agents = append(s.agents, v.Agent)
		}
		if dir := agentsDirForScope(scope); dir != "" {
			_ = config.WriteAgentFile(dir, agentViewToSettings(v.Agent), "")
		}
	case ports.RemoveAgent:
		if v.Name == ports.DefaultAgentName {
			return fmt.Errorf("the default agent %q cannot be removed", ports.DefaultAgentName)
		}
		i := s.findAgent(v.Name)
		if i < 0 {
			return fmt.Errorf("agent %q not found", v.Name)
		}
		// Remove from the row's own scope, not the caller-supplied one: a
		// stray scope argument must never delete the wrong file, the same
		// guard settingsSkills.remove derives from sk.Origin rather than
		// trusting its own scope parameter blindly.
		rowScope := s.agents[i].Scope
		s.agents = append(s.agents[:i], s.agents[i+1:]...)
		if dir := agentsDirForScope(rowScope); dir != "" {
			_ = config.RemoveAgentFile(dir, v.Name)
		}
	default:
		return fmt.Errorf("unknown agent edit %T", e)
	}
	return nil
}
