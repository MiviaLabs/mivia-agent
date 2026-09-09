package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// bindDeferredFixtureContext binds an already-built fixture's session to a
// fresh, isolated context store, so Save/Load exercise the durable catalog -
// the only persistence path since the legacy file-backed session store was
// removed.
func bindDeferredFixtureContext(t *testing.T, sess *chat.Session) {
	t.Helper()
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
}

// scriptedCompleter records the tool list of every request and replays a
// scripted sequence of turns, so a test can assert what the model was
// advertised on each request rather than inferring it from the registry.
type scriptedCompleter struct {
	mu sync.Mutex
	// turns are consumed in order; the last one repeats.
	turns []provider.Response
	// advertised is the tool-name list of each request, in request order.
	advertised [][]string
	// toolSpecs is the raw tools[] array of each request, in request order,
	// for byte-identity assertions (plan tools-advertising/01).
	toolSpecs [][]provider.ToolSpec
	// systemPrompts is the system message of each request.
	systemPrompts []string
	// messages is the full message list of each request, so a test can read
	// the tool-role results the model was handed - which is where each
	// execution path's answer about a call actually lands.
	messages [][]provider.Message
	calls    int
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
	c.messages = append(c.messages, req.Messages)
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

// rawToolSpecs returns the raw tools[] array captured on every request, for
// byte-identity assertions (plan tools-advertising/01).
func (c *scriptedCompleter) rawToolSpecs() [][]provider.ToolSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.toolSpecs)
}

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

func loadToolsCall(id, args string) provider.Response {
	call := provider.ToolCall{ID: id, Type: "function"}
	call.Function.Name = tools.LoadToolsToolName
	call.Function.Arguments = args
	return provider.Response{ToolCalls: []provider.ToolCall{call}}
}

func toolCallResponse(calls ...provider.ToolCall) provider.Response {
	return provider.Response{ToolCalls: calls}
}

func namedCall(id, name, args string) provider.ToolCall {
	call := provider.ToolCall{ID: id, Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = args
	return call
}

// deferredFixture is a fully attached session whose selected agent defers
// everything except read_file.
type deferredFixture struct {
	sess      *chat.Session
	state     *AgentSessionState
	res       *config.Resolved
	completer *scriptedCompleter
	dir       string
	// cleanup is the attach-time session cleanup. It is registered with the
	// test, and exposed so a test can assert what it actually closes.
	cleanup func()
}

func newDeferredFixture(t *testing.T, completer *scriptedCompleter, core []string, effective []string, probes ...tools.Tool) *deferredFixture {
	t.Helper()
	return newDeferredFixtureIn(t, t.TempDir(), completer, core, effective, probes...)
}

// newDeferredFixtureIn is newDeferredFixture over a caller-owned workspace, so
// a test can seed skills or files the attach path must see.
func newDeferredFixtureIn(t *testing.T, dir string, completer *scriptedCompleter, core []string, effective []string, probes ...tools.Tool) *deferredFixture {
	t.Helper()
	res := &config.Resolved{Model: "m", ProviderName: "p", Subagents: config.DefaultSubagentConfig, SystemPrompt: "ROOT PROMPT"}
	return newDeferredFixtureWith(t, dir, res, completer, core, effective, probes...)
}

// newDeferredFixtureWith is newDeferredFixtureIn over a caller-owned config, so
// a test that drives the real /model path can supply a provider the completer
// factory is actually able to construct.
// probes are extra tools registered into the full set before scope and the
// tier split run, so a test can drive a tool whose behaviour it controls
// through the REAL attach path. The tool-execution conformance suite needs
// this: it must observe what each execution path does to an ordinary tool,
// and the default registry has none whose timing or failure it can dictate.
func newDeferredFixtureWith(t *testing.T, dir string, res *config.Resolved, completer *scriptedCompleter, core []string, effective []string, probes ...tools.Tool) *deferredFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	for _, probe := range probes {
		full.Register(probe)
	}
	coreCopy := slices.Clone(core)
	selected := &agents.ResolvedAgent{
		Name:           "reader",
		SystemPrompt:   "ROOT PROMPT",
		EffectiveTools: slices.Clone(effective),
		CoreTools:      &coreCopy,
	}
	reg := agents.NewRegistry()
	if err := reg.Publish(*selected); err != nil {
		t.Fatal(err)
	}
	sess := chat.NewSession(res, completer)
	sess.Tools = full
	sess.UseTools = true
	sess.SetAgentSettings("ROOT PROMPT", 4, "")
	state := &AgentSessionState{
		Registry: reg, Selected: selected, WorkspaceRoot: dir, AllowProjectSkills: true,
		BaselinePrompt: "ROOT PROMPT", BaselineMaxSteps: 4, BaselineCaptured: true,
	}
	cleanup, err := attachSessionDispatcher(sess, dir, res.Model, res.Subagents, state, nil,
		sessionRouting{Resolved: res})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(cleanup)
	return &deferredFixture{sess: sess, state: state, res: res, completer: completer, dir: dir, cleanup: cleanup}
}

