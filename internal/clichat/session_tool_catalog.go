package clichat

import (
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// sessionToolSpec is one dispatcher-owned session tool in the catalog.
type sessionToolSpec struct {
	// Name is the wire name the model sees.
	Name string
	// New returns an instance whose static schema
	// (Name/Description/Parameters) is faithful for advertising. The returned
	// tool must read no runtime state in those three methods beyond the passed
	// immutable agent-registry snapshot (nil where the schema needs none).
	New func(agentReg *agents.AgentRegistry) tools.Tool
	// DeferredOnly gates advertising: true means the tool ships only when this
	// binding defers something (load_tools discovers the deferred index; with
	// nothing deferred there is nothing to discover).
	DeferredOnly bool
}

// sessionToolCatalog is the single source of truth for the dispatcher-owned
// session tools: their names, their registration order, and their advertised
// schemas. Order is load-bearing - it mirrors the registration order in
// newSessionDispatcherCore (delegation, orchestration, messaging, ledger, then
// load_tools) so the advertised tail lands in the same position the dispatcher
// actually registers the tools; OpenAI-compatible providers invalidate their
// implicit prompt cache on any tools[] change, INCLUDING a reorder.
//
// The catalog exists because these tools are registered onto the scoped
// execution registry AFTER the root binding's advertised union is built from
// the pre-scope base (plan tools-advertising/01), so a union built from base
// alone ships none of them and the compiled prompt's instructions to use them
// can never be followed on the turn they are read. The advertised wire array
// appends this catalog's schemas instead (advertisedToolSpecs), which is also
// why scripts/verify_agent_config.py derives its non-deferrable tool set from
// this file: a prompt may name these freely precisely because every root
// binding advertises them.
//
// read_skill_resource is deliberately absent: it is injected per skill
// activation (InjectSkillResourceTool) into a skill-scoped clone, not
// registered by the session dispatcher, so no root binding advertises it.
var sessionToolCatalog = []sessionToolSpec{
	{Name: "dispatch_tasks", New: func(agentReg *agents.AgentRegistry) tools.Tool {
		return cliorchestrate.NewDispatchTasksToolForAdvertising(agentReg)
	}},
	{Name: "inspect_agents", New: func(*agents.AgentRegistry) tools.Tool { return cliorchestrate.NewInspectAgentsToolZero() }},
	{Name: "join_run", New: func(*agents.AgentRegistry) tools.Tool { return cliorchestrate.NewJoinRunToolZero() }},
	{Name: "cancel_run", New: func(*agents.AgentRegistry) tools.Tool { return cliorchestrate.NewCancelRunToolZero() }},
	{Name: "post_message", New: func(*agents.AgentRegistry) tools.Tool { return &postMessageTool{} }},
	{Name: "run_messages", New: func(*agents.AgentRegistry) tools.Tool { return &runMessagesTool{} }},
	{Name: "send_to_task", New: func(*agents.AgentRegistry) tools.Tool { return &sendToTaskTool{} }},
	{Name: "ledger_read", New: func(*agents.AgentRegistry) tools.Tool { return &ledgerReadTool{} }},
	{Name: "list_run_events", New: func(*agents.AgentRegistry) tools.Tool { return &listRunEventsTool{} }},
	{Name: "read_output", New: func(*agents.AgentRegistry) tools.Tool { return &readOutputTool{} }},
	{Name: "load_tools", New: func(*agents.AgentRegistry) tools.Tool { return cliagents.NewLoadToolsTool(nil, nil) }, DeferredOnly: true},
}

// advertisedSessionToolSpecs renders the catalog's advertised schemas for one
// binding. agentReg is the binding's immutable resolved agent snapshot; it is
// what lets dispatch_tasks advertise its REAL agent enum and roster prose at
// turn zero instead of a degraded empty enum (agentNames(nil) returns
// []string{}, never JSON null). DeferredOnly entries ship only when the plan
// defers something. Every entry's Name/Description/Parameters reads no
// runtime state beyond that immutable snapshot.
func advertisedSessionToolSpecs(plan toolTierPlan, agentReg *agents.AgentRegistry) []provider.ToolSpec {
	reg := tools.NewRegistry()
	for _, spec := range sessionToolCatalog {
		if spec.DeferredOnly && !plan.Deferred() {
			continue
		}
		reg.Register(spec.New(agentReg))
	}
	return reg.OpenAITools()
}
