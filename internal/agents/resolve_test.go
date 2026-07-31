package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func strp(s string) *string { return &s }
func slicep(s ...string) *[]string {
	v := append([]string(nil), s...)
	return &v
}

func baseOpts() ResolveOptions {
	return ResolveOptions{
		Global: config.AgentsGlobal{
			FailOnEmptyToolset: true,
		},
		KnownTools: knownToolSet(tools.AllToolNames()),
	}
}

type stubTool struct{ name string }

func (s stubTool) Name() string               { return s.name }
func (s stubTool) Description() string        { return s.name }
func (s stubTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestAgentResolve_InheritsPool(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "parent", Source: config.AgentSourceUser, Path: "parent.toml",
			Spec: config.AgentFileSpec{
				Name: strp("parent"), Description: strp("p"),
				Tools: slicep("read_file", "grep", "glob"),
			},
		},
		{
			Name: "child", Source: config.AgentSourceUser, Path: "child.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("c"),
				Inherits: strp("parent"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	child, ok := reg.Get("child")
	if !ok {
		t.Fatal("child missing")
	}
	if len(child.EffectiveTools) != 3 {
		t.Fatalf("inherited tools = %v", child.EffectiveTools)
	}
}

func TestAgentResolve_ToolsAddExtendsParent(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "parent", Source: config.AgentSourceUser, Path: "parent.toml",
			Spec: config.AgentFileSpec{
				Name: strp("parent"), Description: strp("p"),
				Tools: slicep("read_file", "grep"),
			},
		},
		{
			Name: "child", Source: config.AgentSourceUser, Path: "child.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("c"),
				Inherits: strp("parent"),
				ToolsAdd: slicep("write_file"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	child, _ := reg.Get("child")
	// M1: tools_add extends, does not replace.
	found := map[string]bool{}
	for _, n := range child.EffectiveTools {
		found[n] = true
	}
	for _, want := range []string{"read_file", "grep", "write_file"} {
		if !found[want] {
			t.Errorf("missing %q in %v", want, child.EffectiveTools)
		}
	}
	if len(child.EffectiveTools) != 3 {
		t.Fatalf("tools = %v, want parent∪delta", child.EffectiveTools)
	}
}

func TestAgentResolve_ToolsRemove(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "parent", Source: config.AgentSourceUser, Path: "p.toml",
			Spec: config.AgentFileSpec{
				Name: strp("parent"), Description: strp("p"),
				Tools: slicep("read_file", "grep", "write_file"),
			},
		},
		{
			Name: "child", Source: config.AgentSourceUser, Path: "c.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("c"),
				Inherits:    strp("parent"),
				ToolsRemove: slicep("write_file"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	child, _ := reg.Get("child")
	for _, n := range child.EffectiveTools {
		if n == "write_file" {
			t.Fatal("write_file should have been removed")
		}
	}
}

func TestAgentResolve_InheritanceCycle(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
			Spec: config.AgentFileSpec{Name: strp("a"), Description: strp("a"), Inherits: strp("b"), Tools: slicep("read_file")},
		},
		{
			Name: "b", Source: config.AgentSourceUser, Path: "b.toml",
			Spec: config.AgentFileSpec{Name: strp("b"), Description: strp("b"), Inherits: strp("a"), Tools: slicep("read_file")},
		},
	}
	_, _, err := ResolveAll(inputs, baseOpts())
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestAgentResolve_EmptyToolsetRefused(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "empty", Source: config.AgentSourceUser, Path: "e.toml",
			Spec: config.AgentFileSpec{
				Name: strp("empty"), Description: strp("e"),
				Tools: slicep(),
			},
		},
	}
	_, _, err := ResolveAll(inputs, baseOpts())
	if err == nil || !strings.Contains(err.Error(), "empty toolset") {
		t.Fatalf("want empty toolset error, got %v", err)
	}
}

func TestAgentResolve_CompiledFallbackNotParent(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "child", Source: config.AgentSourceUser, Path: "c.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("c"),
				Inherits: strp("default"),
				Tools:    slicep("read_file"),
			},
		},
	}
	_, _, err := ResolveAll(inputs, baseOpts())
	if err == nil || !strings.Contains(err.Error(), "not a selectable") {
		t.Fatalf("want non-inheritable parent error, got %v", err)
	}
}

func TestValidateAgainstCatalogue_UnknownToolName(t *testing.T) {
	err := ValidateAgainstCatalogue("readfile", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("want unknown tool error, got %v", err)
	}
	inputs := []ResolveInput{
		{
			Name: "typo", Source: config.AgentSourceUser, Path: "t.toml",
			Spec: config.AgentFileSpec{
				Name: strp("typo"), Description: strp("t"),
				Tools: slicep("readfile"),
			},
		},
	}
	_, _, err = ResolveAll(inputs, baseOpts())
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("want catalogue fatal, got %v", err)
	}
}

