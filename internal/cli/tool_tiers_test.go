package cli

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func tierRegistry(names ...string) *tools.Registry {
	reg := tools.NewRegistry()
	for _, name := range names {
		reg.Register(namedTool{name: name})
	}
	return reg
}

func corePtr(names ...string) *[]string {
	out := slices.Clone(names)
	return &out
}

func TestPlanToolTiersWithoutACoreListIsInert(t *testing.T) {
	base := tierRegistry("read_file", "grep")
	plan := planToolTiers(base, &agents.ResolvedAgent{Name: "a", EffectiveTools: []string{"read_file", "grep"}}, nil)
	if plan.Deferred() {
		t.Fatalf("plan deferred %v with no core list", plan.Candidates)
	}
	if !slices.Equal(plan.Tiers.Core, []string{"read_file", "grep"}) {
		t.Fatalf("core = %v, want every authorized tool", plan.Tiers.Core)
	}
	if plan.Digest != "" {
		t.Fatalf("an inert plan must not mint a digest, got %q", plan.Digest)
	}
}

func TestPlanToolTiersFallsBackToTheGlobalCoreList(t *testing.T) {
	base := tierRegistry("read_file", "grep")
	res := &config.Resolved{Tools: config.ToolsConfig{Core: corePtr("read_file")}}
	plan := planToolTiers(base, &agents.ResolvedAgent{Name: "a", EffectiveTools: []string{"read_file", "grep"}}, res)
	if !slices.Equal(plan.Tiers.Deferred, []string{"grep"}) {
		t.Fatalf("deferred = %v, want [grep] from the global core list", plan.Tiers.Deferred)
	}
	if plan.Digest == "" {
		t.Fatal("a deferring plan must mint a digest for resume checks")
	}
}

func TestPlanToolTiersPrefersThePerAgentOverride(t *testing.T) {
	base := tierRegistry("read_file", "grep")
	res := &config.Resolved{Tools: config.ToolsConfig{Core: corePtr("read_file")}}
	selected := &agents.ResolvedAgent{Name: "a", EffectiveTools: []string{"read_file", "grep"}, CoreTools: corePtr("read_file", "grep")}
	plan := planToolTiers(base, selected, res)
	if plan.Deferred() {
		t.Fatalf("per-agent tools_core did not override the global list: %v", plan.Candidates)
	}
}

func TestPlanToolTiersWithNoSelectedAgentUsesTheWholeRegistry(t *testing.T) {
	base := tierRegistry("read_file", "grep", "glob")
	res := &config.Resolved{Tools: config.ToolsConfig{Core: corePtr("read_file")}}
	plan := planToolTiers(base, nil, res)
	if !slices.Equal(plan.Tiers.Deferred, []string{"grep", "glob"}) {
		t.Fatalf("deferred = %v, want the whole registry minus core", plan.Tiers.Deferred)
	}
	if plan.Digest == "" {
		t.Fatal("digest missing for a compiled-default binding")
	}
}

func TestPlanToolTiersIgnoresNamesAbsentFromTheRegistry(t *testing.T) {
	base := tierRegistry("read_file")
	selected := &agents.ResolvedAgent{Name: "a", EffectiveTools: []string{"read_file", "not_registered"}, CoreTools: corePtr("read_file")}
	plan := planToolTiers(base, selected, nil)
	for _, candidate := range plan.Candidates {
		if candidate.Name == "not_registered" {
			t.Fatal("a tool absent from the live registry became a deferred candidate")
		}
	}
}

func TestPlanToolTiersOnANilRegistry(t *testing.T) {
	plan := planToolTiers(nil, nil, &config.Resolved{Tools: config.ToolsConfig{Core: corePtr("read_file")}})
	if plan.Deferred() || len(plan.Tiers.Core) != 0 {
		t.Fatalf("nil registry produced %+v", plan)
	}
	if names := authorizedNamesInRegistryOrder(nil, nil); names != nil {
		t.Fatalf("authorized names on a nil registry = %v", names)
	}
}

func TestTieredRootRegistryFallsBackToPlainRootScope(t *testing.T) {
	base := tierRegistry("read_file", "write_file")
	selected := &agents.ResolvedAgent{Name: "a", EffectiveTools: []string{"read_file"}}
	got := tieredRootRegistry(base, selected, nil, toolTierPlan{}, nil)
	if _, ok := got.Get("write_file"); ok {
		t.Fatal("an inert plan must still apply ordinary root scope")
	}
	if _, ok := got.Get("read_file"); !ok {
		t.Fatal("root scope dropped an authorized tool")
	}
	if tieredRootRegistry(nil, selected, nil, toolTierPlan{}, nil) != nil {
		t.Fatal("a nil base registry must stay nil")
	}
}

