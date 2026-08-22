package cli

// cliagents_wiring.go wires cliagents function variables to their cli
// implementations. Each variable points back into cli so the dependency
// direction stays inward (cliagents never imports cli). The init function
// here runs at process start in every binary that imports cli.

import (
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
)

func init() {
	cliagents.NewSessionDispatcherVar = NewSessionDispatcher
	cliagents.RemainderSpoolFromRegistryVar = RemainderSpoolFromRegistry
	cliagents.WireWorkflowToolOptionsVar = cliworkflow.WireWorkflowToolOptions
	cliagents.BuiltInSlashTokensVar = builtInSlashTokenSet
	cliagents.SummaryWiringVar = summaryWiring
	cliagents.AdvertisedSessionToolSpecsVar = advertisedSessionToolSpecs
	cliagents.ContextDispatcherForVar = contextDispatcherFor
}

// builtInSlashTokenSet returns the set of reserved slash command names.
// Used by cliagents.LoadSessionSkills to reject skill names that collide.
func builtInSlashTokenSet() map[string]struct{} {
	cmds := builtInSlashCommands()
	out := make(map[string]struct{}, len(cmds))
	for _, cmd := range cmds {
		out[cmd.Name] = struct{}{}
	}
	return out
}