func TestAgentAllowlistIntersectsDisabledTools(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
			Spec: config.AgentFileSpec{
				Name: strp("a"), Description: strp("a"),
				Tools: slicep("read_file", "run_command"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Get("a")
	live := tools.NewRegistry()
	live.Register(stubTool{name: "read_file"})
	kept, disabled := IntersectWithRegistry(agent.EffectiveTools, live)
	if len(kept) != 1 || kept[0] != "read_file" {
		t.Fatalf("kept = %v", kept)
	}
	if len(disabled) != 1 || disabled[0] != "run_command" {
		t.Fatalf("disabled = %v", disabled)
	}
}

func TestResolve_EvalOrder(t *testing.T) {
	// Allowlist includes a mandatory-denylist tool; it must still be denied
	// with reason "mandatory denylist" (M2).
	inputs := []ResolveInput{
		{
			Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
			Spec: config.AgentFileSpec{
				Name: strp("a"), Description: strp("a"),
				Tools: slicep("read_file", "dispatch_tasks"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Get("a")
	for _, n := range agent.EffectiveTools {
		if n == "dispatch_tasks" {
			t.Fatal("mandatory denylist tool must not appear in effective tools")
		}
	}
	reason := EvalOrderReason("dispatch_tasks", []string{"read_file", "dispatch_tasks"}, nil, nil)
	if reason != "mandatory denylist" {
		t.Fatalf("reason = %q, want mandatory denylist", reason)
	}
}

func TestWorkspaceAgentCannotShadowUserAgent(t *testing.T) {
	// Shadowing is handled at discovery; resolve only sees one name.
	// Simulate post-discovery: only user agent present.
	inputs := []ResolveInput{
		{
			Name: "researcher", Source: config.AgentSourceUser, Path: "/user/researcher.toml",
			Spec: config.AgentFileSpec{
				Name: strp("researcher"), Description: strp("user"),
				Tools: slicep("read_file"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("researcher")
	if a.Provenance.Source != config.AgentSourceUser {
		t.Fatal("user provenance required")
	}
	if len(a.EffectiveTools) != 1 || a.EffectiveTools[0] != "read_file" {
		t.Fatalf("tools = %v", a.EffectiveTools)
	}
}

func TestUserGuardrailsCannotBeLoosenedByWorkspaceAgent(t *testing.T) {
	// User requires explicit tools; workspace-style loosen attempt is ignored
	// because global comes only from user config.
	user := config.AgentsGlobal{RequireExplicitTools: true, FailOnEmptyToolset: true}
	ws := config.AgentsGlobal{RequireExplicitTools: false, FailOnEmptyToolset: false}
	merged := TightenGuardrails(user, ws)
	if !merged.RequireExplicitTools {
		t.Fatal("workspace must not loosen require_explicit_tools")
	}
	if !merged.FailOnEmptyToolset {
		t.Fatal("workspace must not loosen fail_on_empty_toolset")
	}

	// With require_explicit_tools, omitting tools yields empty → refused.
	opts := baseOpts()
	opts.Global.RequireExplicitTools = true
	inputs := []ResolveInput{
		{
			Name: "ws", Source: config.AgentSourceWorkspace, Path: "ws.toml",
			Spec: config.AgentFileSpec{
				Name: strp("ws"), Description: strp("w"),
				// no tools
			},
		},
	}
	_, _, err := ResolveAll(inputs, opts)
	if err == nil || !strings.Contains(err.Error(), "empty toolset") {
		t.Fatalf("want empty toolset under require_explicit_tools, got %v", err)
	}
}

func TestAgentDescriptionSanitized(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
			Spec: config.AgentFileSpec{
				Name:        strp("a"),
				Description: strp("evil \"quote\" and \x01control and " + strings.Repeat("x", 300)),
				Tools:       slicep("read_file"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("a")
	if strings.Contains(a.Description, "\"") || strings.Contains(a.Description, "\x01") {
		t.Fatalf("description not sanitized: %q", a.Description)
	}
	if len(a.Description) > descriptionMaxLen {
		t.Fatalf("description length %d > max", len(a.Description))
	}
}

func TestAgentNameCollidesWithSkill(t *testing.T) {
	opts := baseOpts()
	opts.SkillNames = map[string]struct{}{"researcher": {}}
	inputs := []ResolveInput{
		{
			Name: "researcher", Source: config.AgentSourceUser, Path: "r.toml",
			Spec: config.AgentFileSpec{
				Name: strp("researcher"), Description: strp("r"),
				Tools: slicep("read_file"),
			},
		},
	}
	_, _, err := ResolveAll(inputs, opts)
	if err == nil || !strings.Contains(err.Error(), "skill") {
		t.Fatalf("want skill collision, got %v", err)
	}
}

func TestAgentNameCollidesWithReservedHandler(t *testing.T) {
	opts := baseOpts()
	opts.ReservedHandlers = subagents.ReservedHandlerNames()
	inputs := []ResolveInput{
		{
			Name: "multi_step", Source: config.AgentSourceUser, Path: "m.toml",
			Spec: config.AgentFileSpec{
				Name: strp("multi_step"), Description: strp("m"),
				Tools: slicep("read_file"),
			},
		},
	}
	_, _, err := ResolveAll(inputs, opts)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("want reserved collision, got %v", err)
	}
}

func TestResolvedAgentImmutableClone(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
			Spec: config.AgentFileSpec{
				Name: strp("a"), Description: strp("a"),
				Tools: slicep("read_file", "grep"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("a")
	a.EffectiveTools[0] = "mutated"
	b, _ := reg.Get("a")
	if b.EffectiveTools[0] == "mutated" {
		t.Fatal("registry agent was mutated through returned slice")
	}
}

func TestSelectUnknownListsAvailable(t *testing.T) {
	inputs := []ResolveInput{
		{
			Name: "researcher", Source: config.AgentSourceUser, Path: "r.toml",
			Spec: config.AgentFileSpec{
				Name: strp("researcher"), Description: strp("r"),
				Tools: slicep("read_file"),
			},
		},
	}
	reg, _, err := ResolveAll(inputs, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Select(reg, "nope")
	if err == nil || !strings.Contains(err.Error(), "researcher") {
		t.Fatalf("error should list available agents, got %v", err)
	}
}