func TestPromptWithDeferredIndex(t *testing.T) {
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep", Description: "Search"}}}
	if got := promptWithDeferredIndex("", plan); !strings.HasPrefix(got, "Additional tools") {
		t.Fatalf("empty prompt = %q, want the index alone", got)
	}
	got := promptWithDeferredIndex("BASE", plan)
	if !strings.HasPrefix(got, "BASE\n\n") || !strings.Contains(got, "- grep") {
		t.Fatalf("prompt = %q", got)
	}
	if got := promptWithDeferredIndex("BASE", toolTierPlan{}); got != "BASE" {
		t.Fatalf("inert plan changed the prompt: %q", got)
	}
}

func TestAgentNameOf(t *testing.T) {
	if agentNameOf(nil) != "" {
		t.Fatal("a nil agent must key as the empty name")
	}
	if agentNameOf(&agents.ResolvedAgent{Name: "reader"}) != "reader" {
		t.Fatal("agent name not returned")
	}
}

// --- schema-mass telemetry ---------------------------------------------

func TestMeasureSchemaMassCountsBothTiers(t *testing.T) {
	base := tierRegistry("read_file", "grep", "glob")
	advertised := tierRegistry("read_file", "grep", "glob").OpenAITools()
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep"}, {Name: "glob"}}}
	mass := measureSchemaMass(advertised, base, plan, nil, "reader", "attach")
	if mass.Advertised != 3 || mass.Locked != 2 {
		t.Fatalf("mass = %+v, want 3 advertised (core plus both deferred candidates) and 2 locked", mass)
	}
	if mass.Tokens <= 0 || mass.LockedTokens <= 0 {
		t.Fatalf("mass = %+v, want positive token measurements", mass)
	}
	if !strings.Contains(mass.String(), "3 tools advertised") || !strings.Contains(mass.String(), "2 locked") {
		t.Fatalf("line = %q", mass.String())
	}
}

func TestMeasureSchemaMassOnAnInertSurface(t *testing.T) {
	mass := measureSchemaMass(tierRegistry("read_file").OpenAITools(), nil, toolTierPlan{}, nil, "", "attach")
	if mass.Locked != 0 || mass.LockedTokens != 0 {
		t.Fatalf("mass = %+v, want nothing locked", mass)
	}
	if strings.Contains(mass.String(), "locked") {
		t.Fatalf("inert line mentions locking: %q", mass.String())
	}
	empty := measureSchemaMass(nil, nil, toolTierPlan{}, nil, "", "attach")
	if empty.Advertised != 0 || empty.Tokens != 0 {
		t.Fatalf("nil registry measured %+v", empty)
	}
}

func TestPublishSchemaMassEmitsAConfigChangeEvent(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	bus := events.New()
	t.Cleanup(bus.Close)
	received := make(chan events.Event, 1)
	bus.Subscribe(events.KindConfigChange, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		select {
		case received <- ev:
		default:
		}
	}))
	sess.EventBus = bus
	publishSchemaMass(sess, schemaMass{Advertised: 3, Tokens: 120, Locked: 2, LockedTokens: 80, AgentName: "reader", Publication: "attach"})
	bus.Flush()
	select {
	case ev := <-received:
		if ev.Name != "tool_schema_mass" {
			t.Fatalf("event name = %q", ev.Name)
		}
		if ev.Metadata["tools_locked"] != "2" || ev.Metadata["locked_tokens"] != "80" {
			t.Fatalf("metadata = %v", ev.Metadata)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no config-change event published")
	}
	// A session without a bus must stay silent rather than panic.
	sess.EventBus = nil
	publishSchemaMass(sess, schemaMass{})
	publishSchemaMass(nil, schemaMass{})
}

func TestRecordSchemaMassWithoutAgentState(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	sess.Tools = tierRegistry("read_file")
	recordSchemaMass(sess, nil, toolTierPlan{}, nil, "", "attach")
	if (*AgentSessionState)(nil).schemaMassSnapshot() != (schemaMass{}) {
		t.Fatal("a nil state must report the zero measurement")
	}
}

