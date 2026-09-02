package cliagents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// An operator's denylist must apply at root when NO agent is selected.
//
// This is the default session: no `default` agent in the registry and no
// --agent, `--agent mivia`, or `/agent mivia` mid-session all leave
// selected == nil. ScopedRootRegistry returned the registry untouched in that
// case, so ExtraDenylist never reached tools.ScopedRegistry at all and the
// guardrail did nothing.
//
// A previous fix corrected the inner predicate (scopeAdmits) and left this
// outer early return in place, and its regression test called
// tools.ScopedRegistry DIRECTLY - bypassing the very branch that was still
// broken. The test passed, the commit claimed the class closed, and the
// default configuration stayed wide open. Hence this test, at the layer the
// caller actually uses.
func TestRootScopeAppliesTheOperatorDenylistWithNoAgentSelected(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(denyProbe{name: "read_file"})
	reg.Register(denyProbe{name: "run_command"})

	scoped, _ := ScopedRootRegistry(reg, nil, []string{"run_command"})

	if _, ok := scoped.Get("run_command"); ok {
		t.Error("an operator's mandatory_tool_denylist entry does not apply when " +
			"no agent is selected - which is the DEFAULT session - so the " +
			"guardrail exists in the config and nowhere else")
	}
	if _, ok := scoped.Get("read_file"); !ok {
		t.Fatal("a tool nobody denied was dropped")
	}
}

// The same for the tiered constructor, which delegates here when the tier
// plan is inert - a session with no [tools] core, which is most of them.
func TestTieredRootRegistryAppliesTheDenylistWithNoAgentSelected(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(denyProbe{name: "read_file"})
	reg.Register(denyProbe{name: "run_command"})

	scoped := TieredRootRegistry(reg, nil, []string{"run_command"}, ToolTierPlan{}, nil)

	if _, ok := scoped.Get("run_command"); ok {
		t.Error("the denied tool survives the tiered root registry with no agent " +
			"selected and an inert tier plan; that is the shipped default path")
	}
}

type denyProbe struct{ name string }

func (d denyProbe) Name() string               { return d.name }
func (d denyProbe) Description() string        { return d.name }
func (d denyProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (d denyProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// A session-owned tool must be refusable by the operator.
//
// dispatch_tasks, post_message, read_output and load_tools are registered
// onto the live registry AFTER it has been scoped, so no earlier filter can
// see them - and every one of them is a PrivilegedTool, which scopeAdmits
// used to admit at root before consulting any denylist. The operator's
// guardrail therefore could not reach a single session tool, whatever it
// said. Registration is the only point at which they can be refused.
func TestRegisterSessionToolRefusesAnOperatorDeniedName(t *testing.T) {
	reg := tools.NewRegistry()
	err := RegisterSessionTool(nil, reg, privilegedProbe{name: "read_output"},
		[]string{"read_output"})

	if err == nil {
		t.Error("a session tool the operator denied was registered anyway; it is " +
			"privileged and registered after scoping, so nothing else can refuse it")
	}
	if _, ok := reg.Get("read_output"); ok {
		t.Error("the denied session tool is in the model-visible registry")
	}
}

// The COMPILED denylist must NOT refuse a session tool: it names the
// delegation tools a root session legitimately owns, and folding it in here
// would refuse dispatch_tasks outright.
func TestRegisterSessionToolIgnoresTheCompiledDenylist(t *testing.T) {
	reg := tools.NewRegistry()
	if err := RegisterSessionTool(nil, reg, privilegedProbe{name: "dispatch_tasks"}, nil); err != nil {
		t.Fatalf("a compiled-denylist name is a legitimate session tool: %v", err)
	}
	if _, ok := reg.Get("dispatch_tasks"); !ok {
		t.Error("dispatch_tasks was not registered")
	}
}

type privilegedProbe struct{ name string }

func (p privilegedProbe) Name() string               { return p.name }
func (p privilegedProbe) Description() string        { return p.name }
func (p privilegedProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p privilegedProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
func (p privilegedProbe) Privileged() {}
