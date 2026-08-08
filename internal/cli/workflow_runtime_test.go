package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestWorkflowRuntimeBindingRejectsRemovedPinnedPolicy(t *testing.T) {
	agent := agents.ResolvedAgent{Name: "worker"}
	pinned := workflowledger.AgentSnapshot{ProviderName: "openrouter", Model: "old/model"}
	opts := SessionDispatcherOpts{
		ProviderName: "openrouter", Model: "new/model",
		ModelCatalog: []config.ProviderModelGroup{{
			Provider: "openrouter", Selectable: true,
			Models: []config.ModelSpec{{Name: "new/model", ContextWindowTokens: 1000}},
		}},
	}
	_, err := workflowRuntimeBinding(agent, pinned, true, opts)
	if err == nil || !strings.Contains(err.Error(), "not selectable") {
		t.Fatalf("pinned binding error = %v, want current-policy rejection", err)
	}
}

func TestPanelAuthorityRequiresExactFinalToolNames(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/panel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	for _, name := range []string{"ledger_read", "workflow_status", "future_read_tool"} {
		registry.Register(namedTool{name: name})
	}
	skillRegistry := skills.NewRegistry()
	if err := skillRegistry.Register(skills.Definition{Name: "review"}); err != nil {
		t.Fatal(err)
	}
	opts := SessionDispatcherOpts{Registry: registry, AuthorityRegistry: registry, Config: config.DefaultSubagentConfig, SkillReg: skillRegistry}
	good := agents.ResolvedAgent{Name: "panel-reviewer", EffectiveTools: []string{"read_file", "list_dir", "grep", "glob", "find_references"}, DisallowedTools: []string{toolPostMessage}}
	if err := validatePanelAgentTools(good, "review", opts, false); err != nil {
		t.Fatalf("validatePanelAgentTools(good): %v", err)
	}
	for _, name := range []string{"ledger_read", "workflow_status", "future_read_tool"} {
		t.Run(name, func(t *testing.T) {
			bad := good
			bad.EffectiveTools = append(bad.EffectiveTools, name)
			if err := validatePanelAgentTools(bad, "review", opts, false); err == nil {
				t.Fatalf("validatePanelAgentTools accepted %s", name)
			}
		})
	}
	if err := validatePanelAgentTools(agents.ResolvedAgent{Name: "review-synthesizer", DisallowedTools: []string{toolPostMessage}}, "review", opts, true); err == nil {
		t.Fatal("validatePanelAgentTools accepted an unmarked empty synthesizer")
	}
}

