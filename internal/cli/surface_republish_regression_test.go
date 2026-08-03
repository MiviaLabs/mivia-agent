package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// switchableDeferredRes is a fixture config whose provider is real enough for
// buildModelBinding to construct a completer, so these tests drive the actual
// /model path (PrepareBinding -> SwitchBinding) rather than a stand-in.
func switchableDeferredRes() *config.Resolved {
	return &config.Resolved{
		ProviderName: "deepseek",
		Model:        "deepseek-chat",
		Models:       []string{"deepseek-chat", "deepseek-reasoner"},
		APIKey:       "test-key",
		APIKeySet:    true,
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		BaseURL:      "http://127.0.0.1:1",
		Subagents:    config.DefaultSubagentConfig,
		SystemPrompt: "ROOT PROMPT",
	}
}

// newSwitchableFixture is the deferred fixture plus the binding factory the
// chat entry point installs, which is the single factory every /model surface
// (REPL slash, TUI dialog, catalog reload) goes through.
func newSwitchableFixture(t *testing.T, dir string, completer *scriptedCompleter, core, effective []string) *deferredFixture {
	t.Helper()
	res := switchableDeferredRes()
	fixture := newDeferredFixtureWith(t, dir, res, completer, core, effective)
	fixture.sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		return buildModelBinding(fixture.sess, res, dir, providerName, model, fixture.state)
	})
	return fixture
}

func switchToOtherModel(t *testing.T, fixture *deferredFixture) {
	t.Helper()
	if _, err := switchModelCommand(fixture.sess, fixture.res, "deepseek", "deepseek-reasoner"); err != nil {
		t.Fatalf("model switch: %v", err)
	}
}

// --- M1: /model must not collapse authority or orphan load_tools -----------

// TestModelSwitchKeepsLoadToolsInvocable: a /model rebuild that advertises
// load_tools without registering it leaves deferred loading dead for the rest
// of the session - the model cannot reach the admission path at all
// (INV-CE-05-A: advertised implies invocable).
func TestModelSwitchKeepsLoadToolsInvocable(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newSwitchableFixture(t, t.TempDir(), completer, []string{"read_file"}, []string{"read_file", "grep"})

	switchToOtherModel(t, fixture)

	names := registryToolNames(fixture.sess.Tools)
	if !slices.Contains(names, tools.LoadToolsToolName) {
		t.Fatalf("load_tools vanished from the advertised surface: %v", names)
	}
	if !fixture.sess.Dispatcher.Has(runtime.Tool, tools.LoadToolsToolName) {
		t.Fatalf("load_tools is advertised but not invocable after /model: %v", names)
	}
}

// TestModelSwitchKeepsRootAuthority: the deferred tier decides what the root
// model is shown, never what the session may delegate. A /model rebuild that
// takes the core tier for the authority set silently deregisters every skill
// and narrows every routed agent.
func TestModelSwitchKeepsRootAuthority(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "searchy", "---\nname: searchy\ndescription: Search things\ntools: [grep]\n---\nSearch.")
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newSwitchableFixture(t, dir, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if !fixture.sess.Dispatcher.Has(runtime.Subagent, "searchy") {
		t.Fatal("precondition: searchy is not registered before the switch")
	}

	switchToOtherModel(t, fixture)

	if !fixture.sess.Dispatcher.Has(runtime.Subagent, "searchy") {
		t.Fatal("a skill needing a deferred tool was deregistered by /model: authority collapsed to the core tier")
	}
}

// TestModelSwitchKeepsAdmittedToolsAdvertised: an admitted tool is part of the
// session's surface, not of one dispatcher, so a model switch must carry it.
func TestModelSwitchKeepsAdmittedToolsAdvertised(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newSwitchableFixture(t, t.TempDir(), completer, []string{"read_file"}, []string{"read_file", "grep"})
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("admission turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("precondition: admitted = %v", got)
	}

	switchToOtherModel(t, fixture)

	if _, ok := fixture.sess.Tools.Get("grep"); !ok {
		t.Fatalf("the admitted tool was dropped by /model: %v", registryToolNames(fixture.sess.Tools))
	}
	if !fixture.sess.Dispatcher.Has(runtime.Tool, "grep") {
		t.Fatal("the admitted tool is advertised but not invocable after /model")
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep] preserved across /model", got)
	}
}

