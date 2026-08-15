package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// sessionToolSpec is one dispatcher-owned session tool in the catalog.
type sessionToolSpec struct {
	// Name is the wire name the model sees.
	Name string
	// New returns a zero-value instance whose static schema
	// (Name/Description/Parameters) is faithful for advertising. The returned
	// tool must read no runtime state in those three methods.
	New func() tools.Tool
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
// activation (injectSkillResourceTool) into a skill-scoped clone, not
// registered by the session dispatcher, so no root binding advertises it.
var sessionToolCatalog = []sessionToolSpec{
	{Name: "delegate", New: func() tools.Tool { return &delegateTool{} }},
	{Name: "dispatch_tasks", New: func() tools.Tool { return &dispatchTasksTool{} }},
	{Name: "spawn_agent", New: func() tools.Tool { return &spawnAgentTool{} }},
	{Name: "inspect_agents", New: func() tools.Tool { return &inspectAgentTool{} }},
	{Name: "join_run", New: func() tools.Tool { return &joinRunTool{} }},
	{Name: "cancel_run", New: func() tools.Tool { return &cancelRunTool{} }},
	{Name: "post_message", New: func() tools.Tool { return &postMessageTool{} }},
	{Name: "run_messages", New: func() tools.Tool { return &runMessagesTool{} }},
	{Name: "send_to_task", New: func() tools.Tool { return &sendToTaskTool{} }},
	{Name: "ledger_read", New: func() tools.Tool { return &ledgerReadTool{} }},
	{Name: "list_run_events", New: func() tools.Tool { return &listRunEventsTool{} }},
	{Name: "read_output", New: func() tools.Tool { return &readOutputTool{} }},
	{Name: "load_tools", New: func() tools.Tool { return &loadToolsTool{} }, DeferredOnly: true},
}

// advertisedSessionToolSpecs renders the catalog's advertised schemas for one
// binding. DeferredOnly entries ship only when the plan defers something. The
// zero-value instances are faithful schema sources: every entry's
// Name/Description/Parameters reads no runtime state (dispatch_tasks and
// spawn_agent degrade to an empty agent enum when their registry is nil -
// agentNames(nil) returns []string{}, never JSON null).
func advertisedSessionToolSpecs(plan toolTierPlan) []provider.ToolSpec {
	reg := tools.NewRegistry()
	for _, spec := range sessionToolCatalog {
		if spec.DeferredOnly && !plan.Deferred() {
			continue
		}
		reg.Register(spec.New())
	}
	return reg.OpenAITools()
}
