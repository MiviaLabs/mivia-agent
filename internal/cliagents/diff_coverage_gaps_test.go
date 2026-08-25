package cliagents

// diff_coverage_gaps_test.go closes the remaining uncovered statement lines
// reported by the diff-coverage gate: small helpers, seam-wiring guards, and
// error branches that the main behavioural tests never reach.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// --- agent_binding.go ------------------------------------------------------

// A factory that succeeds but returns a nil completer must fail closed with
// the "returned nothing" error, not install a nil completer.
func TestResolveAgentBindingFactoryReturnedNoCompleter(t *testing.T) {
	def := agents.ResolvedAgent{
		Name: "routed", Provider: "deepseek", Model: "deepseek-v4-flash",
		EffectiveTools: []string{"read_file"},
	}
	opts := SessionDispatcherOpts{
		Completer:    nullCompleter{},
		Model:        "glm-5.2",
		ProviderName: "zai",
		ModelCatalog: bindingTestCatalog(),
		CompleterFactory: func(string, string) (provider.Completer, error) {
			return nil, nil
		},
	}
	_, err := ResolveAgentBinding(def, opts)
	if err == nil || !strings.Contains(err.Error(), "returned nothing") {
		t.Fatalf("ResolveAgentBinding(nil completer) = %v, want the returned-nothing error", err)
	}
}

// --- agent_catalog.go ------------------------------------------------------

func TestFormatAgentModelQualifiedProvider(t *testing.T) {
	if got := formatAgentModel("zai", "glm-5.2"); got != "zai/glm-5.2" {
		t.Fatalf("formatAgentModel(zai, glm-5.2) = %q, want zai/glm-5.2", got)
	}
}

func TestFormatTraceChainSanitizesEachName(t *testing.T) {
	got := formatTraceChain([]string{"base", "reviewer"})
	if got != "base -> reviewer" {
		t.Fatalf("formatTraceChain = %q, want base -> reviewer", got)
	}
}

// An unparseable workspace mivia.toml must surface as a catalog load error,
// not as an empty catalog.
func TestLoadAgentCatalogRejectsBrokenWorkspaceConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mivia", "mivia.toml"), []byte("agents = ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentCatalog(dir); err == nil {
		t.Fatal("LoadAgentCatalog(broken config) must error")
	}
}

// --- agent_skill_policy.go -------------------------------------------------