// TestModelBindingPreparedBeforeAnAdmissionIsRefused keeps the surface fence:
// a binding built against an older agent surface generation must not publish.
func TestModelBindingPreparedBeforeAnAdmissionIsRefused(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newSwitchableFixture(t, t.TempDir(), completer, []string{"read_file"}, []string{"read_file", "grep"})
	binding, prepared, err := fixture.sess.PrepareBinding("deepseek", "deepseek-reasoner")
	if !prepared || err != nil {
		t.Fatalf("prepare binding: prepared=%v err=%v", prepared, err)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("admission turn: %v", err)
	}
	if err := fixture.sess.SwitchBinding(binding); err == nil {
		t.Fatal("a binding prepared before the admission was published")
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("the refused switch disturbed the live surface: admitted = %v", got)
	}
}

// --- the authority/advertised split, one delegation path at a time ---------

// nestedAdvertisedTools invokes a nested subagent handler on the live session
// dispatcher and returns the tool names that principal was advertised on its
// own provider request.
//
// The nested registry is not reachable from outside the handler, so the request
// the nested loop issues is the only honest witness of what it was scoped from.
// Asserting on registration alone cannot see a handler that registered against
// the core tier instead of the authority set.
func nestedAdvertisedTools(t *testing.T, fixture *deferredFixture, name string) []string {
	t.Helper()
	before, _ := fixture.completer.requests()
	result := fixture.sess.Dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "nested-" + name, Kind: runtime.Subagent, Name: name,
		Input: json.RawMessage(`"do the work"`), SessionID: "test",
	})
	if result.Err != nil {
		t.Fatalf("invoke %s: %v", name, result.Err)
	}
	after, _ := fixture.completer.requests()
	if len(after) <= len(before) {
		t.Fatal(name + " issued no provider request")
	}
	return after[len(before)]
}

// TestNestedMultiStepIsScopedFromTheAuthorityRegistry: multi_step is the
// generic delegation path, so scoping it from the advertised surface narrows
// every delegated task to whatever the root model happens to be shown.
func TestNestedMultiStepIsScopedFromTheAuthorityRegistry(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if _, ok := fixture.sess.Tools.Get("grep"); ok {
		t.Fatal("precondition: grep must be deferred off the advertised surface")
	}
	names := nestedAdvertisedTools(t, fixture, handlerMultiStep)
	if !slices.Contains(names, "grep") {
		t.Fatalf("nested multi_step tools = %v, want the root's deferred grep: a core tier narrowed delegation", names)
	}
}

// TestNestedMultiStepSeesTheDispatcherOwnedTools pins adoptSessionTools: the
// constructor registers read_output and friends onto the advertised surface
// only, and handlers hold the authority registry by pointer. Without the
// adoption a delegated principal loses read_output entirely - so a truncated
// nested result mints a reference the same principal cannot then read.
func TestNestedMultiStepSeesTheDispatcherOwnedTools(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	names := nestedAdvertisedTools(t, fixture, handlerMultiStep)
	if !slices.Contains(names, "read_output") {
		t.Fatalf("nested multi_step tools = %v, want read_output adopted into the authority registry", names)
	}
}

// TestSkillSubagentIsScopedFromTheAuthorityRegistry is the same property for
// the skill path, which registers its own handlers and so narrows separately.
func TestSkillSubagentIsScopedFromTheAuthorityRegistry(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "searchy", "---\nname: searchy\ndescription: Search things\n---\nSearch.")
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixtureIn(t, dir, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if !fixture.sess.Dispatcher.Has(runtime.Subagent, "searchy") {
		t.Fatal("precondition: searchy is not registered")
	}
	names := nestedAdvertisedTools(t, fixture, "searchy")
	if !slices.Contains(names, "grep") {
		t.Fatalf("skill subagent tools = %v, want the root's deferred grep", names)
	}
	if !slices.Contains(names, "read_output") {
		t.Fatalf("skill subagent tools = %v, want read_output adopted into the authority registry", names)
	}
}

