package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// rootScopePlainTool is a non-privileged tool, mirroring run_command which is
// a plain (non-PrivilegedTool) tool in the default registry.
type rootScopePlainTool struct{ name string }

func (t rootScopePlainTool) Name() string               { return t.name }
func (t rootScopePlainTool) Description() string        { return t.name }
func (t rootScopePlainTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t rootScopePlainTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// TestRootScope_DeniedToolExcludedFromAllowlistedRegistry reproduces the
// INV-AG-29 execution-denial requirement for an operator
// [agents.guardrails].mandatory_tool_denylist addition under ScopeRoot: a
// non-privileged tool denied by ExtraDenylist AND absent from the agent
// allowlist must NOT be re-admitted into the scoped root registry.
//
// The ScopeRoot branch of ScopedRegistry registers denylist-named tools
// unconditionally before the allowlist intersection, contradicting its own
// comment ("kept at root only when no allowlist is set, or when listed").
func TestRootScope_DeniedToolExcludedFromAllowlistedRegistry(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(rootScopePlainTool{name: "read_file"})
	reg.Register(rootScopePlainTool{name: "run_command"})

	scoped := tools.ScopedRegistry(reg, tools.ScopeOptions{
		Mode:          tools.ScopeRoot,
		Allowlist:     map[string]struct{}{"read_file": {}},
		ExtraDenylist: []string{"run_command"},
	})

	if _, ok := scoped.Get("run_command"); ok {
		t.Fatalf("denied tool %q re-admitted to root scope despite ExtraDenylist and allowlist exclusion (INV-AG-29)", "run_command")
	}
	if _, ok := scoped.Get("read_file"); !ok {
		t.Fatal("allowlisted read_file must remain in root scope")
	}
}

// An operator's denylist must apply at root even when no agent is selected.
//
// ScopeRoot keeps a denylisted non-privileged tool when no allowlist is set.
// That is deliberate for the COMPILED list - delegation tools stay available
// to the root agent - but it swept the operator's additions along with it, so
// [agents.guardrails] mandatory_tool_denylist did nothing at all in the
// commonest configuration there is: no agent selected, so ScopedRootRegistry
// returns the registry untouched and no allowlist is ever built.
//
// The compiled list is a rule about what a SPAWNED agent may not reach. An
// operator addition is a rule about what THIS INSTALLATION may not run, and
// the root agent is exactly who it is aimed at.
func TestRootScope_OperatorDenialAppliesWithNoAllowlist(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(rootScopePlainTool{name: "read_file"})
	reg.Register(rootScopePlainTool{name: "run_command"})

	scoped := tools.ScopedRegistry(reg, tools.ScopeOptions{
		Mode:          tools.ScopeRoot,
		ExtraDenylist: []string{"run_command"},
		// No Allowlist: no agent is selected.
	})

	if _, ok := scoped.Get("run_command"); ok {
		t.Error("an operator's mandatory_tool_denylist entry is ignored at root " +
			"when no agent is selected - which is the default session - so the " +
			"guardrail exists in the config and nowhere else")
	}
	if _, ok := scoped.Get("read_file"); !ok {
		t.Fatal("a tool nobody denied was dropped")
	}
}

// ...and the COMPILED list keeps its root exemption, which is what makes
// delegation work for the root agent.
func TestRootScope_CompiledDenialKeepsItsRootExemption(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(rootScopePlainTool{name: "dispatch_tasks"})

	scoped := tools.ScopedRegistry(reg, tools.ScopeOptions{Mode: tools.ScopeRoot})

	if _, ok := scoped.Get("dispatch_tasks"); !ok {
		t.Error("a COMPILED denylist name was dropped at root; that list bounds " +
			"what a SPAWNED agent may reach, and the root agent keeps delegation")
	}
}

// rootScopePrivilegedTool is a privileged (session-owned) tool: dispatch_tasks,
// post_message, read_output and load_tools are all of this shape.
type rootScopePrivilegedTool struct{ name string }

func (t rootScopePrivilegedTool) Name() string               { return t.name }
func (t rootScopePrivilegedTool) Description() string        { return t.name }
func (t rootScopePrivilegedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t rootScopePrivilegedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
func (t rootScopePrivilegedTool) Privileged() {}

// An operator's denial must outrank the privileged marker.
//
// scopeAdmits returned true for a PrivilegedTool BEFORE consulting the
// operator's denylist, so `mandatory_tool_denylist = ["read_output"]` (or
// post_message, dispatch_tasks, load_tools) did nothing at all: every
// session-owned tool is privileged by construction.
//
// The COMPILED denylist keeps its root exemption - it bounds what a SPAWNED
// agent may reach and the root agent is meant to keep delegation. An operator
// addition is a rule about what this installation may run, and a tool being
// session-owned is not a reason to exempt it from that.
func TestRootScope_OperatorDenialOutranksThePrivilegedMarker(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(rootScopePrivilegedTool{name: "read_output"})
	reg.Register(rootScopePrivilegedTool{name: "dispatch_tasks"})

	scoped := tools.ScopedRegistry(reg, tools.ScopeOptions{
		Mode:          tools.ScopeRoot,
		ExtraDenylist: []string{"read_output"},
	})

	if _, ok := scoped.Get("read_output"); ok {
		t.Error("an operator denied a session-owned tool and it survived, because " +
			"the privileged marker was checked first; every session tool is " +
			"privileged, so the guardrail could never reach any of them")
	}
	if _, ok := scoped.Get("dispatch_tasks"); !ok {
		t.Error("a COMPILED denylist name lost its root exemption; the root agent " +
			"keeps delegation")
	}
}
