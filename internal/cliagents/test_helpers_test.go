package cliagents

// test_helpers_test.go provides shared test stubs for the cliagents test suite.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestMain wires the func-vars that tests in this package need before any test
// runs. The vars are ordinarily set by internal/cli/cliagents_wiring.go at
// process start; tests in this package use a minimal in-package implementation
// to avoid importing cli (which would create an import cycle).
func TestMain(m *testing.M) {
	NewSessionDispatcherVar = testNewSessionDispatcher
	// WireWorkflowToolOptionsVar is called by ConfigureChatWorkspace. A no-op
	// is sufficient for tests that call ConfigureChatWorkspace without needing
	// workflow tool wiring.
	WireWorkflowToolOptionsVar = func(_ *tools.DefaultOptions, _ string, _ *config.Resolved, _ func() *events.Bus, _ bool, _ bool, _ ledger.LedgerRepository) {
	}
	// RemainderSpoolFromRegistryVar is wired to a no-op; the tests do not need
	// real read_output spool tracking.
	RemainderSpoolFromRegistryVar = func(_ *tools.Registry) *remainder.Spool { return nil }
	// ContextDispatcherForVar returns an empty wiring; tests do not need a real
	// context store or preparation manager.
	ContextDispatcherForVar = func(_ *chat.Session, _ config.SubagentConfig) ContextDispatcherWiring {
		return ContextDispatcherWiring{}
	}
	// BuiltInSlashTokensVar returns an empty set; tests do not need real slash
	// command collision detection.
	BuiltInSlashTokensVar = func() map[string]struct{} { return nil }
	os.Exit(m.Run())
}

// --- type aliases for unexported tests ------------------------------------

// toolTierPlan aliases ToolTierPlan so test files can use the lowercase name.
type toolTierPlan = ToolTierPlan

// schemaMass aliases SchemaMass so test files can use the lowercase name.
type schemaMass = SchemaMass

// contextDispatcherWiring aliases ContextDispatcherWiring for test files.
type contextDispatcherWiring = ContextDispatcherWiring

// --- function aliases for unexported tests --------------------------------

// planToolTiers is a test-file alias for PlanToolTiers.
var planToolTiers = PlanToolTiers

// tieredRootRegistry is a test-file alias for TieredRootRegistry.
var tieredRootRegistry = TieredRootRegistry

// disabledForAgent is a test-file alias for DisabledForAgent.
var disabledForAgent = DisabledForAgent

// registryToolNames returns the name of every tool in reg in list order.
func registryToolNames(reg *tools.Registry) []string {
	if reg == nil {
		return nil
	}
	out := make([]string, 0, len(reg.List()))
	for _, tool := range reg.List() {
		out = append(out, tool.Name())
	}
	return out
}

// --- completer stubs ------------------------------------------------------

// nullCompleter is a stub provider.Completer that always returns empty.
type nullCompleter struct{}

func (nullCompleter) Name() string { return "null" }
func (nullCompleter) Chat(_ context.Context, _ provider.Request) (string, error) {
	return "", nil
}
func (nullCompleter) ChatStream(_ context.Context, _ provider.Request, _ io.Writer) (string, error) {
	return "", nil
}
func (nullCompleter) ChatTurn(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return &provider.Response{}, nil
}

// welcomeStubCompleter is a stub that always returns "ok".
type welcomeStubCompleter struct{}

func (welcomeStubCompleter) Name() string { return "welcome-stub" }
func (welcomeStubCompleter) Chat(_ context.Context, _ provider.Request) (string, error) {
	return "ok", nil
}
func (welcomeStubCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}
func (welcomeStubCompleter) ChatTurn(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "ok"}, nil
}

// stubAgentCompleter is a minimal stub that always answers "ok".
type stubAgentCompleter struct{}

func (stubAgentCompleter) Name() string { return "stub" }
func (stubAgentCompleter) Chat(_ context.Context, _ provider.Request) (string, error) {
	return "ok", nil
}
func (stubAgentCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}
func (stubAgentCompleter) ChatTurn(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "ok"}, nil
}

