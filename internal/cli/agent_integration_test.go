package cli

import (
	"context"
	"encoding/json"
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
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestAgentNameCollidesWithSkill(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)

	// User agent named like a skill.
	writeTestAgent(t, config.UserAgentsDir(), "reviewer", `
name = "reviewer"
description = "agent"
tools = ["read_file"]
`)
	// Project skill with same name.
	skillDir := filepath.Join(workspace.SkillsDir(ws), "reviewer")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: reviewer\ndescription: skill\n---\n\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillReg, _, err := loadSessionSkills(ws, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadAgentDefinitions(ws, "", skillReg)
	if err == nil || !strings.Contains(err.Error(), "skill") {
		t.Fatalf("want skill collision, got %v", err)
	}
}

func TestRootSession_AgentFlagUnknownName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestAgent(t, config.UserAgentsDir(), "researcher", `
name = "researcher"
description = "r"
tools = ["read_file"]
`)
	_, err := loadAgentDefinitions(t.TempDir(), "nope", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("want unknown agent error, got %v", err)
	}
	if !strings.Contains(err.Error(), "researcher") {
		t.Fatalf("error should list available agents: %v", err)
	}
}

func TestRootSession_AgentFlag(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)
	writeTestAgent(t, config.UserAgentsDir(), "researcher", `
name = "researcher"
description = "read only"
tools = ["read_file", "grep"]
`)
	// Build a session-like registry and apply root scope after "attach".
	reg := tools.NewRegistry()
	reg.Register(namedTool{name: "read_file"})
	reg.Register(namedTool{name: "write_file"})
	reg.Register(namedTool{name: "grep"})
	reg.Register(privilegedNamed{namedTool: namedTool{name: "dispatch_tasks"}})

	loaded, err := loadAgentDefinitions(ws, "researcher", nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Selected == nil {
		t.Fatal("expected selected agent")
	}
	sess := &chat.Session{Tools: reg}
	applyRootAgentScope(sess, loaded.Selected, nil)

	if _, ok := sess.Tools.Get("write_file"); ok {
		t.Fatal("write_file must be excluded after root scope")
	}
	if _, ok := sess.Tools.Get("read_file"); !ok {
		t.Fatal("read_file must remain")
	}
	if _, ok := sess.Tools.Get("dispatch_tasks"); !ok {
		t.Fatal("privileged dispatch_tasks must survive root scope")
	}
}

func TestRootScopedRegistry_AfterAttach(t *testing.T) {
	// Same as AgentFlag: privileged survives, excluded tools gone (M16).
	TestRootSession_AgentFlag(t)
}

func TestAgentScopedLoopCannotWriteFile(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	// Agent allows only read_file.
	scoped := tools.ScopedRegistry(full, tools.ScopeOptions{
		Mode:      tools.ScopeSpawned,
		Allowlist: map[string]struct{}{"read_file": {}},
	})
	if _, ok := scoped.Get("write_file"); ok {
		t.Fatal("write_file must not be in scoped registry")
	}
	target := filepath.Join(dir, "must-not-exist.txt")
	// Execution denial: registry.Execute is the real path when loop gates on Get.
	_, err = scoped.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"must-not-exist.txt","content":"pwned"}`))
	if err == nil {
		t.Fatal("expected write_file execution denial")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("file must not exist after denied write, stat=%v", statErr)
	}
	// Control: full registry can write.
	if _, err := full.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"ok.txt","content":"yes"}`)); err != nil {
		t.Fatalf("full registry write: %v", err)
	}
}