// --- M5: the remainder spool is session-scoped, not dispatcher-scoped ------

func readOutputStatus(t *testing.T, fixture *deferredFixture, ctx context.Context, ref string) map[string]any {
	t.Helper()
	tool, ok := fixture.sess.Tools.Get("read_output")
	if !ok {
		t.Fatal("read_output is not registered on the live surface")
	}
	out, err := tool.Execute(ctx, json.RawMessage(`{"ref":`+quoteJSON(ref)+`}`))
	if err != nil {
		t.Fatalf("read_output: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("read_output payload %q: %v", out, err)
	}
	return payload
}

func quoteJSON(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// spoolRefFor mints a truncated-output reference the way a capped tool result
// does, against the spool the live surface holds.
func spoolRefFor(t *testing.T, fixture *deferredFixture, ctx context.Context, body string) string {
	t.Helper()
	spool := RemainderSpoolFromRegistry(fixture.sess.Tools)
	if spool == nil {
		t.Fatal("no remainder spool on the live surface")
	}
	ref := spool.Spool(ctx, fixture.sess.SessionID, []byte(body))
	if ref == "" {
		t.Fatal("spool minted no reference")
	}
	return ref
}

// TestTruncatedOutputSurvivesAToolAdmission: republishing the surface must not
// revoke the model's access to output this same session produced.
func TestTruncatedOutputSurvivesAToolAdmission(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: fixture.sess.SessionID})
	ref := spoolRefFor(t, fixture, ctx, "earlier truncated output")
	if status := readOutputStatus(t, fixture, ctx, ref)["status"]; status != "ok" {
		t.Fatalf("precondition: status = %v before the admission", status)
	}

	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("admission turn: %v", err)
	}

	payload := readOutputStatus(t, fixture, ctx, ref)
	if payload["status"] != "ok" {
		t.Fatalf("read_output after the admission = %v, want ok (the admission revoked the session's own grant)", payload["status"])
	}
	if payload["content"] != "earlier truncated output" {
		t.Fatalf("content = %v, want the spooled bytes", payload["content"])
	}
}

// TestTruncatedOutputSurvivesAnAgentSwitch is the same invariant for /agent,
// which republishes the surface for a different reason.
func TestTruncatedOutputSurvivesAnAgentSwitch(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: fixture.sess.SessionID})
	ref := spoolRefFor(t, fixture, ctx, "output from before the switch")

	writer := agents.ResolvedAgent{Name: "writer", SystemPrompt: "W", EffectiveTools: []string{"read_file", "grep"}}
	if err := fixture.state.Registry.Publish(writer); err != nil {
		t.Fatal(err)
	}
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "writer", false); err != nil {
		t.Fatalf("agent switch: %v", err)
	}

	if status := readOutputStatus(t, fixture, ctx, ref)["status"]; status != "ok" {
		t.Fatalf("read_output after /agent = %v, want ok", status)
	}
}

// --- M7: cleanup closes the dispatcher that is live at exit ----------------

// TestSessionCleanupClosesTheLiveDispatcher: the attach-time cleanup must
// close whatever dispatcher is live when it runs, or the last publication's
// OnClose hooks (coordinator and ledger teardown) never run.
func TestSessionCleanupClosesTheLiveDispatcher(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	attached := fixture.sess.Dispatcher
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("admission turn: %v", err)
	}
	live := fixture.sess.Dispatcher
	if live == attached {
		t.Fatal("precondition: the admission did not replace the dispatcher")
	}

	fixture.cleanup()

	if _, leaked := coordinators.Load(live); leaked {
		t.Fatal("cleanup closed a stale dispatcher; the live one's OnClose hooks never ran")
	}
}

