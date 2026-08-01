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
