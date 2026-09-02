package cliagents

import (
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ToolTierPlan is one agent binding's frozen deferred-tool decision (plan
// tools/05 D8). It is computed once per binding, before any admission, and
// never recomputed while that binding lives: the prompt index it feeds must
// stay byte-stable across admissions, so it is stale by design afterwards.
type ToolTierPlan struct {
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
func (p ToolTierPlan) Deferred() bool { return len(p.Candidates) > 0 }

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
func PlanToolTiers(base *tools.Registry, selected *agents.ResolvedAgent, res *config.Resolved) ToolTierPlan {
	core := agentCoreTier(selected, res)
	if base == nil || core == nil {
		return ToolTierPlan{Tiers: tools.Tiers{Core: authorizedNamesInRegistryOrder(base, selected)}}
	}
	authorized := authorizedNamesInRegistryOrder(base, selected)
	tiers := tools.SplitTiers(authorized, withMCPServerToolsAlwaysCore(*core, authorized, selected))
	plan := ToolTierPlan{Tiers: tiers}
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

// withMCPServerToolsAlwaysCore extends a configured core-tier list with every
// authorized MCP-discovered tool, so an operator's [tools] core (or an
// agent's tools_core) can never silently defer one.
//
// That list is hand-authored and names static, compiled-in tool names. An
// MCP tool's "mcp__<server>__x<hex>" name is derived at runtime from what the
// remote server reports, and changes the moment that server's tool set does,
// so a list written ahead of time cannot track it. Without this, naming ANY
// core tier at all (this repo's own .mivia/mivia.toml does, to control prompt
// cost) silently and permanently moves every MCP tool into the deferred tier,
// with no practical way to opt back in.
//
// The name is NOT unguessable, and this comment used to say it was - "a
// runtime hash ... no way to opt back in short of predicting the hash".
// EncodeToolName writes a plain reversible hex encoding, so a name IS
// derivable; what defeats a hand-authored list is that it moves, not that it
// cannot be spelled. The distinction matters because the same false claim was
// the stated reason AuthorizedAgentTools granted these tools authority
// without checking any denylist. isMCPServerTool already
// treats server selection as authority over an MCP tool's AUTHORIZATION
// (authorizedAgentTools, mcp_scope.go); this applies the same rule to core-
// tier placement, since a tool the agent cannot even see advertised is a
// stronger deferral than a tool it can no longer call.
//
// Copies core rather than appending in place: core aliases the config
// layer's *[]string, and append can silently write through spare capacity
// into memory another binding still reads.
func withMCPServerToolsAlwaysCore(core, authorized []string, selected *agents.ResolvedAgent) []string {
	if selected == nil || len(selected.EffectiveMCPServers) == 0 {
		return core
	}
	out := make([]string, len(core), len(core)+len(authorized))
	copy(out, core)
	for _, name := range authorized {
		if isMCPServerTool(name, selected) {
			out = append(out, name)
		}
	}
	return out
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
		kept, _ := agents.IntersectWithRegistry(AuthorizedAgentTools(selected, base), base)
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
func TieredRootRegistry(base *tools.Registry, selected *agents.ResolvedAgent, extraDenylist []string, plan ToolTierPlan, admitted []string) *tools.Registry {
	if base == nil || !plan.Deferred() {
		// The disabled-tool report is discarded here on purpose: the caller has
		// already scoped the same base for authority and reported it once.
		scoped, _ := ScopedRootRegistry(base, selected, extraDenylist)
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
func promptWithDeferredIndex(prompt string, plan ToolTierPlan) string {
	index := tools.DeferredIndex(plan.Candidates)
	if index == "" {
		return prompt
	}
	if prompt == "" {
		return index
	}
	return prompt + "\n\n" + index
}

// shortDescTool overrides a deferred tool's advertised description with its
// one-line summary (the same text DeferredIndex puts in the frozen prompt
// index) while leaving Name/Parameters/Execute untouched. The model already
// gets the one-liner in the prompt index at bind time and the full
// description once, at admission time, via load_tools's render - the full
// text in the wire schema for a still-locked tool is pure duplication. Used
// only to build throwaway registries for wire-spec / schema-cost rendering,
// never for execution.
type shortDescTool struct{ tools.Tool }

func (t shortDescTool) Description() string { return tools.FirstLine(t.Tool.Description()) }

// advertisedToolSpecs computes the binding's pinned tools[] array (plan
// tools-advertising/01): core tier, then every deferred candidate in the
// frozen plan's registry order, then the session-tool tail from
// sessionToolCatalog, truncated overall to tools.MaxAdvertisedTools. This is
// the SESSION ADMISSIBLE UNION - what the model may ever be told about for
// this binding - not the live execution registry, so it must be built from
// base (the full pre-scope registry), never from a scoped/admitted registry.
// Ordering here is load-bearing: OpenAI-compatible providers invalidate their
// implicit prompt cache on any change to the tools[] array, INCLUDING a
// reorder, so this order must stay stable for the binding's whole lifetime.
// A deferred candidate's advertised description is shortened to its one-line
// summary (shortDescTool); its parameter schema still ships in full, since
// that is what the model needs to invoke it correctly once admitted. Returns
// the dropped-name count so callers can log a truncation instead of silently
// shipping fewer tools than the plan authorizes.
func advertisedToolSpecs(base *tools.Registry, plan ToolTierPlan, agentReg *agents.AgentRegistry) ([]provider.ToolSpec, int) {
	if base == nil {
		return nil, 0
	}
	// The session-tool tail ships below, so reserve exactly as many slots as
	// the catalog will advertise for this binding (the always-on session
	// tools, plus load_tools when something is deferred) and the FINAL wire
	// array never exceeds tools.MaxAdvertisedTools (a prior version truncated
	// core+deferred to the full cap and then appended schemas on top,
	// silently producing a 129th).
	var tail []provider.ToolSpec
	if AdvertisedSessionToolSpecsVar != nil {
		tail = AdvertisedSessionToolSpecsVar(plan, agentReg)
	}
	names, dropped := tools.AdvertisedNames(plan.Tiers.Core, plan.Tiers.Deferred, len(tail))
	deferredSet := make(map[string]struct{}, len(plan.Tiers.Deferred))
	for _, name := range plan.Tiers.Deferred {
		deferredSet[name] = struct{}{}
	}
	reg := tools.NewRegistry()
	for _, name := range names {
		tool, ok := base.Get(name)
		if !ok {
			continue
		}
		if _, deferred := deferredSet[name]; deferred {
			tool = shortDescTool{tool}
		}
		reg.Register(tool)
	}
	specs := reg.OpenAITools()
	return append(specs, tail...), dropped
}

// AdvertisedToolSpecs is the exported view of advertisedToolSpecs for callers
// outside cliagents (tests, session catalog introspection).
func AdvertisedToolSpecs(base *tools.Registry, plan ToolTierPlan, agentReg *agents.AgentRegistry) ([]provider.ToolSpec, int) {
	return advertisedToolSpecs(base, plan, agentReg)
}

// pinAttachAdvertisedToolSpecs computes and pins the initial-attach binding's
// advertised union BEFORE sess.Tools is narrowed to its core-tier execution
// registry: the union must be built from the full pre-scope base (plan
// tools-advertising/01), same as buildSurfaceFromBase does for /agent and
// /model. agentReg is the session's immutable resolved registry snapshot,
// threaded into the advertised session-tool schemas. Must be called before
// sess.Tools is reassigned.
func PinAttachAdvertisedToolSpecs(sess *chat.Session, selected *agents.ResolvedAgent, plan ToolTierPlan, agentReg *agents.AgentRegistry) {
	advertised, dropped := advertisedToolSpecs(sess.Tools, plan, agentReg)
	warnAdvertisedToolsTruncated(selected, dropped)
	sess.SetAdvertisedToolSpecs(advertised)
}

// agentNameOf is the empty-safe agent name used to key persisted admissions.
func AgentNameOf(selected *agents.ResolvedAgent) string {
	if selected == nil {
		return ""
	}
	return selected.Name
}
