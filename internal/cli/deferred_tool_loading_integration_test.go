package cli

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
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// scriptedCompleter records the tool list of every request and replays a
// scripted sequence of turns, so a test can assert what the model was
// advertised on each request rather than inferring it from the registry.
type scriptedCompleter struct {
	mu sync.Mutex
	// turns are consumed in order; the last one repeats.
	turns []provider.Response
	// advertised is the tool-name list of each request, in request order.
	advertised [][]string
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
	state     *agentSessionState
	res       *config.Resolved
	completer *scriptedCompleter
	dir       string
	// cleanup is the attach-time session cleanup. It is registered with the
	// test, and exposed so a test can assert what it actually closes.
	cleanup func()
}

func newDeferredFixture(t *testing.T, completer *scriptedCompleter, core []string, effective []string) *deferredFixture {
	t.Helper()
	return newDeferredFixtureIn(t, t.TempDir(), completer, core, effective)
}

// newDeferredFixtureIn is newDeferredFixture over a caller-owned workspace, so
// a test can seed skills or files the attach path must see.
func newDeferredFixtureIn(t *testing.T, dir string, completer *scriptedCompleter, core []string, effective []string) *deferredFixture {
	t.Helper()
	res := &config.Resolved{Model: "m", ProviderName: "p", Subagents: config.DefaultSubagentConfig, SystemPrompt: "ROOT PROMPT"}
	return newDeferredFixtureWith(t, dir, res, completer, core, effective)
}

