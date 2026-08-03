package cli

// A task routed to an agent must fail before the model is called when the
// requested skill cannot be activated, and a skill's own output schema must
// win over the agent's when the task runs under that skill.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func schemaSkillRegistry(t *testing.T, frontmatter string) *skills.Registry {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\n" + frontmatter + "---\nReview the change.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func newSchemaTaskHandler(t *testing.T, skillReg *skills.Registry) (*agentTaskHandler, string) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	definition := agents.ResolvedAgent{Name: "reviewer", EffectiveTools: []string{}}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	handler := newAgentTaskHandler(definition, digest,
		tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}), runtime.New(runtime.Policy{}),
		SessionDispatcherOpts{
			Completer: &mockDelegateCompleter{name: "test", response: `{"ok":true}`},
			Model:     "model", Config: config.DefaultSubagentConfig, SkillReg: skillReg,
		})
	return handler, digest
}

func TestAgentTaskRefusesASkillItCannotActivate(t *testing.T) {
	handler, digest := newSchemaTaskHandler(t, schemaSkillRegistry(t, ""))
	_, err := handler.Invoke(context.Background(), runtime.Request{
		ID: "task-1", Name: "reviewer", AgentName: "reviewer", AgentDigest: digest,
		Skill: "not-a-skill", Input: json.RawMessage(`"inspect"`),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("err = %v, want an unknown-skill refusal", err)
	}

	// No skill registry at all: the agent may not invoke skills.
	bare, bareDigest := newSchemaTaskHandler(t, nil)
	_, err = bare.Invoke(context.Background(), runtime.Request{
		ID: "task-2", Name: "reviewer", AgentName: "reviewer", AgentDigest: bareDigest,
		Skill: "review", Input: json.RawMessage(`"inspect"`),
	})
	if err == nil || !strings.Contains(err.Error(), "may not invoke skill") {
		t.Fatalf("err = %v, want a skill-permission refusal", err)
	}
}

func TestSkillOutputSchemaWinsOverTheAgentSchema(t *testing.T) {
	skillReg := schemaSkillRegistry(t, `output_schema: '{"type":"object","properties":{"verdict":{"type":"string"}},"required":["verdict"]}'`+"\n")
	handler, _ := newSchemaTaskHandler(t, skillReg)
	handler.definition.OutputSchema = map[string]any{
		"type":       "object",
		"properties": map[string]any{"agent_field": map[string]any{"type": "string"}},
		"required":   []any{"agent_field"},
	}

	multiStep := handler.newMultiStepHandler(handler.binding, tools.NewRegistry(), "prompt", runtime.Request{Skill: "review"})
	props, _ := multiStep.OutputSchema["properties"].(map[string]any)
	if _, ok := props["verdict"]; !ok {
		t.Fatalf("multi-step schema = %v, want the skill's", multiStep.OutputSchema)
	}

	// Without a skill, the agent's own schema is used.
	plain := handler.newMultiStepHandler(handler.binding, tools.NewRegistry(), "prompt", runtime.Request{})
	props, _ = plain.OutputSchema["properties"].(map[string]any)
	if _, ok := props["agent_field"]; !ok {
		t.Fatalf("multi-step schema = %v, want the agent's", plain.OutputSchema)
	}
}

func TestAgentTaskRefusesASkillWhoseResourceToolConflicts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: review\n---\nReview the change.\n",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Report template\"\n",
		"template.md":    "TEMPLATE",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skillReg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The agent already carries the skill-resource capability, so activating a
	// resource-declaring skill cannot inject its own reader. This must refuse
	// the task rather than run it with the wrong reader bound.
	full := tools.NewRegistry()
	full.Register(tools.NewSkillResourceTool(
		func(context.Context, string) (string, string, error) { return "", "", nil },
		"activation-key", 1024,
	))
	definition := agents.ResolvedAgent{Name: "reviewer", EffectiveTools: []string{tools.SkillResourceToolName}}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	handler := newAgentTaskHandler(definition, digest, full, runtime.New(runtime.Policy{}),
		SessionDispatcherOpts{
			Completer: &mockDelegateCompleter{name: "test", response: `{"ok":true}`},
			Model:     "model", Config: config.DefaultSubagentConfig, SkillReg: skillReg,
		})
	_, err = handler.Invoke(context.Background(), runtime.Request{
		ID: "task-3", Name: "reviewer", AgentName: "reviewer", AgentDigest: digest,
		Skill: "review", Input: json.RawMessage(`"inspect"`),
	})
	if err == nil || !strings.Contains(err.Error(), "skill resource capability conflict") {
		t.Fatalf("err = %v, want a resource capability conflict", err)
	}
}
