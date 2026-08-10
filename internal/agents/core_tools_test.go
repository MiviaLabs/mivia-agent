package agents

import (
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func strsPtr(v ...string) *[]string {
	out := slices.Clone(v)
	return &out
}

func resolveCoreFixture(t *testing.T, inputs []ResolveInput) *AgentRegistry {
	t.Helper()
	reg, _, err := ResolveAll(inputs, ResolveOptions{
		Global: config.AgentsGlobal{},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return reg
}

func TestResolveCoreToolsFromTheAgentFile(t *testing.T) {
	reg := resolveCoreFixture(t, []ResolveInput{{
		Name:   "reader",
		Source: config.AgentSourceUser,
		Spec: config.AgentFileSpec{
			Tools:     strsPtr("read_file", "grep", "glob"),
			ToolsCore: strsPtr("read_file"),
		},
	}})
	agent, ok := reg.Get("reader")
	if !ok {
		t.Fatal("reader not resolved")
	}
	if agent.CoreTools == nil {
		t.Fatal("tools_core did not resolve")
	}
	if !slices.Equal(*agent.CoreTools, []string{"read_file"}) {
		t.Fatalf("core tools = %v, want [read_file]", *agent.CoreTools)
	}
}

func TestResolveCoreToolsOmittedStaysNil(t *testing.T) {
	reg := resolveCoreFixture(t, []ResolveInput{{
		Name:   "reader",
		Source: config.AgentSourceUser,
		Spec:   config.AgentFileSpec{Tools: strsPtr("read_file")},
	}})
	agent, _ := reg.Get("reader")
	if agent.CoreTools != nil {
		t.Fatalf("core tools = %v, want nil when tools_core is omitted", *agent.CoreTools)
	}
}

// TestResolveCoreToolsInherit: a child that does not state tools_core inherits
// the parent's decision; one that states it overrides.
func TestResolveCoreToolsInherit(t *testing.T) {
	reg := resolveCoreFixture(t, []ResolveInput{
		{Name: "base", Source: config.AgentSourceUser, Spec: config.AgentFileSpec{
			Tools: strsPtr("read_file", "grep", "glob"), ToolsCore: strsPtr("read_file"),
		}},
		{Name: "heir", Source: config.AgentSourceUser, Spec: config.AgentFileSpec{
			Inherits: strPtr("base"),
		}},
		{Name: "override", Source: config.AgentSourceUser, Spec: config.AgentFileSpec{
			Inherits: strPtr("base"), ToolsCore: strsPtr("read_file", "grep"),
		}},
	})
	heir, _ := reg.Get("heir")
	if heir.CoreTools == nil || !slices.Equal(*heir.CoreTools, []string{"read_file"}) {
		t.Fatalf("inherited core tools = %v, want [read_file]", heir.CoreTools)
	}
	override, _ := reg.Get("override")
	if override.CoreTools == nil || !slices.Equal(*override.CoreTools, []string{"read_file", "grep"}) {
		t.Fatalf("overridden core tools = %v, want [read_file grep]", override.CoreTools)
	}
	// The parent's slice must not be aliased by either child.
	(*override.CoreTools)[0] = "mutated"
	base, _ := reg.Get("base")
	if (*base.CoreTools)[0] != "read_file" {
		t.Fatal("mutating a child's core tools reached the parent")
	}
}

func TestCloneCopiesCoreTools(t *testing.T) {
	original := ResolvedAgent{Name: "a", CoreTools: strsPtr("read_file")}
	clone := original.Clone()
	(*clone.CoreTools)[0] = "mutated"
	if (*original.CoreTools)[0] != "read_file" {
		t.Fatal("Clone aliased CoreTools")
	}
}

func TestCoreToolsChangeDefinitionDigest(t *testing.T) {
	agents := []ResolvedAgent{
		{Name: "a"},
		{Name: "a", CoreTools: strsPtr()},
		{Name: "a", CoreTools: strsPtr("read_file")},
		{Name: "a", CoreTools: strsPtr("grep")},
	}
	seen := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		digest, err := agent.DefinitionDigest()
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := seen[digest]; exists {
			t.Fatalf("CoreTools value %#v did not change the definition digest", agent.CoreTools)
		}
		seen[digest] = struct{}{}
	}
}

func strPtr(s string) *string { return &s }
