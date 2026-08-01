package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func skillScopeAgent(name string, skillList *[]string, toolNames ...string) *agents.ResolvedAgent {
	a := &agents.ResolvedAgent{
		Name:           name,
		EffectiveTools: append([]string(nil), toolNames...),
		Skills:         skillList,
	}
	return a
}

func TestAgentSkillAllowlist_PerInstance(t *testing.T) {
	// Two independent scopes must not share mutable state.
	empty := []string{}
	a := skillScopeFromAgent(skillScopeAgent("a", &empty, "read_file"))
	b := skillScopeFromAgent(skillScopeAgent("b", nil, "read_file", "grep"))
	if !a.restricted || b.restricted {
		t.Fatalf("scopes diverged: a.restricted=%v b.restricted=%v", a.restricted, b.restricted)
	}
	if err := a.checkSkill("bug-audit", nil); err == nil {
		t.Fatal("agent a with empty skills must reject")
	}
	if err := b.checkSkill("bug-audit", nil); err != nil {
		t.Fatalf("agent b with omitted skills must allow: %v", err)
	}
}

func TestSkillCannotBypassAgentSelection(t *testing.T) {
	// Selecting a skill cannot bypass the selected agent's allowlist.
	empty := []string{}
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{Name: "bug-audit", Tools: []string{"read_file"}})
	reg := agents.NewRegistry()
	if err := reg.Publish(*skillScopeAgent("locked", &empty, "read_file")); err != nil {
		t.Fatal(err)
	}

	tool := &dispatchTasksTool{skillReg: skillReg, agentReg: reg, cfg: config.DefaultSubagentConfig}
	_, err := tool.buildTasks([]struct {
		ID             string   `json:"id"`
		Prompt         string   `json:"prompt"`
		DependsOn      []string `json:"depends_on,omitempty"`
		Agent          string   `json:"agent"`
		Skill          string   `json:"skill,omitempty"`
		TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	}{{
		ID: "t1", Prompt: "audit", Agent: "locked", Skill: "bug-audit",
	}}, 30)
	if err == nil || !strings.Contains(err.Error(), "may not invoke") {
		t.Fatalf("skill selection must not bypass allowlist, got %v", err)
	}

	spawn := &spawnAgentTool{skillReg: skillReg, agentReg: reg, cfg: config.DefaultSubagentConfig}
	_, err = spawn.buildSpawnTasks([]spawnTaskParams{{
		ID: "t1", Agent: "locked", Skill: "bug-audit", Prompt: "audit",
	}}, runtime.Caller{})
	if err == nil || !strings.Contains(err.Error(), "may not invoke") {
		t.Fatalf("spawn name skill selection must not bypass allowlist, got %v", err)
	}
}

func TestSkillToolsSubsetOfAgentTools(t *testing.T) {
	allowed := []string{"audit"}
	scope := skillScopeFromAgent(skillScopeAgent("narrow", &allowed, "read_file"))
	if err := scope.checkSkill("audit", []string{"read_file"}); err != nil {
		t.Fatal(err)
	}
	err := scope.checkSkill("audit", []string{"read_file", "run_command"})
	if err == nil || !strings.Contains(err.Error(), "run_command") {
		t.Fatalf("tools subset must fail closed, got %v", err)
	}
}