func TestResolveTaskRouteErrorBranches(t *testing.T) {
	reg := agents.NewRegistry()
	if err := reg.Publish(agents.ResolvedAgent{Name: "worker", EffectiveTools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	// Unknown agent: agents.Select fails first.
	if _, err := ResolveTaskRoute(reg, nil, "ghost", ""); err == nil {
		t.Fatal("ResolveTaskRoute(unknown agent) must error")
	}
	// Known agent, skill requested, but no skill registry at all.
	if _, err := ResolveTaskRoute(reg, nil, "worker", "deploy"); err == nil {
		t.Fatal("ResolveTaskRoute(nil skill registry) must error")
	}
	// Known agent, skill registry without the named skill.
	empty := skills.NewRegistry()
	if _, err := ResolveTaskRoute(reg, empty, "worker", "deploy"); err == nil {
		t.Fatal("ResolveTaskRoute(unknown skill) must error")
	}
}

func TestResolveTaskRouteSuccessReturnsAgent(t *testing.T) {
	reg := agents.NewRegistry()
	if err := reg.Publish(agents.ResolvedAgent{Name: "worker", EffectiveTools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	skillReg := skills.NewRegistry()
	if err := skillReg.Register(skills.Definition{Name: "deploy", Version: "1", Origin: skills.OriginUser}); err != nil {
		t.Fatal(err)
	}
	agent, err := ResolveTaskRoute(reg, skillReg, "worker", "deploy")
	if err != nil {
		t.Fatalf("ResolveTaskRoute = %v, want nil", err)
	}
	if agent.Name != "worker" {
		t.Fatalf("ResolveTaskRoute returned agent %q, want worker", agent.Name)
	}
}

func TestSkillAllowlistPtrBothScopes(t *testing.T) {
	if got := skillAllowlistPtr(AgentSkillScope{}); got != nil {
		t.Fatalf("skillAllowlistPtr(unrestricted) = %v, want nil", got)
	}
	names := []string{"a", "b"}
	scope := SkillScopeFromAgent(&agents.ResolvedAgent{
		Name: "w", Skills: &names,
	})
	got := skillAllowlistPtr(scope)
	if got == nil || len(*got) != 2 || (*got)[0] != "a" || (*got)[1] != "b" {
		t.Fatalf("skillAllowlistPtr(restricted) = %v, want sorted [a b]", got)
	}
}

// --- agent_switch.go -------------------------------------------------------

func TestAgentSessionStateDisplayAndLedgerHelpers(t *testing.T) {
	var nilState *AgentSessionState
	if got := nilState.DisplayName(); got != "root fallback" {
		t.Fatalf("DisplayName(nil) = %q", got)
	}
	if got := nilState.DisplaySource(); got != "compiled" {
		t.Fatalf("DisplaySource(nil) = %q", got)
	}
	if got := nilState.OwnedLedgerStore(); got != nil {
		t.Fatalf("OwnedLedgerStore(nil) = %v, want nil", got)
	}
	nilState.AdoptLedgerRepo(nil, nil)
	nilState.ReleaseOwnedLedgerRepo()
	if got := nilState.SkillScopeSnapshot(); got.restricted {
		t.Fatal("SkillScopeSnapshot(nil) must be the open zero value")
	}

	selected := agents.ResolvedAgent{
		Name:       "reviewer",
		Provenance: agents.Provenance{Source: config.AgentSourceWorkspace, Path: "/tmp/a.toml"},
	}
	state := &AgentSessionState{Selected: &selected}
	if got := state.DisplayName(); got != "reviewer" {
		t.Fatalf("DisplayName(selected) = %q, want reviewer", got)
	}
	if got := state.DisplaySource(); got != "workspace" {
		t.Fatalf("DisplaySource(selected) = %q, want workspace", got)
	}
	// Adopt/Release on a live state must round-trip without panicking.
	state.AdoptLedgerRepo(nil, nil)
	if got := state.OwnedLedgerStore(); got != nil {
		t.Fatalf("OwnedLedgerStore(after adopting nil) = %v, want nil", got)
	}
	state.ReleaseOwnedLedgerRepo()
}

// A widened-surface build without a captured skill registry must refuse
// instead of silently re-reading disk.
func TestBuildWidenedWithoutSkillRegistryFails(t *testing.T) {
	_, err := BuildWidenedWith(nil, nil, &AgentSessionState{}, nil)
	if err == nil || !strings.Contains(err.Error(), "skill registry") {
		t.Fatalf("BuildWidenedWith(empty state) = %v, want the missing-skill-registry error", err)
	}
}

// --- agents_command.go -----------------------------------------------------

func TestRunAgentsRejectsUnknownSubcommand(t *testing.T) {
	if err := RunAgents([]string{"bogus"}); err == nil {
		t.Fatal("RunAgents(bogus) must error")
	}
}

func TestRunAgentsExplainUnknownAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errOut strings.Builder
	err := RunAgentsWithIO([]string{"explain", "ghost"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("RunAgentsWithIO(explain ghost) = %v, want the unknown-agent error", err)
	}
	if !strings.Contains(out.String(), "agents:") {
		t.Fatalf("explain fallback must print the catalog, got %q", out.String())
	}
}

// --- dispatcher_wiring.go --------------------------------------------------

func TestNewSessionDispatcherWithoutWiringFails(t *testing.T) {
	saved := NewSessionDispatcherVar
	NewSessionDispatcherVar = nil
	defer func() { NewSessionDispatcherVar = saved }()
	if _, err := NewSessionDispatcher(SessionDispatcherOpts{}); err == nil ||
		!strings.Contains(err.Error(), "not wired") {
		t.Fatalf("NewSessionDispatcher(unwired) = %v, want the not-wired error", err)
	}
}

func TestRegisterSessionToolGuards(t *testing.T) {
	reg := tools.NewRegistry()
	// A tool without the PrivilegedTool marker is refused.
	if err := RegisterSessionTool(nil, reg, namedTool{name: "plain"}); err == nil ||
		!strings.Contains(err.Error(), "PrivilegedTool") {
		t.Fatalf("RegisterSessionTool(non-privileged) = %v, want the privileged-marker error", err)
	}
	// A name the dispatcher already holds is refused as a duplicate, even
	// when the registry itself does not know it yet.
	dup := tools.NewRegistry()
	dup.Register(namedTool{name: "dup"})
	d, err := composition.BuildDispatcher(composition.DispatcherInput{Registry: dup})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	freshReg := tools.NewRegistry()
	err = RegisterSessionTool(d, freshReg, privilegedNamed{namedTool{name: "dup"}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("RegisterSessionTool(duplicate dispatcher name) = %v, want a duplicate error", err)
	}
}

// --- load_tools_tool.go ----------------------------------------------------

func TestTruncatePreviewUTF8(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"short", 100, "short"}, // maxBytes >= len: unchanged
		{"anything", 0, ""},     // non-positive max: empty
		{"anything", -3, ""},    // negative max: empty
		{"héllo", 2, "h"},       // must back up to a valid boundary
		{"héllo", 3, "hé"},      // exact boundary stays
		{"abcdef", 3, "abc"},    // ascii cut is a boundary already
	}
	for _, tc := range cases {
		if got := truncatePreviewUTF8(tc.s, tc.max); got != tc.want {
			t.Errorf("truncatePreviewUTF8(%q, %d) = %q, want %q", tc.s, tc.max, got, tc.want)
		}
	}
}

// --- mcp_scope.go ----------------------------------------------------------

func TestAuthorizedAgentToolsNilBranches(t *testing.T) {
	if got := AuthorizedAgentTools(nil, nil); got != nil {
		t.Fatalf("AuthorizedAgentTools(nil agent) = %v, want nil", got)
	}
	agent := &agents.ResolvedAgent{EffectiveTools: []string{"read_file"}}
	if got := AuthorizedAgentTools(agent, nil); len(got) != 1 || got[0] != "read_file" {
		t.Fatalf("AuthorizedAgentTools(nil registry) = %v, want [read_file]", got)
	}
	if isMCPServerTool("mcp__any__x1", nil) {
		t.Fatal("isMCPServerTool(nil agent) must be false")
	}
}

func TestWorkflowMCPServersNilAndUnknownAgent(t *testing.T) {
	if got := WorkflowMCPServers(nil, nil); got != nil {
		t.Fatalf("WorkflowMCPServers(nil) = %v, want nil", got)
	}
	// A step naming an agent that is not registered contributes nothing.
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "worker", EffectiveMCPServers: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	wf := &definition.CompiledWorkflow{Steps: []definition.Step{
		{ID: "ghost-step", Agent: "ghost"},
		{ID: "work", Agent: "worker"},
	}}
	if got := WorkflowMCPServers(wf, registry); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("WorkflowMCPServers(unknown agent) = %v, want [alpha]", got)
	}
}

// --- mcp_session.go --------------------------------------------------------

func TestAddMCPToolsUnknownServerErrors(t *testing.T) {
	res := config.Resolved{MCP: config.MCPConfig{Enabled: true}}
	if _, err := AddMCPTools(tools.NewRegistry(), &res, []string{"ghost"}); err == nil {
		t.Fatal("AddMCPTools(unknown server) must error")
	}
}

func TestEnsureSelectedMCPToolsNilStateIsNoOp(t *testing.T) {
	if err := ensureSelectedMCPTools(nil, agents.ResolvedAgent{EffectiveMCPServers: []string{"repo"}}); err != nil {
		t.Fatalf("ensureSelectedMCPTools(nil state) = %v, want nil", err)
	}
}

// --- memory_support.go -----------------------------------------------------

// errCoreStore fails CoreEntries so the err branch of coreMemoryBlock runs.
type errCoreStore struct{ memory.Store }

func (errCoreStore) CoreEntries(context.Context, memory.Scope) ([]memory.Result, error) {
	return nil, errors.New("core entries unavailable")
}

func TestCoreMemoryBlockBranches(t *testing.T) {
	mc := config.MemoryConfig{InjectCore: true}
	if got := coreMemoryBlock(context.Background(), errCoreStore{}, memory.ScopeProject, mc); got != "" {
		t.Fatalf("coreMemoryBlock(erroring store) = %q, want empty", got)
	}
	store, err := memory.Open(memory.Config{Backend: memory.BackendMemory})
	if err != nil {
		t.Fatal(err)
	}
	// No core entries yet: still empty.
	if got := coreMemoryBlock(context.Background(), store, memory.ScopeProject, mc); got != "" {
		t.Fatalf("coreMemoryBlock(no core entries) = %q, want empty", got)
	}
	// Two promoted entries render as two dash lines.
	for _, title := range []string{"one", "two"} {
		res, err := store.Save(context.Background(), memory.Entry{
			Title: title, Scope: memory.ScopeProject, Verdict: memory.VerdictGood,
			Summary: "summary " + title, Why: "coverage fixture",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PromoteToCore(context.Background(), res.ID); err != nil {
			t.Fatal(err)
		}
	}
	got := coreMemoryBlock(context.Background(), store, memory.ScopeProject, mc)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "- one: ") || !strings.HasPrefix(lines[1], "- two: ") {
		t.Fatalf("coreMemoryBlock(two core entries) = %q, want two dash lines", got)
	}
}

func TestMemoryOfAndMemoryConfigOfLiveState(t *testing.T) {
	store, err := memory.Open(memory.Config{Backend: memory.BackendMemory})
	if err != nil {
		t.Fatal(err)
	}
	state := &AgentSessionState{Memory: store, MemoryConfig: config.MemoryConfig{InjectCore: true}}
	if MemoryOf(state) != store {
		t.Fatal("MemoryOf(state) must return the state's store")
	}
	if !MemoryConfigOf(state).InjectCore {
		t.Fatal("MemoryConfigOf(state) must return the state's config")
	}
}

// --- model_binding.go ------------------------------------------------------

func TestNewProviderCompleterFactoryClosureFailsClosed(t *testing.T) {
	factory := NewProviderCompleterFactory(&config.Resolved{})
	if factory == nil {
		t.Fatal("NewProviderCompleterFactory(non-nil res) must return a factory")
	}
	// A provider with no configured runtime must fail closed rather than
	// silently falling back to the session completer.
	if _, err := factory("no-such-provider", "some-model"); err == nil {
		t.Fatal("factory(no-such-provider) must error")
	}
	if got := NewProviderCompleterFactory(nil); got != nil {
		t.Fatal("NewProviderCompleterFactory(nil) must return nil")
	}
}
