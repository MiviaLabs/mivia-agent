package ports

import "context"

// AgentView is one subagent role definition (.mivia/agents/*.toml).
// SystemPromptChars is a length, never the prompt text - the same
// "(set, N chars)" convention internal/cli/config_cmd.go already uses
// for the same reason: a prompt is content the screen has no business
// echoing wholesale.
type AgentView struct {
	Name              string
	Description       string
	Provider          string
	Model             string
	Tools             []string
	Skills            []string
	MCPServers        []string
	MaxTurns          int
	SystemPromptChars int
	// Scope is the trust origin the definition file loaded from:
	// ScopeUser for ~/.mivia/agents/<name>.md, ScopeProject for
	// <workspace>/.agents/agents/<name>.md. Populated from the resolved
	// agent's Provenance.Source, the same origin split
	// ports.SkillView.Origin already carries for skills.
	Scope Scope
}

// AgentEdit is a closed union of agent-definition mutations.
type AgentEdit interface{ isAgentEdit() }

type UpsertAgent struct{ Agent AgentView }
type RemoveAgent struct{ Name string }

func (UpsertAgent) isAgentEdit() {}
func (RemoveAgent) isAgentEdit() {}

// AgentSettings is the Agents section's read/write surface.
type AgentSettings interface {
	Agents() []AgentView
	Apply(ctx context.Context, scope Scope, e AgentEdit) (SaveHandle, error)
}