// TestModelSwitchWithoutAnAgentSurfaceUsesThePlainClone covers the fallback:
// a session with no captured agent surface defers nothing, so the advertised
// registry IS the authority registry and a generation clone is correct.
func TestModelSwitchWithoutAnAgentSurfaceUsesThePlainClone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	res := switchableDeferredRes()
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.Tools = tierRegistry("read_file")
	sess.UseTools = true
	binding, err := buildModelBinding(sess, res, dir, res.ProviderName, res.Model, nil)
	if err != nil {
		t.Fatalf("buildModelBinding with no agent state: %v", err)
	}
	if binding.Dispatcher == nil {
		t.Fatal("the fallback produced no dispatcher")
	}
	t.Cleanup(binding.Dispatcher.Close)
	if binding.Registry != nil {
		t.Fatal("the fallback must not publish a registry; there is no surface to replace")
	}
	if !binding.Dispatcher.Has(runtime.Tool, "read_file") {
		t.Fatal("the fallback dispatcher lost the session's tools")
	}
}

// TestModelSwitchReportsASurfaceBuildFailure: a model switch that cannot
// rebuild the surface must refuse rather than publish a narrowed one.
//
// The failure is induced the way it happens in production - a skill in the
// binding's registry whose name collides with a routed agent, so the dispatcher
// refuses to register a duplicate handler. It is injected into the binding's
// frozen registry rather than written to disk, because a /model rebuild
// deliberately does not re-read skills from disk.
func TestModelSwitchReportsASurfaceBuildFailure(t *testing.T) {
	dir := t.TempDir()
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newSwitchableFixture(t, dir, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.state.mu.Lock()
	regErr := fixture.state.SkillRegFull.Register(skills.Definition{
		Name: "reader", Description: "collides with an agent", Instructions: "body",
	})
	fixture.state.mu.Unlock()
	if regErr != nil {
		t.Fatal(regErr)
	}
	_, err := buildModelBinding(fixture.sess, fixture.res, dir, "deepseek", "deepseek-reasoner", fixture.state)
	if err == nil {
		t.Fatal("a failed surface rebuild was reported as a usable binding")
	}
	if !strings.Contains(err.Error(), "duplicate handler") {
		t.Fatalf("error = %v, want the dispatcher construction failure", err)
	}
}

// TestModelSwitchWithoutAnAgentSurfaceReportsAFailure covers the same refusal
// on the no-agent-surface fallback.
func TestModelSwitchWithoutAnAgentSurfaceReportsAFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	res := switchableDeferredRes()
	sess := chat.NewSession(res, stubAgentCompleter{})
	// A plain tool already holding a session-tool name makes registration
	// refuse, which is the only way this path can fail.
	base := tierRegistry("read_file")
	base.Register(namedTool{name: "delegate"})
	sess.Tools = base
	sess.UseTools = true
	if _, err := buildModelBinding(sess, res, dir, res.ProviderName, res.Model, nil); err == nil {
		t.Fatal("the fallback reported a usable binding despite a registration conflict")
	}
}

// --- R1: /model must not diverge from what the next admission rebuilds -----

// TestModelSwitchKeepsTheBindingSkillRegistry: the skill registry is frozen for
// the life of the agent binding (re-discovery is /agent's job), so a /model
// switch must publish the SAME registry a later tool admission rebuilds from.
// A /model that re-reads disk while buildWidenedWith reuses the attach-time
// registry makes the catalogue flip back and forth: an operator-deleted skill
// disappears at /model and is silently resurrected by the next admission.
func TestModelSwitchKeepsTheBindingSkillRegistry(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "searchy", "---\nname: searchy\ndescription: Search things\ntools: [grep]\n---\nSearch.")
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newSwitchableFixture(t, dir, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if !fixture.sess.Dispatcher.Has(runtime.Subagent, "searchy") {
		t.Fatal("precondition: searchy is not registered at attach")
	}
	// An operator removes the skill mid-session. The binding is already frozen
	// against the attach-time registry, so nothing may re-read this directory
	// until /agent starts a new binding.
	if err := os.RemoveAll(filepath.Join(dir, ".mivia", "skills", "searchy")); err != nil {
		t.Fatal(err)
	}

	switchToOtherModel(t, fixture)
	afterSwitch := fixture.sess.Dispatcher.Has(runtime.Subagent, "searchy")

	// The next tool admission rebuilds the surface through buildWidenedWith,
	// which is the other side of the divergence.
	candidate, err := buildWidenedWith(fixture.sess, fixture.res, fixture.state, []string{"grep"})
	if err != nil {
		t.Fatalf("widen after /model: %v", err)
	}
	t.Cleanup(candidate.dispatcher.Close)
	afterAdmission := candidate.dispatcher.Has(runtime.Subagent, "searchy")

	if afterSwitch != afterAdmission {
		t.Fatalf("skill catalogue diverged: registered after /model = %v, after the next admission = %v",
			afterSwitch, afterAdmission)
	}
	if !afterSwitch {
		t.Fatal("/model re-read skills from disk; the binding's skill registry is frozen for its life")
	}
}

