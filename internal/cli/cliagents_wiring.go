package cli

// cliagents_wiring.go wires cliagents function variables to their cli
// implementations. Each variable points back into cli so the dependency
// direction stays inward (cliagents never imports cli). The init function
// here runs at process start in every binary that imports cli.

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cli/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli/workflow"
)

func init() {
	agents.NewSessionDispatcherVar = chat.NewSessionDispatcher
	agents.RemainderSpoolFromRegistryVar = chat.RemainderSpoolFromRegistry
	agents.WireWorkflowToolOptionsVar = workflow.WireWorkflowToolOptions
	agents.BuiltInSlashTokensVar = builtInSlashTokenSet
	agents.SummaryWiringVar = chat.SummaryWiring
	agents.AdvertisedSessionToolSpecsVar = chat.AdvertisedSessionToolSpecs
	agents.ContextDispatcherForVar = chat.ContextDispatcherFor
}

// builtInSlashTokenSet returns the set of reserved slash command names.
// Used by agents.LoadSessionSkills to reject skill names that collide.
func builtInSlashTokenSet() map[string]struct{} {
	cmds := chat.BuiltInSlashCommands()
	out := make(map[string]struct{}, len(cmds))
	for _, cmd := range cmds {
		out[cmd.Name] = struct{}{}
	}
	return out
}
