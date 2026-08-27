package cliorchestrate

// test_helpers_moved_test.go holds test helpers that lived in the old
// monolithic cli package before cliagents was extracted. Production code
// moved to cliagents; these are the corresponding test-only wrappers and
// duplicated helpers that cli tests still need.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- registry helpers -------------------------------------------------------

// namedTool is a minimal tools.Tool with a fixed name (duplicated from
// internal/cli/agent_integration_test.go).
type namedTool struct{ name string }

func (t namedTool) Name() string               { return t.name }
func (t namedTool) Description() string        { return t.name }
func (t namedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t namedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// tierRegistry builds a minimal tool registry holding exactly the named tools.
// Copied from cliagents test helpers; cannot import test files across packages.
func tierRegistry(names ...string) *tools.Registry {
	reg := tools.NewRegistry()
	for _, name := range names {
		reg.Register(namedTool{name: name})
	}
	return reg
}

// corePtr wraps a variadic string list as the *[]string CoreTools pointer that
// agents.ResolvedAgent requires.
func corePtr(names ...string) *[]string {
	out := slices.Clone(names)
	return &out
}

// --- lowercase wrappers for exported cliagents functions --------------------
// These match the old unexported names cli tests used before extraction.

func loadAgentDefinitions(workspaceRoot, agentFlag string, skillReg *skills.Registry) (cliagents.AgentLoadResult, error) {
	return cliagents.LoadAgentDefinitions(workspaceRoot, agentFlag, skillReg)
}

func loadSessionSkills(root string, allowProject bool) (*skills.Registry, []string, error) {
	return cliagents.LoadSessionSkills(root, allowProject)
}

func scopedRootRegistry(registry *tools.Registry, selected *agents.ResolvedAgent, extraDenylist []string) (*tools.Registry, []string) {
	return cliagents.ScopedRootRegistry(registry, selected, extraDenylist)
}

func filterSkillRegistryForGate(skillReg *skills.Registry, allowProject bool) *skills.Registry {
	return cliagents.FilterSkillRegistryForGate(skillReg, allowProject)
}

func applyWorkspacePromptGate(res *config.Resolved, global config.AgentsGlobal) {
	cliagents.ApplyWorkspacePromptGate(res, global)
}

func resolveAgentBinding(definition agents.ResolvedAgent, opts cliagents.SessionDispatcherOpts) (cliagents.AgentBinding, error) {
	return cliagents.ResolveAgentBinding(definition, opts)
}

func buildModelBinding(sess *chat.Session, res *config.Resolved, root, providerName, model string, state *cliagents.AgentSessionState) (chat.ModelBinding, error) {
	return cliagents.BuildModelBinding(sess, res, root, providerName, model, state)
}

func chatBindingFactory(sess *chat.Session, res *config.Resolved, root string, state *cliagents.AgentSessionState) func(string, string) (chat.ModelBinding, error) {
	return cliagents.ChatBindingFactory(sess, res, root, state)
}

func configureChatWorkspace(sess *chat.Session, root string, useTools bool, res *config.Resolved, state *cliagents.AgentSessionState, quiet bool, fullDisk bool, runRecoverySweep bool) (func(), error) {
	return cliagents.ConfigureChatWorkspace(sess, root, useTools, res, state, quiet, fullDisk, runRecoverySweep)
}

func configuredProfile(res *config.Resolved, providerName, model string) (config.ModelSpec, bool) {
	return cliagents.ConfiguredProfile(res, providerName, model)
}

func tieredRootRegistry(base *tools.Registry, selected *agents.ResolvedAgent, extraDenylist []string, plan cliagents.ToolTierPlan, admitted []string) *tools.Registry {
	return cliagents.TieredRootRegistry(base, selected, extraDenylist, plan, admitted)
}

func advertisedToolSpecs(base *tools.Registry, plan cliagents.ToolTierPlan) ([]provider.ToolSpec, int) {
	return cliagents.AdvertisedToolSpecs(base, plan, nil)
}

func planToolTiers(base *tools.Registry, selected *agents.ResolvedAgent, res *config.Resolved) cliagents.ToolTierPlan {
	return cliagents.PlanToolTiers(base, selected, res)
}

func refreshSummarizerAfterModelSwitch(sess *chat.Session, res *config.Resolved) {
	cliagents.RefreshSummarizerAfterModelSwitch(sess, res)
}

func buildWidenedWith(sess *chat.Session, res *config.Resolved, state *cliagents.AgentSessionState, admitted []string) (*cliagents.AgentSurface, error) {
	return cliagents.BuildWidenedWith(sess, res, state, admitted)
}

func runAgentsWithIO(args []string, stdout, stderr io.Writer) error {
	return cliagents.RunAgentsWithIO(args, stdout, stderr)
}

// --- bindingProbeCompleter --------------------------------------------------
// Duplicated from cliagents/agent_binding_test.go; test files cannot be
// imported across packages.

// bindingProbeCompleter records the (provider, model) each turn actually ran
// against, so a test can assert what reached the wire rather than configured.
type bindingProbeCompleter struct {
	name string
	mu   sync.Mutex
	seen []string
}

func (c *bindingProbeCompleter) Name() string { return c.name }

func (c *bindingProbeCompleter) record(req provider.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, c.name+"/"+req.Model)
}

