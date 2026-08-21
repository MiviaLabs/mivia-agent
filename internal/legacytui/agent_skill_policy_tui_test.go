package legacytui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// skillScopeAgent is a package-local copy of internal/cli's helper of the
// same name (agent_skill_policy_test.go): cli's staying skill-policy tests
// need their own copy.
func skillScopeAgent(name string, skillList *[]string, toolNames ...string) *agents.ResolvedAgent {
	a := &agents.ResolvedAgent{
		Name:           name,
		EffectiveTools: append([]string(nil), toolNames...),
		Skills:         skillList,
	}
	return a
}

// policyTestTool is a minimal tools.Tool for live-registry scope tests.
// Package-local copy of internal/cli's helper of the same name.
type policyTestTool struct{ name string }

func (t policyTestTool) Name() string               { return t.name }
func (t policyTestTool) Description() string        { return t.name }
func (t policyTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t policyTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// Plan 43 phase 2: direct slash activation uses the same policy seam as routed
// tasks. A skill with an unmet declared tool must not start a turn and must not
// inject a resource reader.
func TestSlashSkillUnmetToolRequirementDoesNotActivate(t *testing.T) {
	allowed := []string{"review"}
	agent := skillScopeAgent("dev", &allowed, "read_file") // write_file missing
	state := &cli.AgentSessionState{SkillScope: cli.SkillScopeFromAgent(agent)}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	skillReg := skills.NewRegistry()
	_ = skillReg.Register(skills.Definition{
		Name: "review", UserInvocable: true, Tools: []string{"read_file", "write_file"},
	})
	session.SetBindingSkillRegistry(skillReg)
	m := &TUIModel{session: session, toolsOn: true, agentState: state}
	def, _ := skillReg.Get("review")
	m.startSkillAI(SkillSlashSpec{definition: def, args: "", display: "/review"})
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
	state := &cli.AgentSessionState{SkillScope: cli.SkillScopeFromAgent(agent)}
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
	m.startSkillAI(SkillSlashSpec{definition: def, args: "", display: "/review"})
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
	state := &cli.AgentSessionState{SkillScope: cli.SkillScopeFromAgent(agent)}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	session.Tools.Register(policyTestTool{name: "read_file"})
	session.SetBindingSkillRegistry(skillReg)
	m := &TUIModel{session: session, toolsOn: true, agentState: state}
	m.startSkillAI(SkillSlashSpec{definition: def, args: "", display: "/review"})
	if m.waiting {
		t.Fatal("resource skill with unmet declared tool must not activate")
	}
	if _, exists := session.Tools.Get(tools.SkillResourceToolName); exists {
		t.Fatal("unmet resource skill must not inject read_skill_resource")
	}
}

// TestFilterSkillsForScopeRemovesDisallowedSlashSkills tests that skills the
// selected agent may not invoke are removed from the TUI slash catalog.
func TestFilterSkillsForScopeRemovesDisallowedSlashSkills(t *testing.T) {
	registry := skills.NewRegistry()
	_ = registry.Register(skills.Definition{Name: "blocked-skill", UserInvocable: true})
	empty := []string{}
	filtered := cli.FilterSkillsForScope(registry, cli.SkillScopeFromAgent(skillScopeAgent("locked", &empty, "read_file")))
	session := chat.NewSession(&config.Resolved{}, nullCompleter{})
	session.SetBindingSkillRegistry(filtered)
	m := &TUIModel{session: session}
	if _, _, ok := m.skillSlashTurn("/blocked-skill"); ok {
		t.Fatal("disallowed skill remained invocable through TUI slash routing")
	}
}