// --- the load_tools tool surface ---------------------------------------

func TestLoadToolsToolSurfaceIsGenericAndPrivileged(t *testing.T) {
	tool := &loadToolsTool{candidates: []tools.TierCandidate{{Name: "grep", Description: "Search files"}}}
	if tool.Name() != tools.LoadToolsToolName {
		t.Fatalf("name = %q", tool.Name())
	}
	if !strings.Contains(tool.Description(), "NEXT turn") {
		t.Fatalf("description must state the next-turn availability: %q", tool.Description())
	}
	params := tool.Parameters()
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["names"]; !ok {
		t.Fatalf("parameters missing names: %v", params)
	}
	if _, ok := props["query"]; !ok {
		t.Fatalf("parameters missing query: %v", params)
	}
	if params["additionalProperties"] != false {
		t.Fatal("parameters must reject unknown fields")
	}
	tool.Privileged()
	if capability := tool.Capability(nil); capability.Class != tools.ExecutionWrite {
		t.Fatalf("capability class = %v, want write (load_tools mutates session state)", capability.Class)
	}
}

func TestLoadToolsRequiresSomethingToLoad(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	tool := &loadToolsTool{session: sess, candidates: []tools.TierCandidate{{Name: "grep", Description: "Search"}}}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("an empty request was accepted")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"nothing matches this"}`)); err == nil {
		t.Fatal("a non-matching query was accepted")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Fatal("malformed arguments were accepted")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["  "]}`)); err == nil {
		t.Fatal("a blank name was accepted as a request")
	}
}

