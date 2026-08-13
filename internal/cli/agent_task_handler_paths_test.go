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
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
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

// TestSchemaSystemAppendixRendersAContractNotTheSchemaDocument guards the
// system-prompt output contract: it must show the model an instance shape,
// never the raw schema document with its $schema/meta keys (a verbatim
// document invites the model to echo it back as its answer).
func TestSchemaSystemAppendixRendersAContractNotTheSchemaDocument(t *testing.T) {
	schema := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Review output",
		"description": "A review verdict with findings.",
		"type":        "object",
		"required":    []any{"verdict", "inspected"},
		"properties": map[string]any{
			"verdict":   map[string]any{"type": "string", "enum": []any{"approved", "changes_requested"}},
			"inspected": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
	block := schemaSystemAppendix(schema)
	for _, key := range []string{`"$schema"`, `"title"`, `"description"`} {
		if strings.Contains(block, key) {
			t.Fatalf("system appendix leaked the %s meta-key: %q", key, block)
		}
	}
	if !strings.Contains(block, "never the schema document") {
		t.Fatalf("system appendix must carry the never-echo instruction: %q", block)
	}
	if !strings.Contains(block, `"required"`) || !strings.Contains(block, "verdict") {
		t.Fatalf("system appendix lost the instance shape: %q", block)
	}
	if got := schemaSystemAppendix(nil); got != "" {
		t.Fatalf("a nil schema must emit nothing, got %q", got)
	}
}

// TestSchemaSystemAppendixDelegatesToJschemaEnvelope pins the centralization:
// schemaSystemAppendix must produce exactly the same envelope-wrapping
// instruction as jschema.EnvelopeAppendixBody, rather than hand-building its
// own near-identical string as it did before. That duplication previously
// required the two renderers' wording to be kept in sync by hand.
func TestSchemaSystemAppendixDelegatesToJschemaEnvelope(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"required":   []any{"ok"},
		"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
	}
	got := schemaSystemAppendix(schema)
	want := jschema.EnvelopeAppendixBody(jschema.ModelSchemaContract(schema))
	if got != want {
		t.Fatalf("schemaSystemAppendix = %q, want delegation to jschema.EnvelopeAppendixBody: %q", got, want)
	}
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

func TestAgentTaskCombinesAllWorkLimitSources(t *testing.T) {
	agentTurns, agentOutput := 16, 8192
	handler := &agentTaskHandler{
		definition: agents.ResolvedAgent{MaxTurns: &agentTurns, MaxTokens: &agentOutput},
		opts:       SessionDispatcherOpts{WorkLimits: runtime.WorkLimits{MaxTurns: 12, MaxPromptTokens: 90, MaxToolCalls: 8}},
	}
	got := handler.effectiveWorkLimits(agentBinding{maxTokens: 4096}, runtime.Request{WorkLimits: runtime.WorkLimits{
		MaxTurns: 10, MaxPromptTokens: 100, MaxOutputTokens: 70, MaxOutputPerCall: 2048, MaxToolCalls: 9,
	}})
	want := runtime.WorkLimits{MaxTurns: 10, MaxPromptTokens: 90, MaxOutputTokens: 70, MaxOutputPerCall: 2048, MaxToolCalls: 8}
	if got != want {
		t.Fatalf("work limits = %+v, want %+v", got, want)
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
