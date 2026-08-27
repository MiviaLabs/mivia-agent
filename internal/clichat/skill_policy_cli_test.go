package clichat

// skill_policy_cli_test.go contains the skill policy tests that need internal
// cli types (cliorchestrate.DispatchTasksToolForTest, gatedSkillHandler, agentTaskHandler). These
// tests were separated from internal/cliagents/agent_skill_policy_test.go
// because the cli-internal types they exercise cannot be accessed from
// outside the cli package.

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// skillScopeAgentCLI builds a minimal ResolvedAgent for skill-policy tests.
func skillScopeAgentCLI(name string, skillList *[]string, toolNames ...string) *agents.ResolvedAgent {
	return &agents.ResolvedAgent{
		Name:           name,
		EffectiveTools: append([]string(nil), toolNames...),
		Skills:         skillList,
	}
}

// skillPolicyTestTool is a minimal tools.Tool for live-registry scope tests.
type skillPolicyTestTool struct{ name string }

func (t skillPolicyTestTool) Name() string               { return t.name }
func (t skillPolicyTestTool) Description() string        { return t.name }
func (t skillPolicyTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t skillPolicyTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// skillPolicyStubHandler is a minimal runtime.Handler for skill-gate tests.
type skillPolicyStubHandler struct {
	out   json.RawMessage
	calls int
}

func (s *skillPolicyStubHandler) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	s.calls++
	return s.out, nil
}

func TestSkillCannotBypassAgentSelection(t *testing.T) {
	// Selecting a skill cannot bypass the selected agent's allowlist.
	empty := []string{}
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{Name: "bug-audit", Tools: []string{"read_file"}})
	reg := agents.NewRegistry()
	if err := reg.Publish(*skillScopeAgentCLI("locked", &empty, "read_file")); err != nil {
		t.Fatal(err)
	}

	tool := cliorchestrate.NewDispatchTasksToolForSkillPolicy(skillReg, reg, config.DefaultSubagentConfig)
	_, err := tool.BuildTasksForTest([]cliorchestrate.DispatchTaskParamForTest{{
		ID: "t1", Prompt: "audit", Agent: "locked", Skill: "bug-audit",
	}}, 30)
	if err == nil || !strings.Contains(err.Error(), "may not invoke") {
		t.Fatalf("skill selection must not bypass allowlist, got %v", err)
	}
}

func TestResumeRechecksAgentAccess(t *testing.T) {
	// gatedSkillHandler re-checks scope on every Invoke (resume path).
	empty := []string{}
	scope := cliagents.SkillScopeFromAgent(skillScopeAgentCLI("locked", &empty, "read_file"))
	inner := &skillPolicyStubHandler{out: json.RawMessage(`"ok"`)}
	h := &gatedSkillHandler{
		scope: scope,
		skill: skills.Definition{Name: "bug-audit", Tools: []string{"read_file"}},
		inner: inner,
	}
	_, err := h.Invoke(context.Background(), runtime.Request{})
	if err == nil || !strings.Contains(err.Error(), "may not invoke") {
		t.Fatalf("resume invoke must recheck allowlist, got %v", err)
	}
	if inner.calls != 0 {
		t.Fatal("inner handler must not run when policy rejects")
	}

	// Widen scope (new instance after agent switch) - still independent.
	open := cliagents.SkillScopeFromAgent(skillScopeAgentCLI("open", nil, "read_file"))
	h2 := &gatedSkillHandler{
		scope: open,
		skill: skills.Definition{Name: "bug-audit", Tools: []string{"read_file"}},
		inner: inner,
	}
	out, err := h2.Invoke(context.Background(), runtime.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `"ok"` {
		t.Fatalf("out=%s", out)
	}
}

func TestRouteTimeRejectsSkillWithUnmetTool(t *testing.T) {
	reg := agents.NewRegistry()
	if err := reg.Publish(agents.ResolvedAgent{
		Name: "researcher", Description: "Research",
		EffectiveTools: []string{"read_file"},
		Skills:         &[]string{"audit"},
	}); err != nil {
		t.Fatal(err)
	}
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{Name: "audit", Tools: []string{"read_file", "run_command"}})
	tool := cliorchestrate.NewDispatchTasksToolForSkillPolicy(skillReg, reg, config.DefaultSubagentConfig)
	_, err := tool.BuildTasksForTest([]cliorchestrate.DispatchTaskParamForTest{{ID: "t1", Prompt: "audit", Agent: "researcher", Skill: "audit"}}, 30)
	if err == nil || !strings.Contains(err.Error(), "run_command") {
		t.Fatalf("route-time must reject unmet skill tool, got %v", err)
	}
}

func TestHandlerTimeRejectsSkillWithLiveDisabledTool(t *testing.T) {
	allowed := []string{"audit"}
	definition := agents.ResolvedAgent{
		Name:           "dev",
		EffectiveTools: []string{"read_file", "run_command"},
		Skills:         &allowed,
	}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewRegistry()
	full.Register(skillPolicyTestTool{name: "read_file"}) // run_command disabled
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{Name: "audit", Tools: []string{"read_file", "run_command"}})
	h := &agentTaskHandler{definition: definition, digest: digest, full: full, opts: SessionDispatcherOpts{SkillReg: skillReg}}
	err = h.ValidateRequest(runtime.Request{
		Name: "dev", AgentName: "dev", AgentDigest: digest, Skill: "audit",
	})
	if err == nil || !strings.Contains(err.Error(), "live tool registry") {
		t.Fatalf("handler-time must reject live-disabled skill tool, got %v", err)
	}
}
