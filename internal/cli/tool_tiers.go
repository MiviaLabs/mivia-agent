package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// toolTierPlan is one agent binding's frozen deferred-tool decision (plan
// tools/05 D8). It is computed once per binding, before any admission, and
// never recomputed while that binding lives: the prompt index it feeds must
// stay byte-stable across admissions, so it is stale by design afterwards.
type toolTierPlan struct {
	Tiers tools.Tiers
	// Candidates are the deferred tools with their one-line descriptions, in
	// live-registry order. Empty means the plan is inert.
	Candidates []tools.TierCandidate
	// Digest fingerprints the tier split a persisted admitted set was made
	// against, so a resumed session can drop a stale set fail-closed.
	Digest string
}

// Deferred reports whether this binding defers anything at all. A plan that
// defers nothing must behave byte-identically to a build without the feature:
// no load_tools tool, no prompt index, no tail ordering.
func (p toolTierPlan) Deferred() bool { return len(p.Candidates) > 0 }

// agentCoreTier resolves the core tier list for the selected agent. A per-agent
// tools_core overrides the global [tools] core; nil at both levels means every
// authorized tool stays core and the feature is inert.
func agentCoreTier(selected *agents.ResolvedAgent, res *config.Resolved) *[]string {
	if selected != nil && selected.CoreTools != nil {
		return selected.CoreTools
	}
	if res != nil {
		return res.Tools.Core
	}
	return nil
}

// planToolTiers splits the authorized tool set of the live pre-scope registry
// into core and deferred tiers. Authority is unchanged: the split only decides
// which authorized schemas ship on every request and which wait for load_tools.
func planToolTiers(base *tools.Registry, selected *agents.ResolvedAgent, res *config.Resolved) toolTierPlan {
	core := agentCoreTier(selected, res)
	if base == nil || core == nil {
		return toolTierPlan{Tiers: tools.Tiers{Core: authorizedNamesInRegistryOrder(base, selected)}}
	}
	authorized := authorizedNamesInRegistryOrder(base, selected)
	tiers := tools.SplitTiers(authorized, *core)
	plan := toolTierPlan{Tiers: tiers}
	for _, name := range tiers.Deferred {
		// Every name here came out of base.List(), so the lookup cannot miss.
		tool, _ := base.Get(name)
		plan.Candidates = append(plan.Candidates, tools.TierCandidate{Name: name, Description: tool.Description()})
	}
	agentName := ""
	if selected != nil {
		agentName = selected.Name
	}
	plan.Digest = tools.AdmissionDigest(agentName, tiers)
	return plan
}

// authorizedNamesInRegistryOrder lists the names the selected agent may invoke,
// in the live registry's registration order. A nil agent means the compiled
// default, which authorizes everything registered.
func authorizedNamesInRegistryOrder(base *tools.Registry, selected *agents.ResolvedAgent) []string {
	if base == nil {
		return nil
	}
	var allowed map[string]struct{}
	if selected != nil {
		kept, _ := agents.IntersectWithRegistry(authorizedAgentTools(selected, base), base)
		allowed = agents.AllowlistSet(kept)
	}
	out := make([]string, 0, len(base.List()))
	for _, tool := range base.List() {
		name := tool.Name()
		if allowed != nil {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		out = append(out, name)
	}
	return out
}

// tieredRootRegistry materializes the core tier in base order and appends the
// admitted tools as a tail (plan tools/05 D8). An inert plan falls through to
// the ordinary root scope so a zero-config session is byte-identical to a build
// without deferred loading.
func tieredRootRegistry(base *tools.Registry, selected *agents.ResolvedAgent, extraDenylist []string, plan toolTierPlan, admitted []string) *tools.Registry {
	if base == nil || !plan.Deferred() {
		// The disabled-tool report is discarded here on purpose: the caller has
		// already scoped the same base for authority and reported it once.
		scoped, _ := scopedRootRegistry(base, selected, extraDenylist)
		return scoped
	}
	// The plan is frozen per binding while the selection can move, so clamp both
	// tiers to what selected may actually invoke. A plan that outlived its agent
	// then publishes less, never more.
	authorized := agents.AllowlistSet(authorizedNamesInRegistryOrder(base, selected))
	return tools.ScopedRegistryWithTail(base, tools.ScopeOptions{
		Mode:          tools.ScopeRoot,
		Allowlist:     agents.AllowlistSet(clampToAuthorized(plan.Tiers.Core, authorized)),
		ExtraDenylist: extraDenylist,
	}, clampToAuthorized(admitted, authorized))
}

// clampToAuthorized drops names the selected agent is not authorized for,
// preserving order.
//
// An entirely unauthorized list clamps to empty, which is deny-all rather than
// an absent allowlist filter: the core-tier caller converts the result with
// agents.AllowlistSet, which returns a non-nil (empty) map for an empty list,
// and tools.ScopeOptions treats only a nil Allowlist as "no filter". The
// non-nil empty slice returned here is belt and braces for a future caller that
// distinguishes the two; it is not what makes the current path fail closed.
func clampToAuthorized(names []string, authorized map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := authorized[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// promptWithDeferredIndex appends the frozen deferred-tool index to the agent
// prompt. It is generated once per binding and carried unchanged through every
// admission, which is what keeps system-prompt bytes stable.
func promptWithDeferredIndex(prompt string, plan toolTierPlan) string {
	index := tools.DeferredIndex(plan.Candidates)
	if index == "" {
		return prompt
	}
	if prompt == "" {
		return index
	}
	return prompt + "\n\n" + index
}

// agentNameOf is the empty-safe agent name used to key persisted admissions.
func agentNameOf(selected *agents.ResolvedAgent) string {
	if selected == nil {
		return ""
	}
	return selected.Name
}
