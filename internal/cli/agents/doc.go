// Package agents owns the agent command surfaces and the per-session
// agent bindings: agent definitions, model bindings, tool tiers, MCP
// scope, and memory support.
//
// It may import the runtime agent packages (internal/agents, internal/agent,
// internal/tools, and their peers). The internal/cli and internal/cli/chat
// packages may import it.
package agents