func TestWorkspaceSystemPromptStrippedWhenGateOff(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)
	// User gate off (default).
	writeUserToml(t, home, `[agents]
load_workspace_config = false
`)
	// Workspace config with system prompts.
	wsCfg := workspace.NamespacePath(ws, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(wsCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsCfg, []byte(`
[chat]
system_prompt = "workspace root prompt"

[subagents]
system_prompt = "workspace subagent prompt"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	global, err := config.LoadAgentsGlobal(ws)
	if err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{
		ConfigPath:   wsCfg,
		SystemPrompt: "workspace root prompt",
		Subagents:    config.SubagentConfig{SystemPrompt: "workspace subagent prompt"},
	}
	applyWorkspacePromptGate(res, global)
	if res.SystemPrompt != "" {
		t.Fatalf("workspace chat prompt must be stripped, got %q", res.SystemPrompt)
	}
	if res.Subagents.SystemPrompt != "" {
		t.Fatalf("workspace subagent prompt must be stripped, got %q", res.Subagents.SystemPrompt)
	}
}

func TestUserConfigSystemPromptAlwaysLoaded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userCfg := writeUserToml(t, home, `
[agents]
load_workspace_config = false

[chat]
system_prompt = "trusted user prompt"
`)
	global, err := config.LoadAgentsGlobal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{
		ConfigPath:   userCfg,
		SystemPrompt: "trusted user prompt",
	}
	applyWorkspacePromptGate(res, global)
	if res.SystemPrompt != "trusted user prompt" {
		t.Fatalf("user prompt stripped: %q", res.SystemPrompt)
	}
}

func TestWorkspaceSubagentSystemPromptStrippedWhenGateOff(t *testing.T) {
	// Covered by TestWorkspaceSystemPromptStrippedWhenGateOff subagents field.
	TestWorkspaceSystemPromptStrippedWhenGateOff(t)
}

func TestWorkspaceSkillHandlersNotRegisteredWhenGateOff(t *testing.T) {
	reg := skills.NewRegistry()
	_ = reg.Register(skills.Definition{Name: "user-skill", Origin: skills.OriginUser, Description: "u"})
	_ = reg.Register(skills.Definition{Name: "project-skill", Origin: skills.OriginProject, Description: "p"})
	filtered := filterSkillRegistryForGate(reg, false)
	names := map[string]bool{}
	for _, d := range filtered.List() {
		names[d.Name] = true
	}
	if !names["user-skill"] {
		t.Fatal("user skill must remain")
	}
	if names["project-skill"] {
		t.Fatal("project skill must be filtered when gate off")
	}
}

func TestWorkspaceSkillHandlersRegisteredWhenGateOn(t *testing.T) {
	reg := skills.NewRegistry()
	_ = reg.Register(skills.Definition{Name: "project-skill", Origin: skills.OriginProject, Description: "p"})
	filtered := filterSkillRegistryForGate(reg, true)
	if len(filtered.List()) != 1 {
		t.Fatalf("gate on must keep project skills, got %d", len(filtered.List()))
	}
}

func TestModelSwitchKeepsAgentScope(t *testing.T) {
	// Pre-scoped registry is what buildModelBinding clones for a new generation.
	reg := tools.NewRegistry()
	reg.Register(namedTool{name: "read_file"})
	// No write_file - already scoped (simulates applyRootAgentScope).
	if _, ok := reg.Get("write_file"); ok {
		t.Fatal("precondition: write_file absent")
	}
	clone := reg.CloneForGenerationExcluding()
	if _, ok := clone.Get("write_file"); ok {
		t.Fatal("generation clone must not regain write_file")
	}
	if _, ok := clone.Get("read_file"); !ok {
		t.Fatal("read_file must survive clone")
	}
}

func TestConcurrentAgentInstancesFromOneDefinition(t *testing.T) {
	agent := agents.ResolvedAgent{
		Name:           "researcher",
		EffectiveTools: []string{"read_file"},
		Description:    "d",
	}
	base := tools.NewRegistry()
	base.Register(namedTool{name: "read_file"})
	base.Register(namedTool{name: "write_file"})

	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each instance gets a fresh scoped registry from the same definition.
			scoped := tools.ScopedRegistry(base, tools.ScopeOptions{
				Mode:      tools.ScopeSpawned,
				Allowlist: agents.AllowlistSet(agent.EffectiveTools),
			})
			if _, ok := scoped.Get("write_file"); ok {
				errs <- errString("write_file leaked")
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			if _, ok := scoped.Get("read_file"); !ok {
				errs <- errString("read_file missing")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Source definition and base registry unchanged.
	if agent.EffectiveTools[0] != "read_file" {
		t.Fatal("definition mutated")
	}
	if _, ok := base.Get("write_file"); !ok {
		t.Fatal("base registry mutated")
	}
}

func TestParseChatInvocationAgentFlag(t *testing.T) {
	inv, err := parseChatInvocation([]string{"--agent", "researcher", "-p", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.agent != "researcher" {
		t.Fatalf("agent = %q", inv.agent)
	}
	if inv.prompt != "hi" {
		t.Fatalf("prompt = %q", inv.prompt)
	}
}

// TestDispatcherAgreesWithSessionRegistryAfterAttach verifies INV-AG-29
// property: the session dispatcher and sess.Tools must agree after agent
// scope is applied. A tool absent from sess.Tools must also be absent from
// the dispatcher's executable registry. This prevents a latent execution path
// where the dispatcher retains a stale unscoped registry pointer.
func TestDispatcherAgreesWithSessionRegistryAfterAttach(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	if _, ok := full.Get("write_file"); !ok {
		t.Fatal("precondition: full registry has write_file")
	}
	selected := &agents.ResolvedAgent{Name: "reader", EffectiveTools: []string{"read_file"}}

	// Reproduce attachSessionDispatcher ordering.
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	sess.Tools = full.Clone()
	sess.UseTools = true
	reg := agents.NewRegistry()
	_ = reg.Publish(*selected)

	state := &agentSessionState{
		Registry:           reg,
		Selected:           selected,
		Global:             config.AgentsGlobal{},
		WorkspaceRoot:      dir,
		AllowProjectSkills: true,
		ToolBase:           nil,
	}

	_, err = attachSessionDispatcher(sess, dir, "m", config.DefaultSubagentConfig, state, nil, sessionRouting{})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	// sess.Tools must be scoped (no write_file).
	if _, ok := sess.Tools.Get("write_file"); ok {
		t.Fatal("sess.Tools must not expose write_file after root scope")
	}

	// The dispatcher must also refuse write_file - it must agree with sess.Tools.
	if sess.Dispatcher == nil {
		t.Fatal("dispatcher must be set")
	}
	target := filepath.Join(dir, "must-not-write.txt")
	res := sess.Dispatcher.Invoke(context.Background(), runtime.Request{
		ID:        "test-1",
		Kind:      runtime.Tool,
		Name:      "write_file",
		Input:     json.RawMessage(`{"path":"must-not-write.txt","content":"pwned"}`),
		SessionID: "test",
	})
	if res.Err == nil {
		t.Fatal("dispatcher must deny write_file that agent scope excludes")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("file must not exist: %v", statErr)
	}
}

// TestDispatcherAgreesAfterAgentSwitch verifies the same property for the
// mid-session /agent path (applyAgentSelection).
func TestDispatcherAgreesAfterAgentSwitch(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	if _, ok := full.Get("write_file"); !ok {
		t.Fatal("precondition: full registry has write_file")
	}

	state := fixtureAgentStateWithTools(t, map[string]agentFixture{
		"reader": {prompt: "R", tools: []string{"read_file"}, maxTurns: intPtr(3)},
		"writer": {prompt: "W", tools: []string{"read_file", "write_file"}, maxTurns: intPtr(0)},
	})
	state.ToolBase = full.Clone()

	res := &config.Resolved{Model: "m", ProviderName: "p", Subagents: config.DefaultSubagentConfig}
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.Tools = full.Clone()
	sess.UseTools = true
	// Seed initial dispatcher so CurrentBinding returns a completer.
	seedDispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  full,
		Completer: stubAgentCompleter{},
		Model:     "m",
		Config:    config.SubagentConfig{MaxDepth: 2},
	})
	if err != nil {
		t.Fatalf("seed dispatcher: %v", err)
	}
	t.Cleanup(seedDispatcher.Close)
	sess.SetDispatcher(seedDispatcher)

	// Switch to read-only agent.
	if err := applySessionAgent(sess, res, state, "reader", false); err != nil {
		t.Fatalf("switch to reader: %v", err)
	}
	if _, ok := sess.Tools.Get("write_file"); ok {
		t.Fatal("sess.Tools must not expose write_file after reader switch")
	}

	// Dispatcher must also refuse write_file.
	target := filepath.Join(dir, "switch-leak.txt")
	result := sess.Dispatcher.Invoke(context.Background(), runtime.Request{
		ID:        "switch-test",
		Kind:      runtime.Tool,
		Name:      "write_file",
		Input:     json.RawMessage(`{"path":"switch-leak.txt","content":"pwned"}`),
		SessionID: "test",
	})
	if result.Err == nil {
		t.Fatal("dispatcher must deny write_file after /agent switch to reader")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("file must not exist after switch leak check: %v", statErr)
	}
}

// TestGuardrailDeniedToolExcludedAtRoot verifies INV-AG-29 execution denial
// for an operator [agents.guardrails].mandatory_tool_denylist addition: a
// non-privileged tool denied by the guardrail AND absent from the selected
// agent's allowlist must be excluded from the root session registry AND
// refused by the executable dispatcher after attach.
func TestGuardrailDeniedToolExcludedAtRoot(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws, RunAllowlist: []string{"echo"}})
	if _, ok := full.Get("run_command"); !ok {
		t.Fatal("precondition: full registry has run_command")
	}
	selected := &agents.ResolvedAgent{Name: "reader", EffectiveTools: []string{"read_file"}}
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	sess.Tools = full.Clone()
	sess.UseTools = true
	reg := agents.NewRegistry()
	_ = reg.Publish(*selected)

	state := &agentSessionState{
		Registry:           reg,
		Selected:           selected,
		Global:             config.AgentsGlobal{MandatoryToolDenylistAdditions: []string{"run_command"}},
		WorkspaceRoot:      dir,
		AllowProjectSkills: true,
		ToolBase:           nil,
	}

	if _, err := attachSessionDispatcher(sess, dir, "m", config.DefaultSubagentConfig, state, nil, sessionRouting{}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, ok := sess.Tools.Get("run_command"); ok {
		t.Fatal("sess.Tools must not expose guardrail-denied run_command after root scope")
	}
	res := sess.Dispatcher.Invoke(context.Background(), runtime.Request{
		ID:        "guardrail-test",
		Kind:      runtime.Tool,
		Name:      "run_command",
		Input:     json.RawMessage(`{"argv":["ls"]}`),
		SessionID: "test",
	})
	if res.Err == nil {
		t.Fatal("dispatcher must deny guardrail-excluded run_command at execution")
	}
}

// --- helpers ---

type namedTool struct{ name string }

func (t namedTool) Name() string               { return t.name }
func (t namedTool) Description() string        { return t.name }
func (t namedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t namedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

type privilegedNamed struct{ namedTool }

func (privilegedNamed) Privileged() {}

type errString string

func (e errString) Error() string { return string(e) }

func writeTestAgent(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeUserToml(t *testing.T, home, body string) string {
	t.Helper()
	path := workspace.NamespacePath(home, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