func TestLoadToolsRendersStagedAndAlreadyLoaded(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	tool := &loadToolsTool{session: sess, candidates: []tools.TierCandidate{
		{Name: "grep", Description: "Search file contents"},
		{Name: "glob", Description: "Match paths"},
	}}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["glob","grep"]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Staged in deferred-set order, not request order, so the resulting
	// registry is deterministic.
	if !strings.Contains(out, "loaded: grep, glob") {
		t.Fatalf("result = %q, want deferred-set ordering", out)
	}
	if !strings.Contains(out, "- grep: Search file contents") {
		t.Fatalf("result = %q, want the one-liner for each staged tool", out)
	}
	again, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`))
	if err != nil {
		t.Fatalf("idempotent execute: %v", err)
	}
	// grep was STAGED, never published: it is not callable until the next turn
	// boundary, so it must not be reported as already loaded (R3).
	if !strings.Contains(again, "already staged: grep") || strings.Contains(again, "callable now") {
		t.Fatalf("idempotent result = %q, want the staged-not-published wording", again)
	}
}

func TestLoadToolsErrorListsBoundedCandidates(t *testing.T) {
	candidates := make([]tools.TierCandidate, 0, maxLoadToolsErrorCandidates+5)
	for i := 0; i < maxLoadToolsErrorCandidates+5; i++ {
		candidates = append(candidates, tools.TierCandidate{Name: string(rune('a'+i%26)) + "_tool"})
	}
	tool := &loadToolsTool{candidates: candidates}
	names := tool.candidateNames()
	if len(names) != maxLoadToolsErrorCandidates+1 || names[len(names)-1] != "..." {
		t.Fatalf("candidate list = %d entries ending %q, want a bounded, elided list", len(names), names[len(names)-1])
	}
	if tool.describe("not-a-candidate") != "" {
		t.Fatal("describe invented a description")
	}
}

func TestBuildWidenedWithRefusesAnIncompleteBinding(t *testing.T) {
	state := &AgentSessionState{WorkspaceRoot: t.TempDir()}
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	if _, err := buildWidenedWith(sess, nil, state, []string{"grep"}); err == nil ||
		!strings.Contains(err.Error(), "skill registry") {
		t.Fatalf("error = %v, want a refusal naming the missing skill registry", err)
	}
	state.SkillRegFull = skills.NewRegistry()
	state.ToolBase = tierRegistry("read_file", "grep")
	bare := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, nil)
	if _, err := buildWidenedWith(bare, nil, state, []string{"grep"}); err == nil ||
		!strings.Contains(err.Error(), "nil completer") {
		t.Fatalf("error = %v, want a refusal naming the missing completer", err)
	}
}

// TestSurfaceWidenerClosesARefusedCandidate: a candidate that loses the
// precondition race must not leak the dispatcher it built.
func TestSurfaceWidenerClosesARefusedCandidate(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	widener := newSurfaceWidener(fixture.sess, fixture.res, fixture.state)
	before := fixture.sess.Tools
	// RequireTurnID names a turn that is not current, so the publish is refused.
	published, err := widener([]string{"grep"}, chat.AgentSurfacePublication{
		Prompt: "P", RequireTurnID: 9999,
	})
	if err != nil {
		t.Fatalf("widen: %v", err)
	}
	if published {
		t.Fatal("published against a stale turn id")
	}
	if fixture.sess.Tools != before {
		t.Fatal("a refused publication replaced the live registry")
	}
	// The session's own dispatcher must still work after the refusal.
	if fixture.sess.Dispatcher == nil {
		t.Fatal("refusal closed the live dispatcher")
	}
	if _, err := fixture.sess.SendUser(context.Background(), "still working", io.Discard); err != nil {
		t.Fatalf("turn after a refused widen: %v", err)
	}
}

func TestSurfaceWidenerReportsABuildFailure(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.state.mu.Lock()
	fixture.state.SkillRegFull = nil
	fixture.state.mu.Unlock()
	widener := newSurfaceWidener(fixture.sess, fixture.res, fixture.state)
	if _, err := widener([]string{"grep"}, chat.AgentSurfacePublication{}); err == nil {
		t.Fatal("a failed rebuild was reported as success")
	}
}

func TestRegisterLoadToolsToolRequiresASession(t *testing.T) {
	err := registerLoadToolsTool(nil, SessionDispatcherOpts{
		Registry:      tierRegistry("read_file"),
		DeferredTools: []tools.TierCandidate{{Name: "grep"}},
	})
	if err == nil || !strings.Contains(err.Error(), "without a session") {
		t.Fatalf("error = %v, want a refusal naming the missing session", err)
	}
	if err := registerLoadToolsTool(nil, SessionDispatcherOpts{}); err != nil {
		t.Fatalf("an inert binding must register nothing: %v", err)
	}
}

func TestLoadToolsSurfacesTheCapError(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	tool := &loadToolsTool{session: sess, candidates: []tools.TierCandidate{{Name: "grep"}}}
	// A wrong name each time: re-requesting the SAME loaded tool is a no-op,
	// which is refunded and bounded separately.
	for i := 0; i < tools.MaxAdmissionAttempts; i++ {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["no_such_tool"]}`)); err == nil {
			t.Fatalf("attempt %d: an unknown name was accepted", i)
		}
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`))
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("error = %v, want the attempt bound surfaced to the model", err)
	}
}

func TestLoadToolsRendersAMixedResult(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	tool := &loadToolsTool{session: sess, candidates: []tools.TierCandidate{
		{Name: "grep", Description: "Search"}, {Name: "glob", Description: "Match"},
	}}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`)); err != nil {
		t.Fatalf("first: %v", err)
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep","glob"]}`))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// Both names are staged-not-published, so both belong on the next-turn side.
	if !strings.Contains(out, "loaded: glob") || !strings.Contains(out, "already staged: grep") {
		t.Fatalf("mixed result = %q, want both sections", out)
	}
	if strings.Contains(out, "callable now") {
		t.Fatalf("mixed result = %q, want nothing claimed callable before publication", out)
	}
}

// TestAgentSwitchInstallsTheWidenerForTheNewBinding covers the /agent path that
// arms deferred loading for the agent being switched to.
func TestAgentSwitchInstallsTheWidenerForTheNewBinding(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "glob"})
	deferring := agents.ResolvedAgent{
		Name: "narrow", SystemPrompt: "N",
		EffectiveTools: []string{"read_file", "grep"},
		CoreTools:      corePtr("read_file"),
	}
	if err := fixture.state.Registry.Publish(deferring); err != nil {
		t.Fatal(err)
	}
	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, "narrow", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if _, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName); !ok {
		t.Fatal("the new binding does not advertise load_tools")
	}
	prompt, _ := fixture.sess.AgentSettings()
	if !strings.Contains(prompt, "- grep") {
		t.Fatalf("the new binding's prompt lacks its own index:\n%s", prompt)
	}
	// The widener must be armed for the NEW binding, not the old one.
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "go", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := fixture.sess.AdmittedTools(); len(got) != 1 || got[0] != "grep" {
		t.Fatalf("admitted = %v, want [grep] via the new binding's widener", got)
	}
}