func (c *bindingProbeCompleter) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func (c *bindingProbeCompleter) Chat(_ context.Context, req provider.Request) (string, error) {
	c.record(req)
	return "done", nil
}

func (c *bindingProbeCompleter) ChatStream(_ context.Context, req provider.Request, _ io.Writer) (string, error) {
	c.record(req)
	return "done", nil
}

func (c *bindingProbeCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.record(req)
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

// bindingTestCatalog declares two providers so a routed agent can select one
// the session is not using. Duplicated from cliagents test helpers.
func bindingTestCatalog() []config.ProviderModelGroup {
	return []config.ProviderModelGroup{
		{Provider: "zai", Selectable: true, Active: true, Models: []config.ModelSpec{
			{Name: "glm-5.2", ContextWindowTokens: 200000},
		}},
		{Provider: "deepseek", Selectable: true, Models: []config.ModelSpec{
			{Name: "deepseek-v4-flash", ContextWindowTokens: 64000},
		}},
	}
}

// --- writeCatalogAgent ------------------------------------------------------
// Duplicated from cliagents/agents_command_test.go.

func writeCatalogAgent(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ext := ".md"
	if strings.HasSuffix(name, ".toml") || strings.HasSuffix(name, ".md") {
		ext = ""
	}
	if err := os.WriteFile(filepath.Join(dir, name+ext), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- serverConfig -----------------------------------------------------------
// Duplicated from cliagents/mcp_session_test.go; the cli test binary must
// spawn itself as the MCP server subprocess.

func serverConfig() config.Resolved {
	return config.Resolved{MCP: config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repo", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: []string{"MIVIA_CLI_MCP_HELPER"},
		TimeoutSeconds: 10,
	}}}}
}

// TestMCPStdioHelper serves an MCP server over standard input and output.
// It is the subprocess behind every stdio MCP server test in this package
// (chat_mcp_entrypoint_integration_test.go, workflow_mcp_test.go): a stdio
// transport spawns os.Args[0] as the server command, and every test that needs
// a stdio MCP stub in this binary reuses this one helper.
func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("MIVIA_CLI_MCP_FAIL") == "1" {
		os.Exit(1)
	}
	if os.Getenv("MIVIA_CLI_MCP_HELPER") != "1" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "returns text"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "reply"}}}, nil, nil
	})
	session, err := server.Connect(context.Background(), &sdk.IOTransport{Reader: os.Stdin, Writer: os.Stdout}, nil)
	if err != nil {
		os.Exit(2)
	}
	if err := session.Wait(); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