func TestLoadWorkflowRuntimesPinsPanelMemberBindings(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "member.md"), []byte("review"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "member.json"), []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := agents.NewRegistry()
	member := agents.ResolvedAgent{Name: "panel-reviewer", EffectiveTools: []string{"read_file", "list_dir", "grep", "glob", "find_references"}}
	if err := registry.Publish(member); err != nil {
		t.Fatal(err)
	}
	wf := &compiler.CompiledWorkflow{Digest: "workflow", Steps: []definition.Step{{
		ID: "review", Kind: "agent_panel", Panel: &definition.AgentPanel{Members: []definition.PanelMember{{
			ID: "security", Agent: "panel-reviewer", Provider: "deepseek", Model: "deepseek-v4-flash", Skill: "bug-audit", Template: "member.md", OutputSchema: "member.json",
		}}},
	}}}
	_, snapshot, err := loadWorkflowRuntimes(t.TempDir(), base, wf, registry, nil)
	if err != nil {
		t.Fatalf("loadWorkflowRuntimes: %v", err)
	}
	raw, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"panel_bindings":{"review/security":`) || !strings.Contains(string(raw), `"provider_name":"deepseek"`) {
		t.Fatalf("snapshot does not pin member binding: %s", raw)
	}
	for name, mutate := range map[string]func(*workflowledger.PanelBindingSnapshot){
		"agent digest":    func(b *workflowledger.PanelBindingSnapshot) { b.AgentDigest = "changed" },
		"provider":        func(b *workflowledger.PanelBindingSnapshot) { b.ProviderName = "changed" },
		"model":           func(b *workflowledger.PanelBindingSnapshot) { b.Model = "changed" },
		"template digest": func(b *workflowledger.PanelBindingSnapshot) { b.TemplateDigest = "changed" },
		"schema digest":   func(b *workflowledger.PanelBindingSnapshot) { b.SchemaDigest = "changed" },
	} {
		t.Run("resume rejects changed "+name, func(t *testing.T) {
			prior := snapshot
			prior.PanelBindings = map[string]workflowledger.PanelBindingSnapshot{}
			for key, value := range snapshot.PanelBindings {
				prior.PanelBindings[key] = value
			}
			binding := prior.PanelBindings["review/security"]
			mutate(&binding)
			prior.PanelBindings["review/security"] = binding
			if _, _, err := loadWorkflowRuntimes(t.TempDir(), base, wf, registry, &prior); err == nil {
				t.Fatal("loadWorkflowRuntimes() accepted changed panel binding")
			}
		})
	}
}

func TestAuthorizeWorkflowPanelBindings(t *testing.T) {
	fixture := newPanelAuthorizationFixture(t)
	if err := authorizeWorkflowPanelBindings(fixture.workflow, fixture.agents, fixture.snapshot, false, fixture.opts); err != nil {
		t.Fatalf("authorizeWorkflowPanelBindings(valid): %v", err)
	}

	for name, mutate := range map[string]func(*panelAuthorizationFixture){
		"policy disabled": func(f *panelAuthorizationFixture) { f.opts.AllowWorkspaceAgentProviders = false },
		"incomplete binding": func(f *panelAuthorizationFixture) {
			b := f.snapshot.PanelBindings["review/security"]
			b.Model = ""
			f.snapshot.PanelBindings["review/security"] = b
		},
		"unknown provider": func(f *panelAuthorizationFixture) {
			b := f.snapshot.PanelBindings["review/security"]
			b.ProviderName = "unknown"
			f.snapshot.PanelBindings["review/security"] = b
		},
		"unknown model": func(f *panelAuthorizationFixture) {
			b := f.snapshot.PanelBindings["review/security"]
			b.Model = "unknown"
			f.snapshot.PanelBindings["review/security"] = b
		},
		"missing completer": func(f *panelAuthorizationFixture) { f.opts.CompleterFactory = nil; f.opts.ProviderName = "zai" },
		"changed pinned model": func(f *panelAuthorizationFixture) {
			b := f.snapshot.PanelBindings["review/security"]
			b.Model = "other"
			f.snapshot.PanelBindings["review/security"] = b
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPanelAuthorizationFixture(t)
			mutate(f)
			if _, changed := f.snapshot.PanelBindings["review/security"]; changed && name == "changed pinned model" {
				// Ensure the changed pair remains selectable so the resume comparison runs.
				f.opts.ModelCatalog[0].Models = append(f.opts.ModelCatalog[0].Models, config.ModelSpec{Name: "other", ContextWindowTokens: 1000})
			}
			if err := authorizeWorkflowPanelBindings(f.workflow, f.agents, f.snapshot, name == "changed pinned model", f.opts); err == nil {
				t.Fatal("authorizeWorkflowPanelBindings() succeeded")
			}
		})
	}

	f := newPanelAuthorizationFixture(t)
	f.opts.CompleterFactory = func(string, string) (provider.Completer, error) {
		return nil, errors.New("example-secret")
	}
	err := authorizeWorkflowPanelBindings(f.workflow, f.agents, f.snapshot, false, f.opts)
	if err == nil || strings.Contains(err.Error(), "example-secret") {
		t.Fatalf("credential error = %v", err)
	}
}

type panelAuthorizationFixture struct {
	workflow *compiler.CompiledWorkflow
	agents   *agents.AgentRegistry
	snapshot workflowledger.Snapshot
	opts     SessionDispatcherOpts
}

func newPanelAuthorizationFixture(t *testing.T) *panelAuthorizationFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/panel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	authority := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	skillRegistry := skills.NewRegistry()
	for _, name := range []string{"review", "synth"} {
		if err := skillRegistry.Register(skills.Definition{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "panel-reviewer", EffectiveTools: []string{"read_file", "list_dir", "grep", "glob", "find_references"}, DisallowedTools: []string{toolPostMessage}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Publish(agents.ResolvedAgent{Name: "review-synthesizer", EffectiveTools: []string{}, AllowEmptyTools: true, DisallowedTools: []string{toolPostMessage}}); err != nil {
		t.Fatal(err)
	}
	workflow := &compiler.CompiledWorkflow{Steps: []definition.Step{{
		ID: "review", Kind: "agent_panel", Agent: "review-synthesizer", Skill: "synth", Panel: &definition.AgentPanel{Members: []definition.PanelMember{
			{ID: "security", Agent: "panel-reviewer", Provider: "deepseek", Model: "flash", Skill: "review"},
			{ID: "correctness", Agent: "panel-reviewer", Provider: "zai", Model: "fast", Skill: "review"},
		}},
	}}}
	snapshot := workflowledger.Snapshot{PanelBindings: map[string]workflowledger.PanelBindingSnapshot{
		"review/security":    {StepID: "review", MemberID: "security", AgentName: "panel-reviewer", ProviderName: "deepseek", Model: "flash"},
		"review/correctness": {StepID: "review", MemberID: "correctness", AgentName: "panel-reviewer", ProviderName: "zai", Model: "fast"},
	}}
	return &panelAuthorizationFixture{workflow: workflow, agents: registry, snapshot: snapshot, opts: SessionDispatcherOpts{
		Registry: authority, AuthorityRegistry: authority, SkillReg: skillRegistry, Config: config.DefaultSubagentConfig,
		AllowWorkspaceAgentProviders: true, ProviderName: "deepseek", Model: "flash", Completer: &bindingProbeCompleter{name: "deepseek"},
		ModelCatalog: []config.ProviderModelGroup{{Provider: "deepseek", Selectable: true, Models: []config.ModelSpec{{Name: "flash", ContextWindowTokens: 1000}}}, {Provider: "zai", Selectable: true, Models: []config.ModelSpec{{Name: "fast", ContextWindowTokens: 1000}}}},
		CompleterFactory: func(providerName, _ string) (provider.Completer, error) {
			return &bindingProbeCompleter{name: providerName}, nil
		},
	}}
}