func registryToolNames(reg *tools.Registry) []string {
	out := make([]string, 0, len(reg.List()))
	for _, tool := range reg.List() {
		out = append(out, tool.Name())
	}
	return out
}

// --- the tier split itself ---------------------------------------------

func TestDeferredLoadingIsInertWithoutCoreConfig(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, nil, []string{"read_file", "grep", "glob"})
	names := registryToolNames(fixture.sess.Tools)
	if slices.Contains(names, tools.LoadToolsToolName) {
		t.Fatalf("load_tools registered with no core configured: %v", names)
	}
	for _, want := range []string{"read_file", "grep", "glob"} {
		if !slices.Contains(names, want) {
			t.Fatalf("tool %q missing from an inert surface: %v", want, names)
		}
	}
	prompt, _ := fixture.sess.AgentSettings()
	if prompt != "ROOT PROMPT" {
		t.Fatalf("inert session prompt = %q, want the agent prompt unchanged", prompt)
	}
}

func TestDeferredLoadingAdvertisesOnlyCorePlusLoadTools(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	names := registryToolNames(fixture.sess.Tools)
	if slices.Contains(names, "grep") || slices.Contains(names, "glob") {
		t.Fatalf("deferred tools were advertised: %v", names)
	}
	if !slices.Contains(names, "read_file") || !slices.Contains(names, tools.LoadToolsToolName) {
		t.Fatalf("core surface = %v, want read_file and load_tools", names)
	}
	prompt, _ := fixture.sess.AgentSettings()
	if !strings.Contains(prompt, "- grep") || !strings.Contains(prompt, "- glob") {
		t.Fatalf("prompt is missing the frozen deferred index:\n%s", prompt)
	}
}

// --- D6: stage inside the turn, publish at the boundary -----------------