// newDeferredFixtureWith is newDeferredFixtureIn over a caller-owned config, so
// a test that drives the real /model path can supply a provider the completer
// factory is actually able to construct.
func newDeferredFixtureWith(t *testing.T, dir string, res *config.Resolved, completer *scriptedCompleter, core []string, effective []string) *deferredFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
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
	state := &agentSessionState{
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

// TestAdmittedToolReachesTheNextRequest is the plan's headline ordering
// sequence: load_tools stages, the turn commits, the surface generation bumps,
// and the NEXT request carries the new schema - never the current one.
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
	for i, names := range advertised {
		if slices.Contains(names, "grep") {
			t.Fatalf("request %d in the staging turn already advertised grep: %v", i, names)
		}
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep] after the turn boundary", got)
	}

	before := len(advertised)
	if _, err := fixture.sess.SendUser(context.Background(), "now grep", io.Discard); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	advertised, prompts := fixture.completer.requests()
	if !slices.Contains(advertised[before], "grep") {
		t.Fatalf("first request of the next turn = %v, want grep advertised", advertised[before])
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
	coreLen := slices.Index(before, "delegate")
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

// TestReRequestingAStagedToolIsNotCalledCallableNow is R3: staging takes effect
// at the NEXT turn (D6), so a name staged earlier in this same turn is not
// callable. Reporting it under "callable now" tells the model to call a tool
// that will fail with unknown-tool, and a pure re-request emits no other line.
func TestReRequestingAStagedToolIsNotCalledCallableNow(t *testing.T) {
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
		t.Fatal("a staged tool became callable inside the staging turn")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`))
	if err != nil {
		t.Fatalf("re-request: %v", err)
	}
	if strings.Contains(out, "callable now") {
		t.Fatalf("a staged-but-unpublished tool was reported as callable now: %q", out)
	}
	if !strings.Contains(out, "next turn") {
		t.Fatalf("re-requesting a staged tool gave no next-turn signal: %q", out)
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
	if !strings.Contains(out, "callable now") || !strings.Contains(out, "next turn") {
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
	if !strings.Contains(out, "next turn") {
		t.Fatalf("result must state the next-turn availability honestly: %q", out)
	}
}

// --- D4/R2-5: stage and admission lifecycle -----------------------------

func TestAgentSwitchResetsAdmissions(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("precondition: admitted = %v", got)
	}
	writer := agents.ResolvedAgent{Name: "writer", SystemPrompt: "W", EffectiveTools: []string{"read_file", "grep"}}
	if err := fixture.state.Registry.Publish(writer); err != nil {
		t.Fatal(err)
	}
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "writer", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v, want empty after an /agent switch", got)
	}
}

func TestStagedAdmissionDiesWhenTheBindingIsReplaced(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	stage, ok := fixture.sess.PendingAdmission()
	if !ok {
		t.Fatal("no pending stage")
	}
	writer := agents.ResolvedAgent{Name: "writer", SystemPrompt: "W", EffectiveTools: []string{"read_file", "grep"}}
	if err := fixture.state.Registry.Publish(writer); err != nil {
		t.Fatal(err)
	}
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "writer", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	fixture.sess.PublishPendingAdmission()
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("a stage from generation %d published into a replaced binding: %v", stage.SurfaceGeneration, got)
	}
}

// TestBackgroundWorkDefersTheAdmission is R2-2: while an owner-registered
// switch guard refuses, publishing would close a dispatcher background
// orchestration still holds. The stage waits instead, and says so.
func TestBackgroundWorkDefersTheAdmission(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SetSwitchGuard(func() error { return fmt.Errorf("background run active") })

	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v while background work held the dispatcher", got)
	}
	if _, ok := fixture.sess.PendingAdmission(); !ok {
		t.Fatal("the stage must stay pending for the next qualifying boundary")
	}
	notes := fixture.sess.TakeAdmissionNotes()
	if len(notes) == 0 || !strings.Contains(notes[0], "grep") {
		t.Fatalf("notes = %v, want a bounded deferral note naming grep", notes)
	}

	// Once the guard clears, the next turn boundary publishes it.
	fixture.sess.SetSwitchGuard(nil)
	completer.mu.Lock()
	completer.turns = []provider.Response{{Content: "done"}}
	completer.calls = 0
	completer.mu.Unlock()
	if _, err := fixture.sess.SendUser(context.Background(), "again", io.Discard); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep] at the next allowed boundary", got)
	}
}

func TestDeferralNotesAreBounded(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SetSwitchGuard(func() error { return fmt.Errorf("busy") })
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	for i := 0; i < 10; i++ {
		fixture.sess.PublishPendingAdmission()
	}
	if notes := fixture.sess.TakeAdmissionNotes(); len(notes) > 3 {
		t.Fatalf("%d deferral notes queued; the note must be bounded", len(notes))
	}
}

// --- D3: persistence and resume replay ----------------------------------

func TestAdmittedToolsSurviveSaveAndLoad(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SessionDir = t.TempDir()
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if err := fixture.sess.Save("snap"); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Drop the live surface back to core, then resume.
	fixture.sess.ResetAdmissions()
	if err := fixture.sess.Load("snap"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted after resume = %v, want [grep]", got)
	}
	if !slices.Contains(registryToolNames(fixture.sess.Tools), "grep") {
		t.Fatalf("resumed surface does not advertise grep: %v", registryToolNames(fixture.sess.Tools))
	}
}

// TestResumeDropsAStaleAdmittedSetWithANote is F6: when the tier split has
// changed under a saved session, the admitted names are dropped fail-closed
// and the user is told which ones.
func TestResumeDropsAStaleAdmittedSetWithANote(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	fixture.sess.SessionDir = t.TempDir()
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if err := fixture.sess.Save("snap"); err != nil {
		t.Fatalf("save: %v", err)
	}
	_ = fixture.sess.TakeAdmissionNotes()
	// The operator re-tiers the agent: the digest no longer matches.
	fixture.sess.SetAdmissionBinding("reader", "a-different-digest")
	fixture.sess.ResetAdmissions()
	if err := fixture.sess.Load("snap"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v, want the stale set dropped fail-closed", got)
	}
	notes := fixture.sess.TakeAdmissionNotes()
	if len(notes) == 0 || !strings.Contains(notes[0], "grep") {
		t.Fatalf("notes = %v, want a note naming the dropped tools", notes)
	}
}

// --- telemetry (D5) -----------------------------------------------------

func TestSchemaMassReportsWhatTheDeferredTierWithholds(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	mass := fixture.state.schemaMassSnapshot()
	if mass.Deferred != 2 {
		t.Fatalf("deferred count = %d, want 2", mass.Deferred)
	}
	if mass.HeldTokens <= 0 {
		t.Fatalf("held tokens = %d, want the withheld schema mass to be measured", mass.HeldTokens)
	}
	if mass.Tokens <= 0 {
		t.Fatalf("advertised tokens = %d, want a positive measurement", mass.Tokens)
	}
	if !strings.Contains(mass.String(), "deferred") {
		t.Fatalf("operator line omits the deferred tier: %q", mass.String())
	}
}
