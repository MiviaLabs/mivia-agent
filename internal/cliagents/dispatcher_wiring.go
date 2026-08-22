package cliagents

import (
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
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
// without importing cli.
var WireWorkflowToolOptionsVar func(*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool)

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
var AdvertisedSessionToolSpecsVar func(ToolTierPlan) []provider.ToolSpec

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions.
// NewSessionDispatcherVar must be non-nil; wired by internal/cli/cliagents_wiring.go.
func NewSessionDispatcher(opts SessionDispatcherOpts) (*runtime.Dispatcher, error) {
	if NewSessionDispatcherVar == nil {
		return nil, fmt.Errorf("NewSessionDispatcherVar is not wired")
	}
	return NewSessionDispatcherVar(opts)
}

// registerSessionTool registers a privileged session-owned tool on both the
// dispatcher and the tool registry. It fails fast if the tool name is already
// present so registration conflicts surface at startup rather than at runtime.
func registerSessionTool(d *runtime.Dispatcher, reg *tools.Registry, tool tools.Tool) error {
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
	return registerSessionTool(d, opts.Registry, NewLoadToolsTool(opts.Session, opts.DeferredTools))
}