// TestAdmittedToolReachesTheNextRequest is the plan tools-advertising/01
// headline sequence: the wire tools[] array is pinned for the whole binding,
// so grep is already advertised on request 0 (the load_tools call itself,
// before anything is admitted) and stays byte-identical across every
// subsequent request - the admission that follows changes EXECUTION
// authority (what load_tools makes callable) only, never what is advertised.
// This is what keeps a provider's implicit prompt-cache prefix intact across
// a mid-turn load_tools call.
func TestAdmittedToolReachesTheNextRequest(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "staged"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})

	if _, err := fixture.sess.SendUser(context.Background(), "find something", io.Discard); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	advertised, _ := fixture.completer.requests()
	if len(advertised) < 2 {
		t.Fatalf("expected at least two requests in the staging turn, got %d", len(advertised))
	}
	// Request 0 is the load_tools call itself: grep is already advertised
	// there - it was authorized and part of the union from the start.
	if !slices.Contains(advertised[0], "grep") {
		t.Fatalf("request 0 (the load_tools call) did not advertise grep: %v", advertised[0])
	}
	// Request 1 is the next request: the wire array is byte-identical to
	// request 0's, even though the step-boundary publication ran in between
	// and made grep callable (execution authority, not advertising).
	raw := fixture.completer.rawToolSpecs()
	first, err := json.Marshal(raw[0])
	if err != nil {
		t.Fatalf("marshal request 0 tools: %v", err)
	}
	second, err := json.Marshal(raw[1])
	if err != nil {
		t.Fatalf("marshal request 1 tools: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("advertised tools[] changed between request 0 and request 1:\n%s\n%s", first, second)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep] after the step-boundary publication", got)
	}

	before := len(advertised)
	if _, err := fixture.sess.SendUser(context.Background(), "now grep", io.Discard); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	advertised, prompts := fixture.completer.requests()
	if !slices.Contains(advertised[before], "grep") {
		t.Fatalf("first request of the next turn = %v, want grep advertised", advertised[before])
	}
	raw = fixture.completer.rawToolSpecs()
	third, err := json.Marshal(raw[before])
	if err != nil {
		t.Fatalf("marshal next-turn tools: %v", err)
	}
	if string(first) != string(third) {
		t.Fatalf("advertised tools[] changed across a turn boundary:\n%s\n%s", first, third)
	}
	// D8: the frozen index keeps system-prompt bytes stable across the
	// admission, so the cached prefix survives.
	if prompts[0] != prompts[before] {
		t.Fatalf("system prompt changed across an admission:\n%q\n%q", prompts[0], prompts[before])
	}
}

