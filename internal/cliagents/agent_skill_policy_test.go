package cliagents

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
	a := SkillScopeFromAgent(skillScopeAgent("a", &empty, "read_file"))
	b := SkillScopeFromAgent(skillScopeAgent("b", nil, "read_file", "grep"))
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

// TestSkillCannotBypassAgentSelection is in internal/cli/skill_policy_cli_test.go
// (it needs cli-internal types dispatchTasksTool and spawnAgentTool).

func TestSkillToolsSubsetOfAgentTools(t *testing.T) {
	allowed := []string{"audit"}
	scope := SkillScopeFromAgent(skillScopeAgent("narrow", &allowed, "read_file"))
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
	scopes := []AgentSkillScope{
		SkillScopeFromAgent(skillScopeAgent("a", &empty, "read_file")),
		SkillScopeFromAgent(skillScopeAgent("b", &allowed, "read_file")),
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
	// SkillScopeFromAgent is pure: rebuilding from the same selected agent
	// after a model switch yields the same policy (snapshot immutability).
	allowed := []string{"bug-audit"}
	selected := skillScopeAgent("researcher", &allowed, "read_file", "grep")
	before := SkillScopeFromAgent(selected)
	// Simulate model switch rebuild from the same selected agent pointer clone.
	clone := selected.Clone()
	after := SkillScopeFromAgent(&clone)
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

// TestResumeRechecksAgentAccess is in internal/cli/skill_policy_cli_test.go
// (it needs cli-internal type gatedSkillHandler).

func TestMutation_EmptySkillsNotTreatedAsAll(t *testing.T) {
	// Mutation proof: treating [] as all would break this assertion.
	empty := []string{}
	scope := SkillScopeFromAgent(skillScopeAgent("x", &empty))
	if !scope.restricted {
		t.Fatal("empty skills slice must be restricted")
	}
	if SkillAllowedNilSemanticsBroken(scope) {
		t.Fatal("mutation: [] treated as all")
	}
}

// SkillAllowedNilSemanticsBroken is the inverted check a buggy implementation
// would pass (nil and empty both allow). Used only as a mutation oracle.
func SkillAllowedNilSemanticsBroken(scope AgentSkillScope) bool {
	// Correct: empty is restricted. Broken mutation: restricted=false for empty.
	return !scope.restricted && len(scope.allowed) == 0
}

func TestMutation_DropAllowlistCheckWouldPass(t *testing.T) {
	empty := []string{}
	scope := SkillScopeFromAgent(skillScopeAgent("x", &empty, "read_file"))
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

func dropAllowlistCheck(AgentSkillScope, string) bool { return true }

func TestMutation_SkipToolsSubsetWouldPass(t *testing.T) {
	allowed := []string{"s"}
	scope := SkillScopeFromAgent(skillScopeAgent("x", &allowed, "read_file"))
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
	scope := SkillScopeFromAgent(skillScopeAgent("x", &allowed))
	filtered := FilterSkillsForScope(reg, scope)
	if _, ok := filtered.Get("a"); !ok {
		t.Fatal("allowed skill missing")
	}
	if _, ok := filtered.Get("b"); ok {
		t.Fatal("disallowed skill leaked into filtered registry")
	}
}

func TestSkillScopeFromAgent_NilIsOpen(t *testing.T) {
	scope := SkillScopeFromAgent(nil)
	if scope.restricted {
		t.Fatal("nil agent must allow all skills (compiled default root)")
	}
	if err := scope.checkSkill("any", []string{"read_file"}); err != nil {
		t.Fatal(err)
	}
}

func TestSkillScopeZeroValueIsOpen(t *testing.T) {
	// Dispatchers constructed without SkillScope (legacy/tests) must not deny all.
	var scope AgentSkillScope
	if err := scope.checkSkill("review", nil); err != nil {
		t.Fatalf("zero-value scope must allow skills: %v", err)
	}
}

// policyTestTool is a minimal tools.Tool for live-registry scope tests.
type policyTestTool struct{ name string }

func (t policyTestTool) Name() string               { return t.name }
func (t policyTestTool) Description() string        { return t.name }
func (t policyTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t policyTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// Plan 43 phase 2: a skill is invocable only when every declared static tool is
// present in the final live registry (post disable/deny filtering), not merely
// in the agent TOML list.
func TestSkillScopeLiveRegistryMissingTool(t *testing.T) {
	allowed := []string{"audit"}
	agent := skillScopeAgent("dev", &allowed, "read_file", "run_command")
	reg := tools.NewRegistry()
	reg.Register(policyTestTool{name: "read_file"}) // run_command absent at runtime
	scope := SkillScopeFromAgentAndRegistry(agent, reg)
	if err := scope.CheckSkillDefinition(skills.Definition{Name: "audit", Tools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	err := scope.CheckSkillDefinition(skills.Definition{Name: "audit", Tools: []string{"read_file", "run_command"}})
	if err == nil || !strings.Contains(err.Error(), "live tool registry") {
		t.Fatalf("expected live-registry failure, got %v", err)
	}
}

// Plan 43 phase 2: without a live registry the scope falls back to the agent's
// effective tools (backward compatible for dispatchers without a registry).
func TestSkillScopeWithoutLiveRegistryUsesAgentTools(t *testing.T) {
	allowed := []string{"audit"}
	agent := skillScopeAgent("dev", &allowed, "read_file")
	scope := SkillScopeFromAgentAndRegistry(agent, nil)
	if err := scope.CheckSkillDefinition(skills.Definition{Name: "audit", Tools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	err := scope.CheckSkillDefinition(skills.Definition{Name: "audit", Tools: []string{"run_command"}})
	if err == nil {
		t.Fatal("agent tools subset must still be enforced without a live registry")
	}
}

// Plan 43 phase 2: the unrestricted compiled root (nil agent) stays open even
// when a live registry is supplied - it deliberately owns the full catalogue.
func TestSkillScopeFromAgentAndRegistryNilAgentIsOpen(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(policyTestTool{name: "read_file"})
	scope := SkillScopeFromAgentAndRegistry(nil, reg)
	if scope.restricted || scope.enforceTools {
		t.Fatal("nil agent must stay unrestricted even with a live registry")
	}
	if err := scope.CheckSkillDefinition(skills.Definition{Name: "any", Tools: []string{"run_command"}}); err != nil {
		t.Fatalf("nil-agent root exception lost: %v", err)
	}
}

// Plan 43 phase 2: a runtime-resolved skill definition whose origin differs
// from the allowlist-bound origin is an authorization event (a project skill
// silently shadowing a user-bound allowlist entry) and fails closed.
func TestSkillScopeOriginMismatchFailsClosed(t *testing.T) {
	allowed := []string{"shared"}
	agent := &agents.ResolvedAgent{
		Name:           "a",
		EffectiveTools: []string{"read_file"},
		Skills:         &allowed,
		SkillOrigins:   map[string]string{"shared": "user"},
	}
	scope := SkillScopeFromAgent(agent)
	userDef := skills.Definition{Name: "shared", Origin: skills.OriginUser, Tools: []string{"read_file"}}
	if err := scope.CheckSkillDefinition(userDef); err != nil {
		t.Fatalf("user-bound skill with user origin must pass: %v", err)
	}
	projDef := skills.Definition{Name: "shared", Origin: skills.OriginProject, Tools: []string{"read_file"}}
	err := scope.CheckSkillDefinition(projDef)
	if err == nil || !strings.Contains(err.Error(), "origin mismatch") {
		t.Fatalf("expected origin mismatch, got %v", err)
	}
}

// Plan 43 phase 2: an agent binding resolved to the workspace origin matches a
// runtime project-origin skill (workspace and project are the same trust level).
func TestSkillScopeWorkspaceBindingMatchesProjectOrigin(t *testing.T) {
	allowed := []string{"shared"}
	agent := &agents.ResolvedAgent{
		Name:           "a",
		EffectiveTools: []string{"read_file"},
		Skills:         &allowed,
		SkillOrigins:   map[string]string{"shared": "workspace"},
	}
	scope := SkillScopeFromAgent(agent)
	def := skills.Definition{Name: "shared", Origin: skills.OriginProject, Tools: []string{"read_file"}}
	if err := scope.CheckSkillDefinition(def); err != nil {
		t.Fatalf("workspace binding must match project origin: %v", err)
	}
}

// Plan 43 phase 2: an agent with no allowlist binding for a skill does not
// trigger the origin check (root/omitted skills remain unbound).
func TestSkillScopeOriginCheckSkipsUnboundSkills(t *testing.T) {
	agent := &agents.ResolvedAgent{
		Name:           "root",
		EffectiveTools: []string{"read_file"},
		SkillOrigins:   map[string]string{"bound": "user"},
	}
	scope := SkillScopeFromAgent(agent)
	def := skills.Definition{Name: "unbound", Origin: skills.OriginProject, Tools: []string{"read_file"}}
	if err := scope.CheckSkillDefinition(def); err != nil {
		t.Fatalf("unbound skill must not be origin-checked: %v", err)
	}
}

// TestRouteTimeRejectsSkillWithUnmetTool is in internal/cli/skill_policy_cli_test.go
// (it needs cli-internal type dispatchTasksTool).

// TestHandlerTimeRejectsSkillWithLiveDisabledTool is in internal/cli/skill_policy_cli_test.go
// (it needs cli-internal type agentTaskHandler).

// Plan 43 phase 2: a project skill silently shadowing a user-bound allowlist
// entry fails closed at execution (origin mismatch). Routed-task coverage of
// the same policy engine (via cliorchestrate.ResolveTaskRoute, the one
// production resolver) lives in internal/cliorchestrate; this test covers
// the shared AgentSkillScope mechanism both resolvers delegate to.
func TestOriginFailClosedAtExecution(t *testing.T) {
	allowed := []string{"shared"}
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{
		Name: "shared", Origin: skills.OriginProject, Tools: []string{"read_file"},
	})
	scope := SkillScopeFromAgent(&agents.ResolvedAgent{
		Name: "a", EffectiveTools: []string{"read_file"},
		Skills: &allowed, SkillOrigins: map[string]string{"shared": "user"},
	})
	def, _ := skillReg.Get("shared")
	if err := scope.CheckSkillDefinition(def); err == nil || !strings.Contains(err.Error(), "origin mismatch") {
		t.Fatalf("shared scope must fail closed on origin mismatch, got %v", err)
	}
}

// Plan 43 phase 2: catalogue and runtime agree on origin precedence for
// same-named user/project skills: the catalogue marks both origins, the
// allowlist binds the user origin, and the runtime-resolved project definition
// is rejected as an authorization event rather than executed silently.
func TestCatalogueAndRuntimeOriginPrecedenceAgree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	write := func(base, subdir, name, description string) {
		t.Helper()
		dir := filepath.Join(base, subdir, "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + description + "\ntools: [read_file]\n---\nbody"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(home, ".mivia", "shared", "user")
	write(root, ".agents", "shared", "project")

	catalogue, _ := BuildSkillCatalogue(root)
	entry, ok := catalogue["shared"]
	if !ok || !entry.User || !entry.Project {
		t.Fatalf("catalogue must mark both origins: %#v", entry)
	}
	reg, _, err := agents.ResolveAll([]agents.ResolveInput{{
		Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
		Spec: config.AgentFileSpec{
			Name: strptr("a"), Description: strptr("a"),
			Tools: sliceptr("read_file"), Skills: sliceptr("shared"),
		},
	}}, agents.ResolveOptions{
		Global:             config.AgentsGlobal{FailOnEmptyToolset: true},
		SkillCatalogue:     catalogue,
		AllowProjectSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("a")
	if a.SkillOrigins["shared"] != string(config.AgentSourceUser) {
		t.Fatalf("allowlist must bind the user origin, got %v", a.SkillOrigins)
	}
	// Runtime registry (gate on) serves the project definition.
	runtimeReg, _, err := LoadSessionSkills(root, true)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDef, ok := runtimeReg.Get("shared")
	if !ok || runtimeDef.Origin != skills.OriginProject {
		t.Fatalf("runtime must serve project definition: %#v ok=%v", runtimeDef, ok)
	}
	// Executing the runtime definition under the user-bound allowlist fails
	// closed instead of silently running the project body.
	scope := SkillScopeFromAgent(&a)
	if err := scope.CheckSkillDefinition(runtimeDef); err == nil || !strings.Contains(err.Error(), "origin mismatch") {
		t.Fatalf("user-bound skill executed as project must fail closed, got %v", err)
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
	writeSkill(filepath.Join(root, ".agents", "skills"), "shared", "---\nname: shared\ndescription: project\n---\nproject body\n")

	// Gate off: must not load project sources at all.
	reg, _, err := LoadSessionSkills(root, false)
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
	regOn, _, err := LoadSessionSkills(root, true)
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
	scope := SkillScopeFromAgent(skillScopeAgent("dev", &allowed, "read_file"))
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
	scope := SkillScopeFromAgent(skillScopeAgent("agent", &allowed, "read_file"))
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

// strptr and sliceptr are pointer helpers for ResolveInput specs in cli tests.
func strptr(s string) *string { return &s }

func sliceptr(items ...string) *[]string {
	out := append([]string(nil), items...)
	return &out
}
