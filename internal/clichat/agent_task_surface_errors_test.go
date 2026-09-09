package clichat

// agent_task_surface_errors_test.go covers the refusal branches of
// agent_task_surface.go: the MCP wiring failure, the unrenderable schema
// guard, and every activateSkill rejection.

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestPrepareInvokeSurfaceMCPWiringFailureFailsClosed pins that a failure to
// materialize this subagent's MCP server tools aborts the invocation with a
// wrapped "MCP tools" error, instead of proceeding with a surface silently
// missing the tools the agent declared.
func TestPrepareInvokeSurfaceMCPWiringFailureFailsClosed(t *testing.T) {
	boom := errors.New("stdio server refused")
	var sawServers []string
	h := &agentTaskHandler{
		definition: agents.ResolvedAgent{Name: "dev", EffectiveMCPServers: []string{"docs"}},
		full:       tools.NewRegistry(),
		opts: cliagents.SessionDispatcherOpts{
			EnsureMCPTools: func(servers []string) error {
				sawServers = append([]string(nil), servers...)
				return boom
			},
		},
	}
	prompt, memory, registry, closer, err := h.prepareInvokeSurface(runtime.Request{Name: "dev"})
	if err == nil {
		t.Fatal("prepareInvokeSurface accepted an MCP wiring failure")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the EnsureMCPTools error", err)
	}
	if !strings.HasPrefix(err.Error(), "MCP tools: ") {
		t.Fatalf("err = %q, want the \"MCP tools: \" prefix", err.Error())
	}
	if prompt != "" || memory != "" || registry != nil {
		t.Fatalf("surface = (%q, %q, %v), want all zero on the failure path", prompt, memory, registry)
	}
	if closer == nil {
		t.Fatal("closer = nil; callers defer it unconditionally")
	}
	closer()
	if len(sawServers) != 1 || sawServers[0] != "docs" {
		t.Fatalf("EnsureMCPTools got %v, want the agent's effective server list", sawServers)
	}
}

// TestSchemaSystemAppendixSkipsUnrenderableSchema pins the guard between a nil
// schema (no contract at all) and a schema the host cannot render: neither may
// emit an appendix. Emitting a block with an empty or "null" contract would
// tell the model to satisfy a contract that was never stated.
func TestSchemaSystemAppendixSkipsUnrenderableSchema(t *testing.T) {
	if got := schemaSystemAppendix(nil); got != "" {
		t.Fatalf("schemaSystemAppendix(nil) = %q, want empty", got)
	}
	// A NaN cannot be marshaled, so the rendered contract is empty.
	unrenderable := map[string]any{"type": "object", "maximum": math.NaN()}
	if got := schemaSystemAppendix(unrenderable); got != "" {
		t.Fatalf("schemaSystemAppendix(unrenderable) = %q, want empty", got)
	}
	// A real schema DOES produce an appendix, so the empties above are the
	// guard firing and not a permanently silent renderer.
	real := map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}}
	if got := schemaSystemAppendix(real); got == "" {
		t.Fatal("schemaSystemAppendix(real schema) = empty, want a contract block")
	}
}

// TestActivateSkillRejectsWhenNoSkillRegistry pins that an agent invoked with
// no skill registry attached may not activate any skill: the refusal names the
// agent and the skill rather than silently running with the agent prompt.
func TestActivateSkillRejectsWhenNoSkillRegistry(t *testing.T) {
	h := &agentTaskHandler{definition: agents.ResolvedAgent{Name: "dev"}, full: tools.NewRegistry()}
	reg, prompt, closer, err := h.activateSkill("audit", tools.NewRegistry())
	if err == nil {
		t.Fatal("activateSkill accepted a skill with no registry present")
	}
	if got := err.Error(); got != `agent "dev" may not invoke skill "audit"` {
		t.Fatalf("err = %q, want the may-not-invoke refusal", got)
	}
	if reg != nil || prompt != "" {
		t.Fatalf("activateSkill returned (%v, %q) on the failure path", reg, prompt)
	}
	closer()
}

// TestActivateSkillRejectsUnknownSkill pins that a name absent from the
// registry is refused as unknown rather than activated as an empty skill.
func TestActivateSkillRejectsUnknownSkill(t *testing.T) {
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{Name: "audit"}); err != nil {
		t.Fatal(err)
	}
	h := &agentTaskHandler{
		definition: agents.ResolvedAgent{Name: "dev"},
		full:       tools.NewRegistry(),
		opts:       cliagents.SessionDispatcherOpts{SkillReg: reg},
	}
	_, _, closer, err := h.activateSkill("nope", tools.NewRegistry())
	if err == nil {
		t.Fatal("activateSkill accepted an unregistered skill name")
	}
	if got := err.Error(); got != `unknown skill "nope"` {
		t.Fatalf("err = %q, want the unknown-skill refusal", got)
	}
	closer()
}

