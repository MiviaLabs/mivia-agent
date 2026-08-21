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

// TestPanelAuthorityAdmitsSkillResourceReader pins the fix for the enabled
// agent_panel gate refusing every run at admission: a panel member whose
// skill declares resources gets the host-injected read_skill_resource scoped
// reader (injectSkillResourceTool) in its runtime surface, so the expected
// tool set must carry it too. TestPanelAuthorityRequiresExactFinalToolNames
// above uses a resource-less skill, which is exactly why unit coverage never
// saw the mismatch that blocked the live gate (bug-audit, secure-change, and
// architecture-review all ship resources.toml).
func TestPanelAuthorityAdmitsSkillResourceReader(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/panel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, "review")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: review\n---\nLoad the declared template before reporting.",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Required report template\"\n",
		"template.md":    "TEMPLATE",
	} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	skillRegistry, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opts := SessionDispatcherOpts{Registry: registry, AuthorityRegistry: registry, Config: config.DefaultSubagentConfig, SkillReg: skillRegistry}
	good := agents.ResolvedAgent{Name: "panel-reviewer", EffectiveTools: []string{"read_file", "list_dir", "grep", "glob", "find_references"}, DisallowedTools: []string{toolPostMessage}}
	if err := validatePanelAgentTools(good, "review", opts, false); err != nil {
		t.Fatalf("validatePanelAgentTools with a resource-declaring skill: %v", err)
	}
	// Fail-closed is preserved: an unauthorized extra tool that exists in the
	// authority registry still refuses admission.
	registry.Register(namedTool{name: "future_read_tool"})
	bad := good
	bad.EffectiveTools = append(bad.EffectiveTools, "future_read_tool")
	if err := validatePanelAgentTools(bad, "review", opts, false); err == nil {
		t.Fatal("validatePanelAgentTools accepted an unauthorized extra tool")
	}
}

