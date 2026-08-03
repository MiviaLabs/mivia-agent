package cli

// R4: skill-path post_message injection must not resurrect a disallowed tool.
//
// Mirrors messaging_integration_test.go TestInjectBaselineMessaging (agent
// with post_message disallowed → surface lacks it) and extends it to the
// skill-activated surface built the way prepareInvokeSurface does: the
// baseline messaging injection runs again on the skill scoped surface, and it
// must not bring post_message back even though the full registry carries it,
// while the skill's own tools stay present.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// resourceSkillRegistry builds a skill registry with one resource-declaring
// skill ("review"), so the skill-activated surface carries the skill's own
// read_skill_resource tool (injected by activateSkill/injectSkillResourceTool).
func resourceSkillRegistry(t *testing.T) *skills.Registry {
	t.Helper()
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
	reg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// stubCompleter satisfies provider.Completer for handler binding resolution.
// It is never invoked: the surface tests only call prepareInvokeSurface.
type stubCompleter struct{}

func (stubCompleter) Name() string                                           { return "skill-inject" }
func (stubCompleter) Chat(context.Context, provider.Request) (string, error) { return "", nil }
func (stubCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (stubCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "ok", FinishReason: "stop"}, nil
}

// newSkillInjectionHandler builds an agentTaskHandler whose agent disallows
// post_message, against a full registry that DOES carry post_message (so a
// buggy injection would resurrect it), plus a registered resource skill.
func newSkillInjectionHandler(t *testing.T, skillReg *skills.Registry) *agentTaskHandler {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewRegistry()
	full.Register(&postMessageTool{cfg: config.DefaultSubagentConfig})
	for _, tt := range tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}).List() {
		if tt.Name() == toolPostMessage {
			continue
		}
		full.Register(tt)
	}
	definition := agents.ResolvedAgent{
		Name:            "reviewer",
		EffectiveTools:  []string{tools.SkillResourceToolName, "read_file"},
		DisallowedTools: []string{toolPostMessage},
	}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	return newAgentTaskHandler(definition, digest, full, runtime.New(runtime.Policy{}),
		SessionDispatcherOpts{
			Completer: stubCompleter{},
			Model:     "model", Config: config.DefaultSubagentConfig, SkillReg: skillReg,
		})
}

// TestSkillSurfaceInjectionDoesNotResurrectDisallowedPostMessage guards the
// skill path of injectBaselineMessaging: an agent that opted out of
// post_message via disallowed_tools must keep it off both its plain spawned
// surface and its skill-activated surface, while the skill's own tools remain.
func TestSkillSurfaceInjectionDoesNotResurrectDisallowedPostMessage(t *testing.T) {
	skillReg := resourceSkillRegistry(t)
	handler := newSkillInjectionHandler(t, skillReg)

	// Plain path: agent with post_message disallowed → surface lacks it.
	_, plain, closePlain, err := handler.prepareInvokeSurface(runtime.Request{})
	if err != nil {
		t.Fatal(err)
	}
	closePlain()
	if _, ok := plain.Get(toolPostMessage); ok {
		t.Fatal("plain spawned surface must not carry post_message for a disallowed agent")
	}
	if _, ok := plain.Get("read_file"); !ok {
		t.Fatal("plain spawned surface should keep the agent's allowed tools")
	}

	// Skill path: the skill-activated scoped surface must ALSO lack
	// post_message while carrying the skill's own tool.
	_, scoped, closeAct, err := handler.prepareInvokeSurface(runtime.Request{Skill: "review"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAct()
	if _, ok := scoped.Get(toolPostMessage); ok {
		t.Fatal("skill-activated surface must not resurrect disallowed post_message")
	}
	if _, ok := scoped.Get(tools.SkillResourceToolName); !ok {
		t.Fatal("skill-activated surface must carry the skill's own read_skill_resource tool")
	}
	if _, ok := scoped.Get("read_file"); !ok {
		t.Fatal("skill-activated surface must keep the agent's ordinary tools")
	}
}