// TestActivateSkillRejectsSkillOutsideAgentScope pins the authorization gate:
// a registered skill whose declared tools are absent from the live registry
// must be refused at activation, not activated with a narrower surface than it
// declared.
func TestActivateSkillRejectsSkillOutsideAgentScope(t *testing.T) {
	full := tools.NewRegistry()
	full.Register(skillPolicyTestTool{name: "read_file"})
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{Name: "audit", Tools: []string{"read_file", "run_command"}}); err != nil {
		t.Fatal(err)
	}
	h := &agentTaskHandler{
		definition: agents.ResolvedAgent{Name: "dev", EffectiveTools: []string{"read_file", "run_command"}},
		full:       full,
		opts:       cliagents.SessionDispatcherOpts{SkillReg: reg},
	}
	_, _, closer, err := h.activateSkill("audit", tools.NewRegistry())
	if err == nil {
		t.Fatal("activateSkill accepted a skill whose declared tool is not live")
	}
	if !strings.Contains(err.Error(), "run_command") {
		t.Fatalf("err = %q, want it to name the unmet tool", err.Error())
	}
	closer()
}

// TestActivateSkillSurfacesResourceActivationFailure pins that a skill whose
// declared resources cannot be opened fails the invocation. Activating it
// anyway would hand the model a resource catalogue backed by nothing.
func TestActivateSkillSurfacesResourceActivationFailure(t *testing.T) {
	reg := skills.NewRegistry()
	// A hand-built definition has no on-disk location, so Activate cannot open
	// the declared resource root.
	if err := reg.Register(skills.Definition{
		Name:         "audit",
		Instructions: "audit things",
		Resources:    []skills.ResourceDescriptor{{ID: "checklist"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := &agentTaskHandler{
		definition: agents.ResolvedAgent{Name: "dev"},
		full:       tools.NewRegistry(),
		opts:       cliagents.SessionDispatcherOpts{SkillReg: reg},
	}
	gotReg, prompt, closer, err := h.activateSkill("audit", tools.NewRegistry())
	if err == nil {
		t.Fatal("activateSkill accepted a skill whose resources cannot be opened")
	}
	if !strings.Contains(err.Error(), "skill resources are unavailable") {
		t.Fatalf("err = %q, want the resource-activation failure", err.Error())
	}
	if gotReg != nil || prompt != "" {
		t.Fatalf("activateSkill returned (%v, %q) on the failure path", gotReg, prompt)
	}
	closer()
}

// TestActivateSkillPrependsDescription pins that a skill's description is
// prepended to the executed instructions: the skill's own prompt replaces the
// agent prompt, so dropping the description would lose the only statement of
// what the skill is for.
func TestActivateSkillPrependsDescription(t *testing.T) {
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{
		Name:         "audit",
		Description:  "Hunt reachable defects.",
		Instructions: "Step 1. Read the diff.",
	}); err != nil {
		t.Fatal(err)
	}
	h := &agentTaskHandler{
		definition: agents.ResolvedAgent{Name: "dev"},
		full:       tools.NewRegistry(),
		opts:       cliagents.SessionDispatcherOpts{SkillReg: reg},
	}
	_, prompt, closer, err := h.activateSkill("audit", tools.NewRegistry())
	if err != nil {
		t.Fatalf("activateSkill: %v", err)
	}
	closer()
	if want := "Hunt reachable defects.\n\nStep 1. Read the diff."; prompt != want {
		t.Fatalf("prompt = %q, want %q", prompt, want)
	}

	// With no description the instructions stand alone, so the join above is
	// the description branch and not an unconditional prefix.
	if err := reg.Register(skills.Definition{Name: "plain", Instructions: "Step 1. Read the diff."}); err != nil {
		t.Fatal(err)
	}
	_, plain, closePlain, err := h.activateSkill("plain", tools.NewRegistry())
	if err != nil {
		t.Fatalf("activateSkill(plain): %v", err)
	}
	closePlain()
	if plain != "Step 1. Read the diff." {
		t.Fatalf("prompt = %q, want the bare instructions", plain)
	}
}