// scriptedCompleter records the tool list of every request and replays a
// scripted sequence of turns. Tests assert what the model was advertised on
// each request rather than inferring it from the registry.
type scriptedCompleter struct {
	mu sync.Mutex
	// turns are consumed in order; the last one repeats.
	turns []provider.Response
	// advertised is the tool-name list of each request, in request order.
	advertised [][]string
	// toolSpecs is the raw tools[] array of each request, in request order.
	toolSpecs [][]provider.ToolSpec
	// systemPrompts is the system message of each request.
	systemPrompts []string
	calls         int
}

func (c *scriptedCompleter) Name() string { return "scripted" }

func (c *scriptedCompleter) ChatStream(_ context.Context, req provider.Request, w io.Writer) (string, error) {
	resp, _ := c.ChatTurn(context.Background(), req)
	_, _ = io.WriteString(w, resp.Content)
	return resp.Content, nil
}

func (c *scriptedCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *scriptedCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.advertised = append(c.advertised, toolSpecNames(req.Tools))
	c.toolSpecs = append(c.toolSpecs, req.Tools)
	c.systemPrompts = append(c.systemPrompts, systemPromptOf(req.Messages))
	index := c.calls
	c.calls++
	if index >= len(c.turns) {
		index = len(c.turns) - 1
	}
	resp := c.turns[index]
	return &resp, nil
}

func (c *scriptedCompleter) requests() ([][]string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.advertised), slices.Clone(c.systemPrompts)
}

// rawToolSpecs returns the raw tools[] array captured on every request.
func (c *scriptedCompleter) rawToolSpecs() [][]provider.ToolSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.toolSpecs)
}

// --- request inspection helpers ------------------------------------------

func toolSpecNames(specs []provider.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		fn, _ := spec["function"].(map[string]any)
		name, _ := fn["name"].(string)
		out = append(out, name)
	}
	return out
}

func systemPromptOf(msgs []provider.Message) string {
	for _, msg := range msgs {
		if msg.Role == provider.RoleSystem {
			return msg.Content
		}
	}
	return ""
}

// loadToolsCall builds a provider.Response that calls load_tools with the
// given call ID and JSON argument string.
func loadToolsCall(id, args string) provider.Response {
	call := provider.ToolCall{ID: id, Type: "function"}
	call.Function.Name = tools.LoadToolsToolName
	call.Function.Arguments = args
	return provider.Response{ToolCalls: []provider.ToolCall{call}}
}

// captureStderr redirects os.Stderr to a pipe and returns a func that
// restores os.Stderr and returns everything written to it.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = write
	captured := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		captured <- string(data)
	}()
	var once bool
	var result string
	return func() string {
		if !once {
			once = true
			os.Stderr = original
			_ = write.Close()
			result = <-captured
			_ = read.Close()
		}
		return result
	}
}

// --- deferred-fixture helpers --------------------------------------------

// deferredFixture is a fully attached session whose selected agent defers
// everything except the core tools. workflow_run is listed in effective tools
// but is never registered in the base registry (simulating a workspace without
// a .mivia/workflows/ directory), so it triggers the disabled-tool diagnostic
// at attach time.
type deferredFixture struct {
	sess      *chat.Session
	state     *AgentSessionState
	res       *config.Resolved
	completer *scriptedCompleter
	dir       string
	// cleanup is the attach-time cleanup. It is registered with the test and
	// exposed so a test can assert what it actually closes.
	cleanup func()
}

// newDeferredFixture builds a deferred fixture in a fresh temp directory.
func newDeferredFixture(t *testing.T, completer *scriptedCompleter, core []string, effective []string) *deferredFixture {
	t.Helper()
	return newDeferredFixtureIn(t, t.TempDir(), completer, core, effective)
}

// newDeferredFixtureIn is newDeferredFixture over a caller-owned workspace,
// so a test can seed files the attach path must see.
func newDeferredFixtureIn(t *testing.T, dir string, completer *scriptedCompleter, core []string, effective []string) *deferredFixture {
	t.Helper()
	res, sess, selected, reg := buildFixtureSession(t, completer, core, effective)
	state := buildFixtureState(t, dir, sess, selected, reg, completer, res)
	return &deferredFixture{
		sess:      sess,
		state:     state,
		res:       res,
		completer: completer,
		dir:       dir,
		cleanup:   func() {},
	}
}

