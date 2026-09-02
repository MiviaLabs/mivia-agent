package cliagents

import (
	"errors"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/agents"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// errNilDispatcherDep is returned when a required dependency is nil.
var errNilDispatcherDep = errors.New("nil session dispatcher dependency")

// NewSessionDispatcherVar is wired at process start by
// internal/cli/cliagents_wiring.go to cli's NewSessionDispatcher.
// Moving code (agent_switch.go, model_binding.go) calls this instead of
// importing cli directly.
var NewSessionDispatcherVar func(SessionDispatcherOpts) (*runtime.Dispatcher, error)

// RemainderSpoolFromRegistryVar is wired at process start by
// internal/cli/cliagents_wiring.go to cli's RemainderSpoolFromRegistry.
// cli's read_output.go owns the unexported readOutputTool type and cannot
// move, so the spool builder stays there.
var RemainderSpoolFromRegistryVar func(*tools.Registry) *remainder.Spool

// WireWorkflowToolOptionsVar is wired at process start by
// internal/cli/cliagents_wiring.go to cli's wireWorkflowToolOptions.
// chat_workspace.go (moving) calls this to wire the workflow-engine event bus
// without importing cli. The ledger repository is the owning session's
// orchestration repo (AgentSessionState.LedgerRepo); nil keeps child-run
// registration skipped.
var WireWorkflowToolOptionsVar func(*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool, ledger.LedgerRepository)

// BuiltInSlashTokensVar is wired by internal/cli/cliagents_wiring.go to return
// the set of reserved slash tokens from cli's builtInSlashCommands. Used by
// loadSessionSkills in model_binding.go to reject skill names that collide with
// built-in slash commands.
var BuiltInSlashTokensVar func() map[string]struct{}

// SummaryWiringVar is wired by internal/cli/cliagents_wiring.go to cli's
// summaryWiring. Used by refreshSummarizerAfterModelSwitch in model_binding.go.
var SummaryWiringVar func(*chat.Session, *config.Resolved) (*contextmgr.Summarizer, contextstate.PolicySnapshot, bool)

// AdvertisedSessionToolSpecsVar is wired by internal/cli/cliagents_wiring.go to
// cli's advertisedSessionToolSpecs. Used by advertisedToolSpecs in tool_tiers.go
// to append the session-owned dispatcher tools to the advertised wire array.
// The *agents.AgentRegistry argument is the binding's immutable resolved
// agent snapshot, passed as data (never a global) so dispatch_tasks can
// advertise its real agent enum and roster at turn zero.
var AdvertisedSessionToolSpecsVar func(ToolTierPlan, *agents.AgentRegistry) []provider.ToolSpec

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions.
// NewSessionDispatcherVar must be non-nil; wired by internal/cli/cliagents_wiring.go.
func NewSessionDispatcher(opts SessionDispatcherOpts) (*runtime.Dispatcher, error) {
	if NewSessionDispatcherVar == nil {
		return nil, fmt.Errorf("NewSessionDispatcherVar is not wired")
	}
	return NewSessionDispatcherVar(opts)
}

// RegisterSessionTool registers a privileged session-owned tool on both the
// dispatcher and the tool registry. It fails fast if the tool name is already
// present so registration conflicts surface at startup rather than at runtime.
// RegisterSessionTool installs a session-owned tool onto the live registry
// and dispatcher.
//
// denylist is the operator's mandatory_tool_denylist. It is required because
// these tools are registered AFTER the registry has been scoped, so no
// earlier filter can see them: dispatch_tasks, post_message, read_output and
// load_tools were all unreachable by an operator's guardrail, whatever it
// said. Registration is the only point at which they can be refused.
func RegisterSessionTool(d *runtime.Dispatcher, reg *tools.Registry, tool tools.Tool, denylist []string) error {
	if tools.OperatorDenialSet(denylist)[tool.Name()] {
		return fmt.Errorf("session tool %q is on the operator's tool denylist", tool.Name())
	}
	if _, privileged := tool.(tools.PrivilegedTool); !privileged {
		return fmt.Errorf("session tool %q must implement tools.PrivilegedTool", tool.Name())
	}
	if _, exists := reg.Get(tool.Name()); exists {
		return fmt.Errorf("session tool %q already registered", tool.Name())
	}
	if d != nil {
		if err := d.RegisterTool(reg, tool); err != nil {
			return fmt.Errorf("register session tool %q: %w", tool.Name(), err)
		}
	}
	reg.Register(tool)
	return nil
}

// registerLoadToolsTool registers the deferred-tool discovery surface.
// It is registered last so it lands after the core block and the admitted
// tail, and only when this binding actually defers something.
func registerLoadToolsTool(d *runtime.Dispatcher, opts SessionDispatcherOpts) error {
	if len(opts.DeferredTools) == 0 {
		return nil
	}
	if opts.Session == nil {
		return fmt.Errorf("deferred tools configured without a session to stage against")
	}
	return RegisterSessionTool(d, opts.Registry, NewLoadToolsTool(opts.Session, opts.DeferredTools), opts.ToolDenylist)
}
