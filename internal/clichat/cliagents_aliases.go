package clichat

// cliagents_aliases.go re-exports types from internal/cliagents that moved
// cli files reference unqualified. Use the cliagents-qualified form in new
// code. These aliases are intentional shims while the extraction stabilises.

import cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"

// Type aliases for types from internal/cliagents.
type (
	AgentBinding            = cliagents.AgentBinding
	AgentSkillScope         = cliagents.AgentSkillScope
	ContextDispatcherWiring = cliagents.ContextDispatcherWiring
	SessionDispatcherOpts   = cliagents.SessionDispatcherOpts
	ToolTierPlan            = cliagents.ToolTierPlan
)

// agentBinding is the unexported alias used within legacy chat files.
type agentBinding = cliagents.AgentBinding

// toolTierPlan is the unexported alias.
type toolTierPlan = cliagents.ToolTierPlan

// AgentSessionState is re-exported from cliagents.
type AgentSessionState = cliagents.AgentSessionState

// agentSessionContext is the unexported alias.
type agentSessionContext = cliagents.AgentSessionContext

// AgentLoadResult is re-exported from cliagents for legacy test files.
type AgentLoadResult = cliagents.AgentLoadResult

// AgentSurface is re-exported from cliagents for cross-package test use.
type AgentSurface = cliagents.AgentSurface

// schemaMass is the unexported alias used within legacy chat files.
type schemaMass = cliagents.SchemaMass