// TestAdmittedToolIsAppendedAsATail pins the D8 ordering contract: the core
// block is byte-identical before and after, and the admitted tool lands after
// it rather than materializing inside it.
func TestAdmittedToolIsAppendedAsATail(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["glob"]}`),
		{Content: "staged"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file", "list_dir"}, []string{"read_file", "list_dir", "grep", "glob"})
	before := registryToolNames(fixture.sess.Tools)

	if _, err := fixture.sess.SendUser(context.Background(), "load glob", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	after := registryToolNames(fixture.sess.Tools)
	if len(after) != len(before)+1 {
		t.Fatalf("surface = %v, want exactly one more tool than %v", after, before)
	}
	// The core block keeps its position and order; the privileged session
	// tools the dispatcher registers sit behind the appended tail.
	coreLen := slices.Index(before, "dispatch_tasks")
	if coreLen <= 0 {
		t.Fatalf("privileged session tools do not follow the core block: %v", before)
	}
	if !slices.Equal(before[:coreLen], after[:coreLen]) {
		t.Fatalf("core block changed: %v -> %v", before[:coreLen], after[:coreLen])
	}
	if after[coreLen] != "glob" {
		t.Fatalf("admitted tool at %d = %q, want glob appended directly after the core block (%v)", coreLen, after[coreLen], after)
	}
}

// TestSiblingToolCallsCompleteInTheSameBatchAsLoadTools is F2/§7: the
// dispatcher executing the batch that contains load_tools is never closed
// mid-batch, so the sibling calls still run.
func TestSiblingToolCallsCompleteInTheSameBatchAsLoadTools(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		toolCallResponse(
			namedCall("c1", tools.LoadToolsToolName, `{"names":["grep"]}`),
			namedCall("c2", "list_dir", `{"path":"."}`),
		),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file", "list_dir"}, []string{"read_file", "list_dir", "grep"})
	if _, err := fixture.sess.SendUser(context.Background(), "both", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	var sawListDir bool
	for _, msg := range fixture.sess.MessagesCopy() {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "c2" {
			sawListDir = true
			if strings.Contains(strings.ToLower(msg.Content), "unknown tool") ||
				strings.Contains(strings.ToLower(msg.Content), "closed") {
				t.Fatalf("sibling call failed alongside load_tools: %q", msg.Content)
			}
		}
	}
	if !sawListDir {
		t.Fatal("sibling list_dir call produced no tool result")
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep]", got)
	}
}

// --- caps (F7) ----------------------------------------------------------

func TestLoadToolsIdempotentCallsAreFree(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	result, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0)
	if err != nil {
		t.Fatalf("idempotent stage: %v", err)
	}
	if len(result.Staged) != 0 || !slices.Equal(result.Already, []string{"grep"}) {
		t.Fatalf("idempotent stage = %+v, want already=[grep] and nothing staged", result)
	}
	if _, ok := fixture.sess.PendingAdmission(); ok {
		t.Fatal("an already-loaded tool must not create a pending stage")
	}
}

func TestLoadToolsAttemptBoundStopsALoopingModel(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	tool, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	if !ok {
		t.Fatal("load_tools is not registered")
	}
	// The bound is charged at the tool, not at staging: the loop that matters
	// is a model re-calling load_tools, however each call ends. Names it keeps
	// getting wrong are the canonical loop - they never reach staging at all.
	for i := 0; i < tools.MaxAdmissionAttempts; i++ {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["no_such_tool"]}`)); err == nil {
			t.Fatalf("attempt %d: an unknown name was accepted", i)
		}
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`))
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("error = %v, want the attempt bound of %d surfaced", err, tools.MaxAdmissionAttempts)
	}
}

// TestNoOpLoadToolsCallsDoNotBurnTheGenuineBudget: the frozen index (D8) keeps
// listing loaded tools as loadable, so the model is invited to re-request them.
// Those calls are refunded - otherwise the invitation itself exhausts the
// budget a real request needs - and separately bounded, so the refund cannot
// become a free loop.
func TestNoOpLoadToolsCallsDoNotBurnTheGenuineBudget(t *testing.T) {
	// The deferred set has to be wide enough that the genuine requests which
	// break the no-op streak never repeat a name.
	deferred := []string{"grep", "list_dir", "glob", "write_file", "search_replace",
		"multi_edit", "search", "fetch_url", "find_references"}
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, append([]string{"read_file"}, deferred...))
	tool, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	if !ok {
		t.Fatal("load_tools is not registered")
	}
	call := func(name string) (string, error) {
		return tool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"names":[%q]}`, name)))
	}
	// Drive the call count past MaxAdmissionAttempts, so the closing genuine
	// request is affordable ONLY because the no-ops were refunded. One genuine
	// request every fourth call keeps the consecutive-no-op streak under its
	// bound; every other call re-requests the already-staged grep.
	//
	// budgeted is chosen so that unrefunded the closing call is one past the
	// attempt bound, and refunded it still fits.
	budgeted := tools.MaxAdmissionAttempts
	genuine := 0
	for i := 0; i < budgeted; i++ {
		if i%(maxNoOpsBetweenGenuineCalls+1) == 0 {
			if _, err := call(deferred[genuine]); err != nil {
				t.Fatalf("genuine request %d (call %d): %v", genuine, i, err)
			}
			genuine++
			continue
		}
		out, err := call("grep")
		if err != nil {
			t.Fatalf("no-op at call %d: %v", i, err)
		}
		if !strings.Contains(out, "grep") || !strings.Contains(out, "NOT updated") {
			t.Fatalf("no-op result = %q, want it to say the frozen index is not updated", out)
		}
	}
	// The genuine request that follows must still be affordable: the refunded
	// no-ops did not eat the attempt budget. Without the refund this call is
	// past MaxAdmissionAttempts and fails as exhausted.
	if _, err := call(deferred[genuine]); err != nil {
		t.Fatalf("a genuine request after %d calls (bound %d): %v", budgeted, tools.MaxAdmissionAttempts, err)
	}
}

// maxNoOpsBetweenGenuineCalls mirrors internal/chat's consecutive-no-op bound.
// internal/cli cannot import the unexported constant, so the coupling is stated
// here; the test above fails loudly if the real bound is smaller.
const maxNoOpsBetweenGenuineCalls = 3

