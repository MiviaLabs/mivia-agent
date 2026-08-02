package subagents

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// guardTool is a plain registry member for scope assertions.
type guardTool struct{ name string }

func (g guardTool) Name() string               { return g.name }
func (g guardTool) Description() string        { return g.name }
func (g guardTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (g guardTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

type guardPrivilegedTool struct{ guardTool }

func (guardPrivilegedTool) Privileged() {}

func guardRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(guardTool{name: "read_file"})
	reg.Register(guardTool{name: "grep"})
	reg.Register(guardTool{name: "write_file"})
	reg.Register(guardPrivilegedTool{guardTool{name: "load_tools"}})
	reg.Register(guardPrivilegedTool{guardTool{name: "read_output"}})
	reg.Register(guardTool{name: "dispatch_tasks"})
	return reg
}

func registryNames(reg *tools.Registry) []string {
	out := make([]string, 0, len(reg.List()))
	for _, tool := range reg.List() {
		out = append(out, tool.Name())
	}
	return out
}

// TestRestrictedRegistryNeverReExpandsPastItsInput pins the production
// contract of the no-Allowlist spawn path (plan tools/05 §4.3, 51.05 §4.2).
// MultiStepHandler.restrictedRegistry passes no allowlist, so its safety rests
// entirely on FullRegistry having been pre-scoped by the caller. This asserts
// the two properties that makes it depend on: the result is a subset of the
// input, and it can never grow.
func TestRestrictedRegistryNeverReExpandsPastItsInput(t *testing.T) {
	full := guardRegistry(t)
	preScoped := tools.ScopedRegistry(full, tools.ScopeOptions{
		Mode:      tools.ScopeSpawned,
		Allowlist: map[string]struct{}{"read_file": {}, "grep": {}},
	})
	h := &MultiStepHandler{FullRegistry: preScoped}

	got := registryNames(h.restrictedRegistry())
	if want := []string{"read_file", "grep"}; !slices.Equal(got, want) {
		t.Fatalf("spawned registry = %v, want %v", got, want)
	}
	for _, name := range registryNames(full) {
		if slices.Contains(got, name) {
			continue
		}
		if _, present := preScoped.Get(name); present {
			t.Fatalf("tool %q was dropped by the caller's scope but reappeared", name)
		}
	}
}

// TestRestrictedRegistryDropsPrivilegedAndDelegationTools proves the spawn
// boundary holds even when a caller hands over an unscoped full registry:
// session-control tools (including load_tools, which mutates the root tool
// surface) and the mandatory delegation denylist never reach a nested agent.
func TestRestrictedRegistryDropsPrivilegedAndDelegationTools(t *testing.T) {
	h := &MultiStepHandler{FullRegistry: guardRegistry(t)}
	got := registryNames(h.restrictedRegistry())
	if want := []string{"read_file", "grep", "write_file"}; !slices.Equal(got, want) {
		t.Fatalf("spawned registry = %v, want %v", got, want)
	}
	for _, denied := range []string{"load_tools", "read_output", "dispatch_tasks"} {
		if slices.Contains(got, denied) {
			t.Fatalf("privileged/denylisted tool %q leaked into the spawned registry", denied)
		}
	}
}
