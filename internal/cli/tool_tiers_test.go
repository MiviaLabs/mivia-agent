package cli

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
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
	advertised := tierRegistry("read_file")
	plan := toolTierPlan{Candidates: []tools.TierCandidate{{Name: "grep"}, {Name: "glob"}}}
	mass := measureSchemaMass(advertised, base, plan, nil, "reader", "attach")
	if mass.Advertised != 1 || mass.Deferred != 2 {
		t.Fatalf("mass = %+v, want 1 advertised and 2 deferred", mass)
	}
	if mass.Tokens <= 0 || mass.HeldTokens <= 0 {
		t.Fatalf("mass = %+v, want positive token measurements", mass)
	}
	if !strings.Contains(mass.String(), "1 tools advertised") || !strings.Contains(mass.String(), "2 deferred") {
		t.Fatalf("line = %q", mass.String())
	}
}

func TestMeasureSchemaMassOnAnInertSurface(t *testing.T) {
	mass := measureSchemaMass(tierRegistry("read_file"), nil, toolTierPlan{}, nil, "", "attach")
	if mass.Deferred != 0 || mass.HeldTokens != 0 {
		t.Fatalf("mass = %+v, want nothing withheld", mass)
	}
	if strings.Contains(mass.String(), "deferred") {
		t.Fatalf("inert line mentions deferral: %q", mass.String())
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
	publishSchemaMass(sess, schemaMass{Advertised: 3, Tokens: 120, Deferred: 2, HeldTokens: 80, AgentName: "reader", Publication: "attach"})
	bus.Flush()
	select {
	case ev := <-received:
		if ev.Name != "tool_schema_mass" {
			t.Fatalf("event name = %q", ev.Name)
		}
		if ev.Metadata["tools_deferred"] != "2" || ev.Metadata["deferred_held_tokens"] != "80" {
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
	if (*agentSessionState)(nil).schemaMassSnapshot() != (schemaMass{}) {
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
	if capability := tool.Capability(nil); capability.Class != tools.ExecutionRead {
		t.Fatalf("capability class = %v, want read", capability.Class)
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
	if !strings.Contains(again, "already loaded: grep") {
		t.Fatalf("idempotent result = %q", again)
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
	state := &agentSessionState{WorkspaceRoot: t.TempDir()}
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
	for i := 0; i < tools.MaxAdmissionAttempts; i++ {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{"names":["grep"]}`)); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
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
	if !strings.Contains(out, "loaded: glob") || !strings.Contains(out, "already loaded: grep") {
		t.Fatalf("mixed result = %q, want both sections", out)
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
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "narrow", false); err != nil {
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
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}); err != nil {
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
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "wide", false); err != nil {
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
