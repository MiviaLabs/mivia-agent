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
	Scope             Scope
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