func TestConcurrentAgentSkillInstances(t *testing.T) {
	empty := []string{}
	allowed := []string{"s1"}
	scopes := []agentSkillScope{
		skillScopeFromAgent(skillScopeAgent("a", &empty, "read_file")),
		skillScopeFromAgent(skillScopeAgent("b", &allowed, "read_file")),
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := scopes[i%2]
			err := s.checkSkill("s1", []string{"read_file"})
			if i%2 == 0 && err == nil {
				errs <- fmt.Errorf("empty allowlist accepted skill")
			}
			if i%2 == 1 && err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentSkillBindingSurvivesModelSwitch(t *testing.T) {
	// skillScopeFromAgent is pure: rebuilding from the same selected agent
	// after a model switch yields the same policy (snapshot immutability).
	allowed := []string{"bug-audit"}
	selected := skillScopeAgent("researcher", &allowed, "read_file", "grep")
	before := skillScopeFromAgent(selected)
	// Simulate model switch rebuild from the same selected agent pointer clone.
	clone := selected.Clone()
	after := skillScopeFromAgent(&clone)
	if before.restricted != after.restricted || before.agentName != after.agentName {
		t.Fatalf("scope drifted across rebuild: before=%+v after=%+v", before, after)
	}
	if err := after.checkSkill("bug-audit", []string{"read_file"}); err != nil {
		t.Fatal(err)
	}
	if err := after.checkSkill("other", nil); err == nil {
		t.Fatal("allowlist must still reject other after rebuild")
	}
}

func TestResumeRechecksAgentAccess(t *testing.T) {
	// gatedSkillHandler re-checks scope on every Invoke (resume path).
	empty := []string{}
	scope := skillScopeFromAgent(skillScopeAgent("locked", &empty, "read_file"))
	inner := &stubHandler{out: json.RawMessage(`"ok"`)}
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

	// Widen scope (new instance after agent switch) — still independent.
	open := skillScopeFromAgent(skillScopeAgent("open", nil, "read_file"))
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

type stubHandler struct {
	out   json.RawMessage
	calls int
}

func (s *stubHandler) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	s.calls++
	return s.out, nil
}

func TestMutation_EmptySkillsNotTreatedAsAll(t *testing.T) {
	// Mutation proof: treating [] as all would break this assertion.
	empty := []string{}
	scope := skillScopeFromAgent(skillScopeAgent("x", &empty))
	if !scope.restricted {
		t.Fatal("empty skills slice must be restricted")
	}
	if SkillAllowedNilSemanticsBroken(scope) {
		t.Fatal("mutation: [] treated as all")
	}
}

// SkillAllowedNilSemanticsBroken is the inverted check a buggy implementation
// would pass (nil and empty both allow). Used only as a mutation oracle.
func SkillAllowedNilSemanticsBroken(scope agentSkillScope) bool {
	// Correct: empty is restricted. Broken mutation: restricted=false for empty.
	return !scope.restricted && len(scope.allowed) == 0
}

func TestMutation_DropAllowlistCheckWouldPass(t *testing.T) {
	empty := []string{}
	scope := skillScopeFromAgent(skillScopeAgent("x", &empty, "read_file"))
	// Correct path rejects.
	if err := scope.checkSkill("secret-skill", nil); err == nil {
		t.Fatal("allowlist check missing")
	}
	// Document that skipping the check is the defect under test.
	if dropAllowlistCheck(scope, "secret-skill") {
		// This branch is the broken path; must not be how production behaves.
		t.Log("mutation oracle: dropAllowlistCheck always true")
	}
	if dropAllowlistCheck(scope, "secret-skill") == (scope.checkSkill("secret-skill", nil) == nil) {
		// If they agree, production is broken (check always allows).
		if scope.checkSkill("secret-skill", nil) == nil {
			t.Fatal("production allowlist check is no-op")
		}
	}
}

func dropAllowlistCheck(agentSkillScope, string) bool { return true }

func TestMutation_SkipToolsSubsetWouldPass(t *testing.T) {
	allowed := []string{"s"}
	scope := skillScopeFromAgent(skillScopeAgent("x", &allowed, "read_file"))
	if err := scope.checkSkill("s", []string{"run_command"}); err == nil {
		t.Fatal("tools subset check missing")
	}
	if skipToolsSubset() {
		t.Log("mutation oracle: skipToolsSubset always true")
	}
}

func skipToolsSubset() bool { return true }

func TestFilterSkillsForScope(t *testing.T) {
	reg := skills.NewRegistry()
	_ = reg.Register(skills.Definition{Name: "a"})
	_ = reg.Register(skills.Definition{Name: "b"})
	allowed := []string{"a"}
	scope := skillScopeFromAgent(skillScopeAgent("x", &allowed))
	filtered := filterSkillsForScope(reg, scope)
	if _, ok := filtered.Get("a"); !ok {
		t.Fatal("allowed skill missing")
	}
	if _, ok := filtered.Get("b"); ok {
		t.Fatal("disallowed skill leaked into filtered registry")
	}
}

func TestSkillScopeFromAgent_NilIsOpen(t *testing.T) {
	scope := skillScopeFromAgent(nil)
	if scope.restricted {
		t.Fatal("nil agent must allow all skills (compiled default root)")
	}
	if err := scope.checkSkill("any", []string{"read_file"}); err != nil {
		t.Fatal(err)
	}
}

func TestSkillScopeZeroValueIsOpen(t *testing.T) {
	// Dispatchers constructed without SkillScope (legacy/tests) must not deny all.
	var scope agentSkillScope
	if err := scope.checkSkill("review", nil); err != nil {
		t.Fatalf("zero-value scope must allow skills: %v", err)
	}
}

// TestUserSkillSurvivesProjectShadowWhenWorkspaceGateOff pins the dual-origin
// gate fix: a project skill of the same name must not erase the user skill when
// load_workspace_config is false.
func TestUserSkillSurvivesProjectShadowWhenWorkspaceGateOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	writeSkill := func(dir, name, body string) {
		t.Helper()
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	userSkills := filepath.Join(home, ".mivia", "skills")
	writeSkill(userSkills, "shared", "---\nname: shared\ndescription: user\n---\nuser body\n")
	writeSkill(filepath.Join(root, ".mivia", "skills"), "shared", "---\nname: shared\ndescription: project\n---\nproject body\n")

	// Gate off: must not load project sources at all.
	reg, _, err := loadSessionSkills(root, false)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("shared")
	if !ok {
		t.Fatal("user skill must remain when gate is off")
	}
	if def.Origin != skills.OriginUser {
		t.Fatalf("origin=%q want user", def.Origin)
	}
	if !strings.Contains(def.Instructions, "user body") {
		t.Fatalf("expected user skill body, got %q", def.Instructions)
	}

	// Gate on: project may shadow (existing merge behavior).
	regOn, _, err := loadSessionSkills(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defOn, ok := regOn.Get("shared")
	if !ok || defOn.Origin != skills.OriginProject {
		t.Fatalf("gate on should prefer project, got %#v ok=%v", defOn, ok)
	}
}

// TestSkillToolsSubsetNonVacuousFixture proves a skill with declared tools fails
// when the agent omits one of them (plan 06 phase 01 guard).
func TestSkillToolsSubsetNonVacuousFixture(t *testing.T) {
	allowed := []string{"review"}
	scope := skillScopeFromAgent(skillScopeAgent("dev", &allowed, "read_file"))
	// Fixture skill declares tools the agent lacks.
	skillTools := []string{"read_file", "write_file"}
	err := scope.checkSkill("review", skillTools)
	if err == nil {
		t.Fatal("non-vacuous tools subset must reject when agent omits a skill tool")
	}
}

func TestNewSessionDispatcher_SkillScopeGatesRegistration(t *testing.T) {
	reg := tools.NewRegistry()
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{
		Name: "allowed-skill", Tools: []string{},
	})
	_ = skillReg.Register(skills.Definition{
		Name: "blocked-skill", Tools: []string{},
	})
	allowed := []string{"allowed-skill"}
	scope := skillScopeFromAgent(skillScopeAgent("agent", &allowed, "read_file"))
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:   reg,
		Completer:  nullCompleter{},
		Model:      "test",
		Config:     config.DefaultSubagentConfig,
		SkillReg:   skillReg,
		SkillScope: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !d.Has(runtime.Subagent, "allowed-skill") {
		t.Fatal("allowed skill must be registered")
	}
	if d.Has(runtime.Subagent, "blocked-skill") {
		t.Fatal("blocked skill must not be registered")
	}
}

func TestFilterSkillsForScopeRemovesDisallowedSlashSkills(t *testing.T) {
	registry := skills.NewRegistry()
	_ = registry.Register(skills.Definition{Name: "blocked-skill", UserInvocable: true})
	empty := []string{}
	filtered := filterSkillsForScope(registry, skillScopeFromAgent(skillScopeAgent("locked", &empty, "read_file")))
	session := chat.NewSession(&config.Resolved{}, nullCompleter{})
	session.SetBindingSkillRegistry(filtered)
	m := &tuiModel{session: session}
	if _, _, ok := m.skillSlashTurn("/blocked-skill"); ok {
		t.Fatal("disallowed skill remained invocable through TUI slash routing")
	}
}