// TestAgentSwitchToAnInertAgentDisarmsTheWidener: switching to an agent that
// defers nothing must remove the discovery surface, not leave a stale one.
func TestAgentSwitchToAnInertAgentDisarmsTheWidener(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	wide := agents.ResolvedAgent{Name: "wide", SystemPrompt: "W", EffectiveTools: []string{"read_file", "grep"}}
	if err := fixture.state.Registry.Publish(wide); err != nil {
		t.Fatal(err)
	}
	if err := ApplySessionAgent(fixture.sess, fixture.res, fixture.state, "wide", false); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if _, ok := fixture.sess.Tools.Get(tools.LoadToolsToolName); ok {
		t.Fatal("an inert agent still advertises load_tools")
	}
	if _, ok := fixture.sess.Tools.Get("grep"); !ok {
		t.Fatal("an inert agent must advertise its whole tool set")
	}
	prompt, _ := fixture.sess.AgentSettings()
	if strings.Contains(prompt, "not currently loaded") {
		t.Fatalf("an inert binding kept a deferred index:\n%s", prompt)
	}
}

func TestRegisterLoadToolsToolPropagatesARegistrationFailure(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	reg := tierRegistry("read_file")
	// A name already taken makes registerSessionTool refuse, and that refusal
	// must reach the dispatcher constructor rather than being swallowed.
	reg.Register(privilegedNamed{namedTool{name: tools.LoadToolsToolName}})
	err := registerLoadToolsTool(nil, SessionDispatcherOpts{
		Registry: reg, Session: sess,
		DeferredTools: []tools.TierCandidate{{Name: "grep"}},
	})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("error = %v, want the registration conflict", err)
	}
	// The same refusal must abort dispatcher construction rather than yield a
	// dispatcher whose tool surface disagrees with what was asked for.
	_, err = NewSessionDispatcher(SessionDispatcherOpts{
		Registry: reg, Completer: stubAgentCompleter{}, Model: "m",
		Config:  config.DefaultSubagentConfig,
		Session: sess, DeferredTools: []tools.TierCandidate{{Name: "grep"}},
	})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("dispatcher error = %v, want the registration conflict", err)
	}
}

// --- the disabled-tool diagnostic is an entry-point event ------------------

// disabledToolWarning is the substring scopedRootRegistry's diagnostic used to
// write on every surface build. The fixtures name workflow_run (not extract) as
// the disabled tool: extract registers only when the fixture supplies a Tavily
// key, so it is conditional, while workflow_run is known but never registered
// in a workspace without .mivia/workflows/ (which these fixtures are).
const disabledToolWarning = "disabled tools omitted from registry"

// TestDisabledToolWarningIsNotEmittedDuringATurn: the TUI owns the terminal
// while a turn runs, so a raw stderr write from a surface rebuild corrupts the
// rendered frame. A tool admission republishes the surface mid-turn, which
// makes this diagnostic model-triggerable.
func TestDisabledToolWarningIsNotEmittedDuringATurn(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{
		loadToolsCall("c1", `{"names":["grep"]}`),
		{Content: "done"},
	}}
	stderr := captureStderr(t)
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep", "workflow_run"})
	attached := stderr()
	if got := strings.Count(attached, disabledToolWarning); got != 1 {
		t.Fatalf("attach emitted the diagnostic %d times, want exactly 1:\n%s", got, attached)
	}

	duringTurn := captureStderr(t)
	_, err := fixture.sess.SendUser(context.Background(), "load", io.Discard)
	turnOutput := duringTurn()
	if err != nil {
		t.Fatalf("admission turn: %v", err)
	}
	if strings.Contains(turnOutput, disabledToolWarning) {
		t.Fatalf("a tool admission wrote to stderr mid-turn:\n%s", turnOutput)
	}
}

// TestDisabledToolWarningIsEmittedOnceOnAnInertAttach: an inert binding scopes
// the registry twice (authority, then the tiered fallback). The operator is
// told once about one agent, not once per internal scope call.
func TestDisabledToolWarningIsEmittedOnceOnAnInertAttach(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	stderr := captureStderr(t)
	newDeferredFixture(t, completer, nil, []string{"read_file", "workflow_run"})
	attached := stderr()
	if got := strings.Count(attached, disabledToolWarning); got != 1 {
		t.Fatalf("inert attach emitted the diagnostic %d times, want exactly 1:\n%s", got, attached)
	}
}