// --- R2: the reused spool must outlive the store it reads through ----------

// TestTruncatedOutputSurvivesAnAdmissionWithASessionOwnedLedger: with no
// context store wired, each dispatcher used to open and own its own ledger
// store. Publication closes the replaced dispatcher, so the first admission
// closed the store the carried-over spool reads through and every earlier ref
// became a hard read_output error.
func TestTruncatedOutputSurvivesAnAdmissionWithASessionOwnedLedger(t *testing.T) {
	dir := t.TempDir()
	res := &config.Resolved{Model: "m", ProviderName: "p", Subagents: config.DefaultSubagentConfig, SystemPrompt: "ROOT PROMPT"}
	res.Subagents.StoreBackend = "sqlite"
	res.Subagents.StorePath = filepath.Join(dir, "ledger.db")
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	fixture := newDeferredFixtureWith(t, dir, res, completer, []string{"read_file"}, []string{"read_file", "grep"})
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: fixture.sess.SessionID})
	ref := spoolRefFor(t, fixture, ctx, "durable truncated output")
	if status := readOutputStatus(t, fixture, ctx, ref)["status"]; status != "ok" {
		t.Fatalf("precondition: status = %v before the admission", status)
	}

	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("admission turn: %v", err)
	}

	payload := readOutputStatus(t, fixture, ctx, ref)
	if payload["status"] != "ok" {
		t.Fatalf("read_output after the admission = %v, want ok (the admission closed the spool's store)", payload["status"])
	}
	if payload["content"] != "durable truncated output" {
		t.Fatalf("content = %v, want the spooled bytes", payload["content"])
	}
	if newRef := spoolRefFor(t, fixture, ctx, "minted after the admission"); newRef == "" {
		t.Fatal("the live spool can no longer mint references")
	}
}

// TestChatBindingFactoryRebuildsThroughTheAgentSurface pins the factory the
// chat entry point installs: every /model surface goes through it, so it is
// where a narrowed-authority rebuild would reappear.
func TestChatBindingFactoryRebuildsThroughTheAgentSurface(t *testing.T) {
	dir := t.TempDir()
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newSwitchableFixture(t, dir, completer, []string{"read_file"}, []string{"read_file", "grep"})
	factory := chatBindingFactory(fixture.sess, fixture.res, dir, fixture.state)
	binding, err := factory("deepseek", "deepseek-reasoner")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if binding.Dispatcher == nil || binding.Registry == nil {
		t.Fatal("the factory produced no surface")
	}
	t.Cleanup(binding.Dispatcher.Close)
	if !binding.Dispatcher.Has(runtime.Tool, tools.LoadToolsToolName) {
		t.Fatal("the rebuilt dispatcher cannot invoke load_tools")
	}
}

// deferringAgent is a published agent that defers everything outside core.
func deferringAgent(t *testing.T, fixture *deferredFixture, name string, core []string, effective ...string) {
	t.Helper()
	if err := fixture.state.Registry.Publish(agents.ResolvedAgent{
		Name: name, SystemPrompt: strings.ToUpper(name),
		EffectiveTools: effective, CoreTools: corePtr(core...),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAgentSwitchArmsTheWidenerItself: the switch must install a widener for
// the binding it is publishing.
//
// The attach-time widener is disarmed first, and that is the whole point of
// the test. It closes over the live *agentSessionState rather than over one
// binding, so it keeps publishing correctly for every later binding - which
// makes "the switch re-installed it" and "startup's is still lying around"
// indistinguishable unless the leftover is removed.
func TestAgentSwitchArmsTheWidenerItself(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	fixture.sess.SetSurfaceWidener(nil)
	deferringAgent(t, fixture, "narrow", []string{"read_file"}, "read_file", "grep")
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "narrow", false); err != nil {
		t.Fatalf("switch: %v", err)
	}

	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "go", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep]: the switch published a deferring binding with no widener", got)
	}
	if _, ok := fixture.sess.Tools.Get("grep"); !ok {
		t.Fatalf("the admitted tool never reached the surface: %v", registryToolNames(fixture.sess.Tools))
	}
}

