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
	_, err := tool.buildTasks([]dispatchTaskParam{{
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

	// Widen scope (new instance after agent switch) - still independent.
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
	scope := skillScopeFromAgentAndRegistry(agent, reg)
	if err := scope.checkSkillDefinition(skills.Definition{Name: "audit", Tools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	err := scope.checkSkillDefinition(skills.Definition{Name: "audit", Tools: []string{"read_file", "run_command"}})
	if err == nil || !strings.Contains(err.Error(), "live tool registry") {
		t.Fatalf("expected live-registry failure, got %v", err)
	}
}

// Plan 43 phase 2: without a live registry the scope falls back to the agent's
// effective tools (backward compatible for dispatchers without a registry).
func TestSkillScopeWithoutLiveRegistryUsesAgentTools(t *testing.T) {
	allowed := []string{"audit"}
	agent := skillScopeAgent("dev", &allowed, "read_file")
	scope := skillScopeFromAgentAndRegistry(agent, nil)
	if err := scope.checkSkillDefinition(skills.Definition{Name: "audit", Tools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	err := scope.checkSkillDefinition(skills.Definition{Name: "audit", Tools: []string{"run_command"}})
	if err == nil {
		t.Fatal("agent tools subset must still be enforced without a live registry")
	}
}

// Plan 43 phase 2: the unrestricted compiled root (nil agent) stays open even
// when a live registry is supplied - it deliberately owns the full catalogue.
func TestSkillScopeFromAgentAndRegistryNilAgentIsOpen(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(policyTestTool{name: "read_file"})
	scope := skillScopeFromAgentAndRegistry(nil, reg)
	if scope.restricted || scope.enforceTools {
		t.Fatal("nil agent must stay unrestricted even with a live registry")
	}
	if err := scope.checkSkillDefinition(skills.Definition{Name: "any", Tools: []string{"run_command"}}); err != nil {
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
	scope := skillScopeFromAgent(agent)
	userDef := skills.Definition{Name: "shared", Origin: skills.OriginUser, Tools: []string{"read_file"}}
	if err := scope.checkSkillDefinition(userDef); err != nil {
		t.Fatalf("user-bound skill with user origin must pass: %v", err)
	}
	projDef := skills.Definition{Name: "shared", Origin: skills.OriginProject, Tools: []string{"read_file"}}
	err := scope.checkSkillDefinition(projDef)
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
	scope := skillScopeFromAgent(agent)
	def := skills.Definition{Name: "shared", Origin: skills.OriginProject, Tools: []string{"read_file"}}
	if err := scope.checkSkillDefinition(def); err != nil {
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
	scope := skillScopeFromAgent(agent)
	def := skills.Definition{Name: "unbound", Origin: skills.OriginProject, Tools: []string{"read_file"}}
	if err := scope.checkSkillDefinition(def); err != nil {
		t.Fatalf("unbound skill must not be origin-checked: %v", err)
	}
}

// Plan 43 phase 2: direct slash activation uses the same policy seam as routed
// tasks. A skill with an unmet declared tool must not start a turn and must not
// inject a resource reader.
func TestSlashSkillUnmetToolRequirementDoesNotActivate(t *testing.T) {
	allowed := []string{"review"}
	agent := skillScopeAgent("dev", &allowed, "read_file") // write_file missing
	state := &agentSessionState{SkillScope: skillScopeFromAgent(agent)}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{
		Name: "review", UserInvocable: true, Tools: []string{"read_file", "write_file"},
	})
	session.SetBindingSkillRegistry(skillReg)
	m := &tuiModel{session: session, toolsOn: true, agentState: state}
	def, _ := skillReg.Get("review")
	m.startSkillAI(skillSlashSpec{definition: def, args: "", display: "/review"})
	if m.waiting {
		t.Fatal("slash skill with an unmet declared tool must not start a turn")
	}
	if _, exists := session.Tools.Get(tools.SkillResourceToolName); exists {
		t.Fatal("unmet slash skill must not inject a resource reader")
	}
}

// Plan 43 phase 2: an allowed slash skill still activates through the gate.
func TestSlashSkillAllowedStillActivates(t *testing.T) {
	allowed := []string{"review"}
	agent := skillScopeAgent("dev", &allowed, "read_file")
	state := &agentSessionState{SkillScope: skillScopeFromAgent(agent)}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	session.Tools.Register(policyTestTool{name: "read_file"})
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{
		Name: "review", UserInvocable: true, Tools: []string{"read_file"},
	})
	session.SetBindingSkillRegistry(skillReg)
	m := newTUIModel(session, &config.Resolved{Model: "model"}, true)
	m.agentState = state
	def, _ := skillReg.Get("review")
	m.startSkillAI(skillSlashSpec{definition: def, args: "", display: "/review"})
	if !m.waiting {
		t.Fatal("allowed slash skill must start a turn")
	}
}

// Plan 43 phase 2: a resource-bearing slash skill with an unmet declared tool
// must not activate and must not inject read_skill_resource into the session
// tool surface.
func TestSlashResourceSkillUnmetToolDoesNotInjectReader(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "review")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: review\ntools: [read_file, write_file]\n---\nLoad the template.",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Required template\"\n",
		"template.md":    "PRIVATE RESOURCE",
	} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skillReg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	def, ok := skillReg.Get("review")
	if !ok || len(def.Resources) != 1 {
		t.Fatalf("resource skill missing: %#v ok=%v", def, ok)
	}
	allowed := []string{"review"}
	agent := skillScopeAgent("dev", &allowed, "read_file") // write_file missing
	state := &agentSessionState{SkillScope: skillScopeFromAgent(agent)}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	session.Tools.Register(policyTestTool{name: "read_file"})
	session.SetBindingSkillRegistry(skillReg)
	m := &tuiModel{session: session, toolsOn: true, agentState: state}
	m.startSkillAI(skillSlashSpec{definition: def, args: "", display: "/review"})
	if m.waiting {
		t.Fatal("resource skill with unmet declared tool must not activate")
	}
	if _, exists := session.Tools.Get(tools.SkillResourceToolName); exists {
		t.Fatal("unmet resource skill must not inject read_skill_resource")
	}
}

// Plan 43 phase 2: route-time recheck - a skill whose declared tools exceed the
// selected task agent's effective tools is rejected at dispatch, not at run.
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
	tool := &dispatchTasksTool{skillReg: skillReg, agentReg: reg, cfg: config.DefaultSubagentConfig}
	_, err := tool.buildTasks([]dispatchTaskParam{{ID: "t1", Prompt: "audit", Agent: "researcher", Skill: "audit"}}, 30)
	if err == nil || !strings.Contains(err.Error(), "run_command") {
		t.Fatalf("route-time must reject unmet skill tool, got %v", err)
	}
}

// Plan 43 phase 2: handler-time recheck - a skill whose declared tool is absent
// from the final live registry (disabled at runtime) is rejected even when the
// agent TOML lists it.
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
	full.Register(policyTestTool{name: "read_file"}) // run_command disabled
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

// Plan 43 phase 2: a project skill silently shadowing a user-bound allowlist
// entry fails closed at execution (origin mismatch), for both routed tasks and
// the shared scope.
func TestOriginFailClosedAtExecution(t *testing.T) {
	allowed := []string{"shared"}
	reg := agents.NewRegistry()
	if err := reg.Publish(agents.ResolvedAgent{
		Name: "a", Description: "A", EffectiveTools: []string{"read_file"},
		Skills: &allowed, SkillOrigins: map[string]string{"shared": "user"},
	}); err != nil {
		t.Fatal(err)
	}
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{
		Name: "shared", Origin: skills.OriginProject, Tools: []string{"read_file"},
	})
	if _, err := resolveTaskRoute(reg, skillReg, "a", "shared"); err == nil || !strings.Contains(err.Error(), "origin mismatch") {
		t.Fatalf("routed task must fail closed on origin mismatch, got %v", err)
	}
	scope := skillScopeFromAgent(&agents.ResolvedAgent{
		Name: "a", EffectiveTools: []string{"read_file"},
		Skills: &allowed, SkillOrigins: map[string]string{"shared": "user"},
	})
	def, _ := skillReg.Get("shared")
	if err := scope.checkSkillDefinition(def); err == nil || !strings.Contains(err.Error(), "origin mismatch") {
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
	write := func(base, name, description string) {
		t.Helper()
		dir := filepath.Join(base, ".mivia", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + description + "\ntools: [read_file]\n---\nbody"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(home, "shared", "user")
	write(root, "shared", "project")

	catalogue, _ := buildSkillCatalogue(root)
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
	runtimeReg, _, err := loadSessionSkills(root, true)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDef, ok := runtimeReg.Get("shared")
	if !ok || runtimeDef.Origin != skills.OriginProject {
		t.Fatalf("runtime must serve project definition: %#v ok=%v", runtimeDef, ok)
	}
	// Executing the runtime definition under the user-bound allowlist fails
	// closed instead of silently running the project body.
	scope := skillScopeFromAgent(&a)
	if err := scope.checkSkillDefinition(runtimeDef); err == nil || !strings.Contains(err.Error(), "origin mismatch") {
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

// TestFilterSkillsForScopeRemovesDisallowedSlashSkills tests that skills the
// selected agent may not invoke are removed from the TUI slash catalog.
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

// strptr and sliceptr are pointer helpers for ResolveInput specs in cli tests.
func strptr(s string) *string { return &s }

func sliceptr(items ...string) *[]string {
	out := append([]string(nil), items...)
	return &out
}
