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
	if err := switchModelCommand(fixture.sess, fixture.res, "deepseek", "deepseek-reasoner"); err != nil {
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