// TestNoOpLoadToolsIsCorrectivelyBounded: the streak bound is corrective, not
// fatal - a model that loops on already-loaded names is told to stop.
func TestNoOpLoadToolsIsCorrectivelyBounded(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	tool, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	if !ok {
		t.Fatal("load_tools is not registered")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`)); err != nil {
		t.Fatalf("first load: %v", err)
	}
	var lastErr error
	for i := 0; i < 10; i++ {
		_, lastErr = tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`))
		if lastErr != nil {
			break
		}
	}
	if lastErr == nil {
		t.Fatal("an unbounded no-op loop was allowed")
	}
}

// TestReRequestingAStagedToolRunsNowEvenWhenUnpublished was R3: staging took
// effect at the NEXT turn (D6), so a name staged earlier in this same turn was
// reported as not callable. Hot-serve inverted the promise: calling a staged
// tool runs immediately (the synchronous serve), while native publication -
// what puts it in sess.Tools - still waits for the boundary. The re-request
// must keep the two states apart: staged means "runs now, not yet in the
// registry", never "already loaded".
func TestReRequestingAStagedToolRunsNowEvenWhenUnpublished(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	tool, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	if !ok {
		t.Fatal("load_tools is not registered")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`)); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if _, callable := fixture.sess.Tools.Get("grep"); callable {
		t.Fatal("a staged tool was natively admitted inside the staging turn")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`))
	if err != nil {
		t.Fatalf("re-request: %v", err)
	}
	if strings.Contains(out, "callable now") {
		t.Fatalf("a staged-but-unpublished tool was listed under the already-loaded promise: %q", out)
	}
	if !strings.Contains(out, "run immediately") {
		t.Fatalf("re-requesting a staged tool gave no hot-serve signal: %q", out)
	}
}

// TestMixedLoadToolsResultSeparatesLoadedFromStaged is R4: one result must not
// put names in identical states on both sides of the callable line.
func TestMixedLoadToolsResultSeparatesLoadedFromStaged(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	// A completed turn publishes grep, so it really is callable now.
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	tool, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	if !ok {
		t.Fatal("load_tools is not registered")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["glob"]}`)); err != nil {
		t.Fatalf("stage glob: %v", err)
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep","glob"]}`))
	if err != nil {
		t.Fatalf("mixed re-request: %v", err)
	}
	if !strings.Contains(out, "callable now") || !strings.Contains(out, "run immediately") {
		t.Fatalf("mixed result does not distinguish the two states at all: %q", out)
	}
	loaded := lineWithPrefix(out, "already loaded: ")
	if loaded == "" {
		t.Fatalf("mixed result lists nothing as already loaded: %q", out)
	}
	if strings.Contains(loaded, "glob") {
		t.Fatalf("the staged-but-unpublished glob was listed as already loaded: %q", out)
	}
	if !strings.Contains(loaded, "grep") {
		t.Fatalf("the published grep was not listed as already loaded: %q", out)
	}
	staged := lineWithPrefix(out, "already staged: ")
	if !strings.Contains(staged, "glob") {
		t.Fatalf("the staged glob was not listed as already staged: %q", out)
	}
}

// lineWithPrefix returns the first line of s starting with prefix, or "".
func lineWithPrefix(s, prefix string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func TestLoadToolsRejectsUnauthorizedNames(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	tool, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	if !ok {
		t.Fatal("load_tools is not registered")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["write_file"]}`))
	if err == nil {
		t.Fatalf("unauthorized name was accepted: %q", out)
	}
	if !strings.Contains(err.Error(), "grep") {
		t.Fatalf("error should list the loadable candidates: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v, want nothing after a refused call", got)
	}
}

func TestLoadToolsQueryMatchesDeferredDescriptions(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	tool, _ := fixture.sess.Tools.Get(tools.LoadToolsToolName)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"grep"}`))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(out, "grep") {
		t.Fatalf("query result = %q, want grep staged", out)
	}
	if !strings.Contains(out, "run immediately") {
		t.Fatalf("result must state the hot-serve promise honestly: %q", out)
	}
}
