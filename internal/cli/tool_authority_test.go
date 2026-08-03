package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// collideWithDelegate makes the next surface build fail at the delegation-tool
// registration, which happens well after the new binding's tier plan and skill
// registry would have been computed. That is the only interesting failure
// point: an /agent switch that dies here must leave nothing of the new agent
// behind.
func collideWithDelegate(state *agentSessionState) {
	state.ToolBase.Register(namedTool{name: "delegate"})
}

// TestFailedAgentSwitchLeavesNoTraceOfTheNewAgent pins the authority-escalation
// fix: a refused /agent switch must not leave the new agent's tier plan (whose
// core tier is the admission allowlist) installed under the old selection.
func TestFailedAgentSwitchLeavesNoTraceOfTheNewAgent(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	collideWithDelegate(fixture.state)
	writer := agents.ResolvedAgent{
		Name: "writer", SystemPrompt: "W",
		EffectiveTools: []string{"read_file", "write_file", "delegate"},
		CoreTools:      corePtr("write_file", "delegate"),
	}
	if err := fixture.state.Registry.Publish(writer); err != nil {
		t.Fatal(err)
	}
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "writer", false); err == nil {
		t.Fatal("a surface build that cannot register its delegation tools was accepted")
	}
	if got := currentAgentName(fixture.state); got != "reader" {
		t.Fatalf("selected agent = %q, want the switch rolled back to reader", got)
	}
	fixture.state.mu.Lock()
	core := slices.Clone(fixture.state.TierPlan.Tiers.Core)
	fixture.state.mu.Unlock()
	if slices.Contains(core, "write_file") {
		t.Fatalf("tier plan core = %v, want reader's plan, not the failed agent's", core)
	}

	// End to end: the next admission rebuilds from the installed plan, so a
	// stale plan would republish the failed agent's core tier under reader.
	if _, err := fixture.sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "go", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if _, ok := fixture.sess.Tools.Get("write_file"); ok {
		t.Fatalf("write_file went live under reader: %v", registryToolNames(fixture.sess.Tools))
	}
	if fixture.sess.Dispatcher.Has(runtime.Tool, "write_file") {
		t.Fatal("write_file became invocable under reader")
	}
	if _, ok := fixture.sess.Tools.Get("grep"); !ok {
		t.Fatal("the admission did not publish reader's own deferred tool")
	}
}

// TestTieredRootRegistryClampsAPlanToTheSelectedAgent is the defence in depth
// behind the fix above: however a plan and a selection come to disagree, the
// core tier can never advertise or authorize past the selected agent.
func TestTieredRootRegistryClampsAPlanToTheSelectedAgent(t *testing.T) {
	base := tierRegistry("read_file", "write_file")
	selected := &agents.ResolvedAgent{Name: "reader", EffectiveTools: []string{"read_file"}}
	stale := toolTierPlan{
		Tiers:      tools.Tiers{Core: []string{"read_file", "write_file"}},
		Candidates: []tools.TierCandidate{{Name: "grep"}},
	}
	got := tieredRootRegistry(base, selected, nil, stale, nil)
	if _, ok := got.Get("write_file"); ok {
		t.Fatal("a core tier naming an unauthorized tool published it")
	}
	if _, ok := got.Get("read_file"); !ok {
		t.Fatal("clamping dropped an authorized core tool")
	}
	// A plan whose whole core tier is unauthorized must deny all, never fall
	// through to an unfiltered surface.
	empty := tieredRootRegistry(base, selected, nil, toolTierPlan{
		Tiers:      tools.Tiers{Core: []string{"write_file"}},
		Candidates: []tools.TierCandidate{{Name: "grep"}},
	}, nil)
	if _, ok := empty.Get("read_file"); ok {
		t.Fatal("an entirely unauthorized core tier republished the agent's tools")
	}
}

// --- authority vs advertised (a core tier must not shrink delegation) ------

func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".mivia", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCoreTierDoesNotDeregisterASkillNeedingADeferredTool: the core tier is an
// advertising decision for the root model. A skill declaring a tool the root
// agent is authorized for must still register, even when that tool is deferred.
func TestCoreTierDoesNotDeregisterASkillNeedingADeferredTool(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	dir := t.TempDir()
	writeSkill(t, dir, "searchy", "---\nname: searchy\ndescription: Search things\ntools: [grep]\n---\nSearch.")
	fixture := newDeferredFixtureIn(t, dir, completer, []string{"read_file"}, []string{"read_file", "grep"})
	if !fixture.sess.Dispatcher.Has(runtime.Subagent, "searchy") {
		t.Fatal("a skill needing a deferred tool was silently never registered")
	}
}

// TestCoreTierDoesNotShrinkARoutedAgentsToolSurface: a routed sub-agent's
// authority is the root agent's authorized set intersected with its own, never
// the root's core tier.
func TestCoreTierDoesNotShrinkARoutedAgentsToolSurface(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "found it"}}}
	fixture := newDeferredFixture(t, completer, []string{"read_file"}, []string{"read_file", "grep"})
	searcher := agents.ResolvedAgent{Name: "searcher", SystemPrompt: "S", EffectiveTools: []string{"grep"}}
	if err := fixture.state.Registry.Publish(searcher); err != nil {
		t.Fatal(err)
	}
	// Rebuild the surface so the new definition gets a handler.
	if err := applySessionAgent(fixture.sess, fixture.res, fixture.state, "reader", false); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	stored, ok := fixture.state.Registry.Get("searcher")
	if !ok {
		t.Fatal("searcher missing from the agent registry")
	}
	digest, err := stored.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	before := len(completer.advertised)
	result := fixture.sess.Dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "routed-1", Kind: runtime.Subagent, Name: "searcher",
		AgentName: "searcher", AgentDigest: digest,
		Input: json.RawMessage(`"find something"`), SessionID: "test",
	})
	if result.Err != nil {
		t.Fatalf("routed task: %v", result.Err)
	}
	advertised, _ := completer.requests()
	if len(advertised) <= before {
		t.Fatal("the routed agent issued no request")
	}
	if !slices.Contains(advertised[before], "grep") {
		t.Fatalf("routed agent tools = %v, want grep from the root's authorized set", advertised[before])
	}
}
