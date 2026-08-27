package cli

// cliagents_aliases.go re-exports types that moved to internal/cliagents so
// staying cli files compile without per-file import updates. Use the
// cliagents-qualified form in new code. These aliases are intentional shims
// while the extraction stabilises; remove them when cli has been updated fully.

import cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"

// Type aliases for types moved to internal/cliagents.
type (
	AgentBinding            = cliagents.AgentBinding
	AgentCatalogView        = cliagents.AgentCatalogView
	AgentCatalogRow         = cliagents.AgentCatalogRow
	AgentListRow            = cliagents.AgentListRow
	AgentLoadResult         = cliagents.AgentLoadResult
	AgentSessionContext     = cliagents.AgentSessionContext
	AgentSkillScope         = cliagents.AgentSkillScope
	AgentSessionState       = cliagents.AgentSessionState
	ContextDispatcherWiring = cliagents.ContextDispatcherWiring
	SchemaMass              = cliagents.SchemaMass
	SessionDispatcherOpts   = cliagents.SessionDispatcherOpts
	ToolTierPlan            = cliagents.ToolTierPlan
)

// agentBinding is the unexported alias used within legacy cli files.
type agentBinding = cliagents.AgentBinding

// schemaMass is the unexported alias used within legacy cli files.
type schemaMass = cliagents.SchemaMass

// agentLoadResult is the unexported alias used within legacy cli files.
type agentLoadResult = cliagents.AgentLoadResult

// agentCatalogView is the unexported alias.
type agentCatalogView = cliagents.AgentCatalogView

// toolTierPlan is the unexported alias.
type toolTierPlan = cliagents.ToolTierPlan

// agentSessionContext is the unexported alias.
type agentSessionContext = cliagents.AgentSessionContext

// ErrAgentWallClockExceeded is re-exported from cliagents for callers inside
// this package that cannot import cliagents directly (e.g. test files that
// use the cli package name).
var ErrAgentWallClockExceeded = cliagents.ErrAgentWallClockExceeded

// AgentSurface is re-exported from cliagents for cross-package test use.
type AgentSurface = cliagents.AgentSurface