// TestAgentSwitchToAnInertAgentDisarmsAStaleWidener: an agent that defers
// nothing has no admission path at all. A widener left over from the previous
// binding is one, and it publishes against the live state - so a stage that
// should be void instead widens a surface that was never tiered.
//
// The previous binding is deliberately the deferring one, and the staged name
// is a tool the inert agent is authorized for, so nothing but the disarming
// stops the publication.
func TestAgentSwitchToAnInertAgentDisarmsAStaleWidener(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.sess.SessionDir = t.TempDir()
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage under the deferring binding: %v", err)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "load", io.Discard); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("precondition: admitted = %v", got)
	}
	if err := fixture.sess.Save("snap"); err != nil {
		t.Fatalf("save: %v", err)
	}

	wide := agents.ResolvedAgent{Name: "wide", SystemPrompt: "W", EffectiveTools: []string{"read_file", "grep"}}
	if err := fixture.state.Registry.Publish(wide); err != nil {
		t.Fatal(err)
	}
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "wide", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	_ = fixture.sess.TakeAdmissionNotes()

	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage under the inert binding: %v", err)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "go", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v under an agent that defers nothing: a stale widener survived the switch", got)
	}
	if _, ok := fixture.sess.PendingAdmission(); ok {
		t.Fatal("a stage under an inert binding stayed pending instead of being voided")
	}

	// Fail-closed on the resume side too: a snapshot taken under the deferring
	// binding must not restore an admitted set into a binding that has no
	// admission concept.
	//
	// This assertion is deliberately not a discriminator, and cannot be made
	// one. Clearing the admission identity on this branch has no observable
	// effect while the widener is nil: replayAdmission refuses without a
	// widener before the identity is ever compared, and admissionRecord is only
	// persisted when the admitted set is non-empty - which an inert binding's
	// ResetAdmissions guarantees it is not. The identity clear is therefore an
	// equivalent mutant behind the disarming above, and is kept because the two
	// statements state one fact together: this binding admits nothing.
	if err := fixture.sess.Load("snap"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v after resuming under an inert binding", got)
	}
}

// TestAgentSwitchRebindsThePersistedAdmissionIdentity: the persisted admitted
// set is keyed by the agent and tier digest it was admitted under, and the
// switch is what rebinds that key. Leaving the previous binding's identity in
// place makes a snapshot resume under an agent it was never taken under -
// exactly the fail-closed check the record exists for.
func TestAgentSwitchRebindsThePersistedAdmissionIdentity(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	fixture.sess.SessionDir = t.TempDir()
	deferringAgent(t, fixture, "narrow", []string{"read_file"}, "read_file", "grep")
	deferringAgent(t, fixture, "other", []string{"read_file"}, "read_file", "grep", "glob")

	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "narrow", false); err != nil {
		t.Fatalf("switch to narrow: %v", err)
	}
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "go", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("precondition: admitted = %v under narrow", got)
	}
	if err := fixture.sess.Save("snap"); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A different agent, so the snapshot's admitted set belongs to nobody here.
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "other", false); err != nil {
		t.Fatalf("switch to other: %v", err)
	}
	_ = fixture.sess.TakeAdmissionNotes()
	if err := fixture.sess.Load("snap"); err != nil {
		t.Fatalf("load: %v", err)
	}

	if got := fixture.sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v; a snapshot taken under narrow resumed under other", got)
	}
	if _, ok := fixture.sess.Tools.Get("grep"); ok {
		t.Fatalf("the other agent's surface carries narrow's admitted tool: %v", registryToolNames(fixture.sess.Tools))
	}
	notes := fixture.sess.TakeAdmissionNotes()
	if len(notes) == 0 || !strings.Contains(notes[0], "grep") {
		t.Fatalf("notes = %v, want the dropped set named", notes)
	}
}
