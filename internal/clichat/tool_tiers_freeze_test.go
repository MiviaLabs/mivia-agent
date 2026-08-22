package clichat

import (
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Frozen-plan and clamping properties of the deferred-tool surface: a tier plan
// is computed once per agent binding, and every later use of it - an admission
// tail, a model switch - must publish no more than the current agent is
// authorized for and no differently than the plan already said.

// TestTieredRootRegistryClampsTheAdmittedTailToTheSelectedAgent is the tail-side
// half of the clamp. The core tier is checked in tool_authority_test.go; here a
// frozen plan that outlived its agent carries an admitted name the current
// selection cannot invoke, and the tail must not smuggle it back onto the
// surface.
func TestTieredRootRegistryClampsTheAdmittedTailToTheSelectedAgent(t *testing.T) {
	base := tierRegistry("read_file", "write_file")
	selected := &agents.ResolvedAgent{Name: "reader", EffectiveTools: []string{"read_file"}}
	stale := toolTierPlan{
		Tiers:      tools.Tiers{Core: []string{"read_file"}, Deferred: []string{"write_file"}},
		Candidates: []tools.TierCandidate{{Name: "write_file"}},
	}
	got := tieredRootRegistry(base, selected, nil, stale, []string{"write_file"})
	if _, ok := got.Get("write_file"); ok {
		t.Fatalf("an admitted tool the agent is not authorized for reached the surface: %v", registryToolNames(got))
	}
	if _, ok := got.Get("read_file"); !ok {
		t.Fatal("clamping the tail dropped the authorized core tier")
	}
	// The same tail under an agent that IS authorized still lands, so the
	// assertion above is about authority and not about tails never publishing.
	authorized := &agents.ResolvedAgent{Name: "writer", EffectiveTools: []string{"read_file", "write_file"}}
	live := tieredRootRegistry(base, authorized, nil, stale, []string{"write_file"})
	if _, ok := live.Get("write_file"); !ok {
		t.Fatalf("an authorized admission did not publish: %v", registryToolNames(live))
	}
}

// TestModelSwitchReusesTheFrozenTierPlan pins the freeze itself. A /model
// switch is not a new binding, so it must carry the tier split computed when
// the binding was created rather than re-tiering from whatever the inputs say
// now: the prompt index the binding still carries was rendered from the frozen
// split, and a surface that disagrees with it advertises a core block the model
// was never told about (and can retire load_tools out from under a deferred
// index). Only /agent may re-tier.
//
// The drift is applied to the selected agent's core list because that is the
// single input planToolTiers reads that a caller could change mid-binding; the
// property is that no input change reaches the surface through /model.
func TestModelSwitchReusesTheFrozenTierPlan(t *testing.T) {
	completer := &scriptedCompleter{turns: []provider.Response{{Content: "done"}}}
	fixture := newSwitchableFixture(t, t.TempDir(), completer, []string{"read_file"}, []string{"read_file", "grep"})
	before := registryToolNames(fixture.sess.Tools)
	if slices.Contains(before, "grep") {
		t.Fatalf("precondition: grep is not deferred before the switch: %v", before)
	}
	fixture.state.Lock()
	widened := *fixture.state.Selected
	widened.CoreTools = corePtr("read_file", "grep")
	fixture.state.Selected = &widened
	fixture.state.Unlock()

	switchToOtherModel(t, fixture)

	after := registryToolNames(fixture.sess.Tools)
	if slices.Contains(after, "grep") {
		t.Fatalf("/model re-tiered the binding: a deferred tool entered the core block: %v", after)
	}
	if !slices.Contains(after, tools.LoadToolsToolName) {
		t.Fatalf("/model re-tiered the binding: load_tools was retired under a live deferred index: %v", after)
	}
}