// buildFixtureSession constructs the session, resolved config, selected agent,
// and agent registry used by newDeferredFixtureIn.
func buildFixtureSession(t *testing.T, completer *scriptedCompleter, core []string, effective []string) (*config.Resolved, *chat.Session, *agents.ResolvedAgent, *agents.AgentRegistry) {
	t.Helper()
	res := &config.Resolved{
		Model: "m", ProviderName: "p",
		Subagents:    config.DefaultSubagentConfig,
		SystemPrompt: "ROOT PROMPT",
	}
	// Build the base registry from the effective list. Exclude workflow_run:
	// it is in the effective list but unregistered when no workflow directory
	// exists, mirroring real cli behavior that triggers the disabled warning.
	base := tools.NewRegistry()
	for _, name := range effective {
		if name != "workflow_run" {
			base.Register(namedTool{name: name})
		}
	}
	var coreField *[]string
	if core != nil {
		c := slices.Clone(core)
		coreField = &c
	}
	selected := &agents.ResolvedAgent{
		Name: "reader", SystemPrompt: "ROOT PROMPT",
		EffectiveTools: slices.Clone(effective),
		CoreTools:      coreField,
	}
	reg := agents.NewRegistry()
	if err := reg.Publish(*selected); err != nil {
		t.Fatal(err)
	}
	sess := chat.NewSession(res, completer)
	sess.Tools = base
	sess.UseTools = true
	sess.SetAgentSettings("ROOT PROMPT", 4, "")
	return res, sess, selected, reg
}

// buildFixtureState attaches the tool surface, dispatcher, and admission
// bindings to sess and returns the resulting AgentSessionState.
func buildFixtureState(t *testing.T, dir string, sess *chat.Session, selected *agents.ResolvedAgent, reg *agents.AgentRegistry, completer *scriptedCompleter, res *config.Resolved) *AgentSessionState {
	t.Helper()
	state := &AgentSessionState{
		Registry: reg, Selected: selected, WorkspaceRoot: dir,
		AllowProjectSkills: true,
		BaselinePrompt:     "ROOT PROMPT", BaselineMaxSteps: 4, BaselineCaptured: true,
	}
	state.ToolBase = sess.Tools
	plan := PlanToolTiers(sess.Tools, selected, res)
	state.TierPlan = plan
	skillReg := skills.NewRegistry()
	state.SkillRegFull = skillReg
	authority, _ := ScopedRootRegistry(sess.Tools, selected, nil)
	WarnDisabledAgentTools(selected, DisabledForAgent(selected, sess.Tools))
	PinAttachAdvertisedToolSpecs(sess, selected, plan, nil)
	sess.Tools = TieredRootRegistry(sess.Tools, selected, nil, plan, nil)
	ApplyDeferredToolPrompt(sess, res, plan, state)
	liveScope := SkillScopeFromAgentAndRegistry(selected, authority)
	state.SetSkillScope(liveScope)
	opts := SessionDispatcherOpts{
		Registry: sess.Tools, AuthorityRegistry: authority,
		Completer: completer, Model: "m", Config: config.DefaultSubagentConfig,
		SkillReg: skillReg, SkillScope: liveScope, AgentRegistry: reg,
		DeferredTools: plan.Candidates, Session: sess,
	}
	dispatcher, err := NewSessionDispatcher(opts)
	if err != nil {
		t.Fatalf("buildFixtureState: dispatcher: %v", err)
	}
	sess.SetDispatcher(dispatcher)
	RecordSchemaMass(sess, state, plan, nil, AgentNameOf(selected), "attach")
	if plan.Deferred() {
		sess.SetSurfaceWidener(NewSurfaceWidener(sess, res, state))
		sess.SetAdmissionBinding(AgentNameOf(selected), plan.Digest)
	}
	sess.SetRemainderSpool(RemainderSpoolFromRegistryVar(sess.Tools))
	t.Cleanup(func() { dispatcher.Close() })
	return state
}

// --- existing test infrastructure ----------------------------------------

// namedTool is a minimal tools.Tool for tests.
type namedTool struct{ name string }

func (t namedTool) Name() string               { return t.name }
func (t namedTool) Description() string        { return t.name }
func (t namedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t namedTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", nil
}

// privilegedNamed is a namedTool that also implements tools.PrivilegedTool.
type privilegedNamed struct{ namedTool }