func TestBuildWidenedWithDefaultsTheWorkspaceRoot(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	fixture.state.mu.Lock()
	fixture.state.WorkspaceRoot = ""
	fixture.state.mu.Unlock()
	candidate, err := buildWidenedWith(fixture.sess, fixture.res, fixture.state, []string{"grep"})
	if err != nil {
		t.Fatalf("widen with an unset workspace root: %v", err)
	}
	t.Cleanup(candidate.dispatcher.Close)
	if _, ok := candidate.registry.Get("grep"); !ok {
		t.Fatal("the widened registry does not advertise the admitted tool")
	}
}

func TestDisabledForAgentWithoutASelectedAgent(t *testing.T) {
	// The compiled default authorizes everything registered, so nothing can be
	// "disabled" relative to it and there is no diagnostic to emit.
	if got := disabledForAgent(nil, tierRegistry("read_file")); got != nil {
		t.Fatalf("disabled = %v, want none for the compiled default", got)
	}
}

func TestSessionSurfaceCleanupWithoutAgentState(t *testing.T) {
	// A tools-off or hand-built caller owns no ledger store; cleanup must still
	// close the live dispatcher rather than skip it or panic.
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	dispatcher, err := runtime.NewToolDispatcher(tierRegistry("read_file"), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	dispatcher.OnClose(func() { close(closed) })
	sess.SetDispatcher(dispatcher)
	sessionSurfaceCleanup(sess, nil)()
	select {
	case <-closed:
	default:
		t.Fatal("cleanup did not close the live dispatcher")
	}
	if got := ledgerRepoOf(nil); got != nil {
		t.Fatalf("ledgerRepoOf(nil) = %v, want no repo", got)
	}
}

// TestAdoptSessionLedgerRepoSkipsASharedStore: when the context store is wired
// the ledger adapter borrows it, so opening a second one would leave an owner
// with nothing to own.
func TestAdoptSessionLedgerRepoSkipsASharedStore(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	state := &AgentSessionState{}
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	adoptSessionLedgerRepo(sess, config.DefaultSubagentConfig, state,
		sessionRouting{Context: contextDispatcherWiring{sharedSQLite: store}})
	if state.LedgerRepo != nil || state.ownedLedgerStore != nil {
		t.Fatal("a shared context store must not be shadowed by a session-owned ledger")
	}
	// No agent state to hold one is likewise a no-op rather than a leak.
	adoptSessionLedgerRepo(sess, config.DefaultSubagentConfig, nil, sessionRouting{})

	// The routing may not carry the store even when the session has one -
	// the second guard reads it back off the session rather than trusting
	// the caller to have plumbed it.
	contextual := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	if err := enableSessionContext(contextual, t.TempDir(), store, &config.Resolved{}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultSubagentConfig
	cfg.StoreBackend = "sqlite"
	unwired := &AgentSessionState{}
	adoptSessionLedgerRepo(contextual, cfg, unwired, sessionRouting{})
	if unwired.LedgerRepo != nil || unwired.ownedLedgerStore != nil {
		t.Fatal("the session's own context store must not be shadowed either")
	}
}

// sqliteLedgerConfig is a subagent config whose ledger backend is a real
// throwaway SQLite file, so the session actually owns a durable store.
func sqliteLedgerConfig(t *testing.T) config.SubagentConfig {
	t.Helper()
	cfg := config.DefaultSubagentConfig
	cfg.StoreBackend = "sqlite"
	cfg.StorePath = filepath.Join(t.TempDir(), "ledger.db")
	return cfg
}

// TestSessionOwnedLedgerRepoStaysRecoverable (R4-1): the coordinator reaches
// startup recovery through an optional-interface assertion on the repository it
// was handed. A wrapper that embeds ledger.LedgerRepository does not promote
// Recover, so wrapping silently turns interrupted-run recovery off.
func TestSessionOwnedLedgerRepoStaysRecoverable(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	state := &AgentSessionState{}
	adoptSessionLedgerRepo(sess, sqliteLedgerConfig(t), state, sessionRouting{})
	t.Cleanup(func() { releaseSessionLedgerRepo(state) })
	if state.LedgerRepo == nil {
		t.Fatal("no session-owned ledger repository was adopted")
	}
	recoverer, ok := state.LedgerRepo.(interface {
		Recover(ctx context.Context) ([]ledger.RecoveredRun, error)
	})
	if !ok {
		t.Fatal("the session-owned ledger repository hides Recover from the coordinator")
	}
	if _, err := recoverer.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
}

// TestInitCoordinatorLeavesACallerOwnedStoreOpen (R4-1): closing the dispatcher
// must not close a durable repository the caller owns - the session keeps using
// it across every surface rebuild. Ownership is decided by who opened the
// store, not by what concrete type the coordinator recognises.
func TestInitCoordinatorLeavesACallerOwnedStoreOpen(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewStorageLedgerRepository(store)
	t.Cleanup(func() { _ = repo.Close() })
	dispatcher, err := runtime.NewToolDispatcher(tierRegistry("read_file"), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	initCoordinator(dispatcher, config.DefaultSubagentConfig, repo)
	dispatcher.Close()
	if _, err := repo.Recover(context.Background()); err != nil {
		t.Fatalf("the dispatcher closed a caller-owned ledger store: %v", err)
	}
}

// TestAttachFailureReleasesTheSessionOwnedLedgerStore (R4-3): the ledger store
// is opened before the dispatcher is built, and a failed build returns no
// cleanup, so the error path is the only place that can close it.
func TestAttachFailureReleasesTheSessionOwnedLedgerStore(t *testing.T) {
	dir := t.TempDir()
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	// read_output is a ledger tool the dispatcher registers itself; a collision
	// fails the build after the store has been adopted.
	sess.Tools = tierRegistry("read_output")
	state := &AgentSessionState{}
	cleanup, err := attachSessionDispatcher(sess, dir, "m", sqliteLedgerConfig(t), state, skills.NewRegistry(), sessionRouting{})
	if err == nil {
		t.Fatal("expected the dispatcher build to fail")
	}
	if cleanup != nil {
		t.Fatal("a failed attach handed back a cleanup the caller is told not to use")
	}
	if state.LedgerRepo != nil || state.ownedLedgerStore != nil {
		t.Fatal("the session-owned ledger store leaked on the attach error path")
	}
}

// TestReleaseSessionLedgerRepoClosesTheStore pins what "release" means: the
// store is really closed, not merely forgotten.
func TestReleaseSessionLedgerRepoClosesTheStore(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	state := &AgentSessionState{}
	adoptSessionLedgerRepo(sess, sqliteLedgerConfig(t), state, sessionRouting{})
	store := state.ownedLedgerStore
	if store == nil {
		t.Fatal("no durable store was adopted")
	}
	releaseSessionLedgerRepo(state)
	if state.LedgerRepo != nil || state.ownedLedgerStore != nil {
		t.Fatal("release left the freed repository reachable")
	}
	if _, err := store.Recover(context.Background()); err == nil {
		t.Fatal("release did not close the store")
	}
}

// TestInitCoordinatorClosesOnlyItsOwnStore: the coordinator opens a durable
// store when no repo is supplied, and only that store is its to close. A
// caller-supplied repo outlives the dispatcher (round-4: the old type-assertion
// closed whatever it recognized, which is what forced the wrapper that then
// hid Recover from the coordinator).
func TestInitCoordinatorClosesOnlyItsOwnStore(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultSubagentConfig
	cfg.StoreBackend = "sqlite"
	cfg.StorePath = filepath.Join(dir, "orchestration.db")
	dispatcher, err := runtime.NewToolDispatcher(tierRegistry("read_file"), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	// No repo argument at all: the coordinator opens (and owns) its own.
	initCoordinator(dispatcher, cfg)
	repo, ok := coordinatorRepos.Load(dispatcher)
	if !ok {
		t.Fatal("initCoordinator registered no repo")
	}
	if _, isMemory := repo.(*ledger.MemoryLedgerRepository); isMemory {
		t.Skip("sqlite backend fell back to memory; nothing owned to close")
	}
	dispatcher.Close()
	if _, still := coordinatorRepos.Load(dispatcher); still {
		t.Fatal("the close hook did not deregister the coordinator repo")
	}
	// The store it opened is closed, so a fresh open of the same file works.
	reopened, err := storage.OpenSQLite(cfg.StorePath)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	_ = reopened.Close()
}

func TestReleaseSessionLedgerRepoWithoutState(t *testing.T) {
	// The failure path can run before any agent state exists; releasing must
	// be a no-op rather than a nil dereference.
	releaseSessionLedgerRepo(nil)
	releaseSessionLedgerRepo(&AgentSessionState{})
}