// TestPanelAuthoritySynthesizerAdmitsMCPServers pins the second live
// admission failure of the enabled agent_panel gate: the review-synthesizer
// inherits the project's global MCP servers (codegraph, context7), so its
// runtime surface carries the mcp__ tools and the expected set must carry
// them too. Before this fix the synthesizer's expected set was always empty,
// so a live panel could never admit even after the member fix landed. The
// synthesizer still fails closed against any non-MCP tool.
func TestPanelAuthoritySynthesizerAdmitsMCPServers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/panel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	// Mirrors the live codegraph/context7 surface: EncodeToolName is
	// "mcp__<server>__x" + hex of the remote tool name.
	for _, name := range []string{
		"mcp__codegraph__x636f646567726170685f6578706c6f7265",
		"mcp__context7__x71756572792d646f6373",
		"mcp__context7__x7265736f6c7665722d6c6962726172792d6964",
	} {
		registry.Register(namedTool{name: name})
	}
	skillRegistry := skills.NewRegistry()
	if err := skillRegistry.Register(skills.Definition{Name: "review"}); err != nil {
		t.Fatal(err)
	}
	opts := SessionDispatcherOpts{Registry: registry, AuthorityRegistry: registry, Config: config.DefaultSubagentConfig, SkillReg: skillRegistry}
	synth := agents.ResolvedAgent{
		Name: "review-synthesizer", AllowEmptyTools: true, DisallowedTools: []string{toolPostMessage},
		EffectiveMCPServers: []string{"codegraph", "context7"},
	}
	if err := validatePanelAgentTools(synth, "review", opts, true); err != nil {
		t.Fatalf("validatePanelAgentTools for a synthesizer with global MCP servers: %v", err)
	}
	// A synthesizer whose surface gains a non-MCP tool still fails closed.
	bad := synth
	bad.EffectiveTools = []string{"read_file"}
	if err := validatePanelAgentTools(bad, "review", opts, true); err == nil {
		t.Fatal("validatePanelAgentTools accepted a synthesizer with a local tool")
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
	synthesizer := agents.ResolvedAgent{Name: "review-synthesizer", AllowEmptyTools: true}
	if err := registry.Publish(synthesizer); err != nil {
		t.Fatal(err)
	}
	wf := &definition.CompiledWorkflow{Digest: "workflow", Steps: []definition.Step{{
		ID: "review", Kind: "agent_panel", Agent: "review-synthesizer", Panel: &definition.AgentPanel{Members: []definition.PanelMember{{
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
	if pinned, ok := snapshot.Agents["review-synthesizer"]; !ok || pinned.Digest == "" {
		t.Fatal("snapshot does not pin the panel synthesizer")
	}
	if _, _, err := loadWorkflowRuntimes(t.TempDir(), base, wf, registry, &snapshot); err != nil {
		t.Fatalf("loadWorkflowRuntimes(resume): %v", err)
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
		"unknown catalog provider": func(f *panelAuthorizationFixture) {
			b := f.snapshot.PanelBindings["review/security"]
			b.ProviderName = "unknown"
			f.snapshot.PanelBindings["review/security"] = b
			f.opts.ModelCatalog = append(f.opts.ModelCatalog, config.ProviderModelGroup{Provider: "unknown", Selectable: true, Models: []config.ModelSpec{{Name: "flash", ContextWindowTokens: 1000}}})
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

// Test-review regression: neither TestLoadWorkflowRuntimesPinsPanelMemberBindings
// nor TestAuthorizeWorkflowPanelBindings ever calls resolveWorkflowPanelSynthesisBindings
// (it is wired only into prepareWorkflowRuntime, not loadWorkflowRuntimes or
// authorizeWorkflowPanelBindings individually), so the synthesizer's own
// session-following binding resolution and resume reauthorization had zero
// direct test coverage.
func TestResolveWorkflowPanelSynthesisBindings(t *testing.T) {
	f := newPanelAuthorizationFixture(t)
	admitted := f.snapshot
	if err := resolveWorkflowPanelSynthesisBindings(f.workflow, f.agents, nil, admitted, f.opts); err != nil {
		t.Fatalf("admit: resolveWorkflowPanelSynthesisBindings() error = %v", err)
	}
	binding, ok := admitted.PanelBindings["review/synthesis"]
	if !ok {
		t.Fatal("admit: synthesis binding was not stored under the reserved key")
	}
	if binding.AgentName != "review-synthesizer" || binding.ProviderName != "deepseek" || binding.Model != "flash" {
		t.Fatalf("admit: synthesis binding = %+v, want review-synthesizer following the session's deepseek/flash default", binding)
	}

	t.Run("resume accepts the unchanged binding", func(t *testing.T) {
		f2 := newPanelAuthorizationFixture(t)
		f2.snapshot.PanelBindings["review/synthesis"] = binding
		prior := f2.snapshot
		next := workflowledger.Snapshot{PanelBindings: map[string]workflowledger.PanelBindingSnapshot{}}
		if err := resolveWorkflowPanelSynthesisBindings(f2.workflow, f2.agents, &prior, next, f2.opts); err != nil {
			t.Fatalf("resolveWorkflowPanelSynthesisBindings(resume, unchanged) error = %v", err)
		}
	})

	// Provider/model resolve FROM the pinned value itself on resume (matching
	// the same pattern non-panel undeclared-binding agents already use via
	// resolvePinnedAgentBinding), so only the agent/template/schema digests -
	// which are recomputed fresh from the CURRENT registry/snapshot on every
	// call - give real drift protection here.
	for name, mutate := range map[string]func(*workflowledger.PanelBindingSnapshot){
		"agent digest":    func(b *workflowledger.PanelBindingSnapshot) { b.AgentDigest = "changed" },
		"template digest": func(b *workflowledger.PanelBindingSnapshot) { b.TemplateDigest = "changed" },
		"schema digest":   func(b *workflowledger.PanelBindingSnapshot) { b.SchemaDigest = "changed" },
	} {
		t.Run("resume rejects changed "+name, func(t *testing.T) {
			fx := newPanelAuthorizationFixture(t)
			drifted := binding
			mutate(&drifted)
			fx.snapshot.PanelBindings["review/synthesis"] = drifted
			prior := fx.snapshot
			next := workflowledger.Snapshot{PanelBindings: map[string]workflowledger.PanelBindingSnapshot{}}
			if err := resolveWorkflowPanelSynthesisBindings(fx.workflow, fx.agents, &prior, next, fx.opts); err == nil {
				t.Fatal("resolveWorkflowPanelSynthesisBindings() accepted a drifted synthesis binding")
			}
		})
	}

	t.Run("resume rejects a missing prior binding", func(t *testing.T) {
		fx := newPanelAuthorizationFixture(t)
		prior := fx.snapshot // PanelBindings has no "review/synthesis" entry
		next := workflowledger.Snapshot{PanelBindings: map[string]workflowledger.PanelBindingSnapshot{}}
		if err := resolveWorkflowPanelSynthesisBindings(fx.workflow, fx.agents, &prior, next, fx.opts); err == nil {
			t.Fatal("resolveWorkflowPanelSynthesisBindings() accepted resume with no pinned synthesis binding")
		}
	})
}

// Test-review regression: the D4 guard on the synthesizer's own binding
// (step.Agent must be named "review-synthesizer" and declare no provider or
// model) is a statement newly added alongside resolveWorkflowPanelSynthesisBindings;
// the equivalent guard in authorizeWorkflowPanelBindings already had
// coverage for members but not this one.
func TestResolveWorkflowPanelSynthesisBindingsRejectsDeclaredOrMisnamedSynthesizer(t *testing.T) {
	f := newPanelAuthorizationFixture(t)
	if err := f.agents.Publish(agents.ResolvedAgent{Name: "declared-synthesizer", Provider: "deepseek", Model: "flash", AllowEmptyTools: true}); err != nil {
		t.Fatal(err)
	}
	f.workflow.Steps[0].Agent = "declared-synthesizer"
	if err := resolveWorkflowPanelSynthesisBindings(f.workflow, f.agents, nil, f.snapshot, f.opts); err == nil {
		t.Fatal("resolveWorkflowPanelSynthesisBindings() accepted a declared-binding synthesizer")
	}

	f2 := newPanelAuthorizationFixture(t)
	if err := f2.agents.Publish(agents.ResolvedAgent{Name: "not-a-synthesizer", AllowEmptyTools: true}); err != nil {
		t.Fatal(err)
	}
	f2.workflow.Steps[0].Agent = "not-a-synthesizer"
	if err := resolveWorkflowPanelSynthesisBindings(f2.workflow, f2.agents, nil, f2.snapshot, f2.opts); err == nil {
		t.Fatal("resolveWorkflowPanelSynthesisBindings() accepted a misnamed synthesizer agent")
	}
}

type panelAuthorizationFixture struct {
	workflow *definition.CompiledWorkflow
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
	workflow := &definition.CompiledWorkflow{Steps: []definition.Step{{
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