func (privilegedNamed) Privileged() {}

// intPtr returns a pointer to n.
func intPtr(n int) *int { return &n }

// minimalAgentHandler is a subagent handler that resolves the agent's binding
// and makes one call to the resolved completer. Tests use it to assert which
// provider completer was selected for a given agent definition.
type minimalAgentHandler struct {
	binding AgentBinding
	bindErr error
}

func (h *minimalAgentHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if h.bindErr != nil {
		return nil, h.bindErr
	}
	// Apply the agent's wall-clock ceiling so tests that probe AgentBinding
	// timing behavior do not rely on the full cli dispatcher.
	wallCtx, cancelWall, wallErr := h.binding.WithWallClock(ctx, req.Name)
	if wallErr != nil {
		return nil, wallErr
	}
	defer cancelWall()
	model := h.binding.Model
	if model == "" {
		model = req.Model
	}
	_, err := h.binding.Completer.Chat(wallCtx, provider.Request{Model: model})
	if err != nil {
		return nil, err
	}
	return json.Marshal("done")
}

// minimalSkillHandler is a stub subagent handler for skills. It records
// registration only; tests that need real invocation use NewSessionDispatcherVar
// directly wired from cli.
type minimalSkillHandler struct{}

func (minimalSkillHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	return json.Marshal("done")
}

// testNewSessionDispatcher is the test-only implementation of NewSessionDispatcher.
// It builds a minimal dispatcher that supports:
//   - Agent subagent handlers (binding checked via ResolveAgentBinding)
//   - Skill subagent handlers (filtered by SkillScope)
//   - Deferred-tool pre-condition checking via registerLoadToolsTool
func testNewSessionDispatcher(opts SessionDispatcherOpts) (*runtime.Dispatcher, error) {
	if opts.Registry == nil || opts.Completer == nil {
		return nil, errNilDispatcherDep
	}
	d, err := composition.BuildDispatcher(composition.DispatcherInput{
		Registry:  opts.Registry,
		MaxDepth:  opts.Config.MaxDepth,
		MaxBudget: opts.Config.DefaultBudget,
	})
	if err != nil {
		return nil, err
	}
	if err := testRegisterAgentHandlers(d, opts); err != nil {
		return nil, err
	}
	if err := testRegisterSkillHandlers(d, opts); err != nil {
		return nil, err
	}
	if err := registerLoadToolsTool(d, opts); err != nil {
		return nil, err
	}
	return d, nil
}

func testRegisterAgentHandlers(d *runtime.Dispatcher, opts SessionDispatcherOpts) error {
	if opts.AgentRegistry == nil {
		return nil
	}
	for _, def := range opts.AgentRegistry.List() {
		binding, bindErr := ResolveAgentBinding(def, opts)
		h := &minimalAgentHandler{binding: binding, bindErr: bindErr}
		if err := d.Register(runtime.Subagent, def.Name, h); err != nil {
			return err
		}
	}
	return nil
}

func testRegisterSkillHandlers(d *runtime.Dispatcher, opts SessionDispatcherOpts) error {
	if opts.SkillReg == nil {
		return nil
	}
	// AgentSkillScope{} is the open (unrestricted) zero value; no special-case
	// needed. CheckSkillDefinition on a zero scope permits everything.
	scope := opts.SkillScope
	for _, sk := range opts.SkillReg.List() {
		if err := scope.CheckSkillDefinition(sk); err != nil {
			continue
		}
		if err := d.Register(runtime.Subagent, sk.Name, minimalSkillHandler{}); err != nil {
			return err
		}
	}
	return nil
}

// testEmptySkillRegistry is a skills.Registry that always returns empty for
// tests that do not need skill handlers.
var testEmptySkillRegistry = skills.NewRegistry()

// memoryTestResolved returns a *config.Resolved with memory enabled/disabled,
// suitable for tests that exercise ConfigureChatWorkspace or memory wiring.
func memoryTestResolved(enabled bool) *config.Resolved {
	return &config.Resolved{
		ProviderName: "deepseek",
		Model:        "deepseek-v4-flash",
		Memory:       config.MemoryConfig{Enabled: &enabled, StoreBackend: "markdown"},
	}
}
