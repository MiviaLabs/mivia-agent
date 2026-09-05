package cliagents

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// agentSurface is a fully built, not-yet-installed binding surface. It carries
// every piece of agentSessionState the build computed, because a build that
// fails halfway must leave the live state untouched: a tier plan or skill scope
// belonging to an agent that was never selected is an authority grant nobody
// asked for. Only the caller's commit block writes these onto the state.
type agentSurface struct {
	registry   *tools.Registry
	dispatcher *runtime.Dispatcher
	skillReg   *skills.Registry
	// plan is the binding's frozen tier split; skillRegFull the unfiltered
	// registry the build loaded; skillScope the policy built against registry.
	plan         ToolTierPlan
	skillRegFull *skills.Registry
	skillScope   AgentSkillScope
	// advertised is the binding's pinned tools[] array (plan
	// tools-advertising/01): the session admissible union, computed once from
	// base and plan, independent of what is currently admitted. advertisedDropped
	// counts names truncated by tools.MaxAdvertisedTools.
	advertised        []provider.ToolSpec
	advertisedDropped int
}

// commitTo installs a successfully built surface's derived state. Callers hold
// state.mu.
func (s *agentSurface) commitTo(state *AgentSessionState) {
	state.TierPlan = s.plan
	state.SkillRegFull = s.skillRegFull
	state.SkillScope = s.skillScope
}

// buildAgentScopedSurface builds a fresh agent binding's surface: it loads
// skills from disk, freezes this binding's core/deferred tool split, and admits
// nothing. Every admission after this point reuses the frozen plan.
func buildAgentScopedSurface(sess *chat.Session, res *config.Resolved, state *AgentSessionState, selected *agents.ResolvedAgent) (*agentSurface, error) {
	root := state.WorkspaceRoot
	if root == "" {
		root = "."
	}
	skillReg, warnings, err := LoadSessionSkills(root, state.AllowProjectSkills)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	WarnSkillLoad(warnings)
	skillReg = FilterSkillRegistryForGate(skillReg, state.AllowProjectSkills)
	entry := entryBase(sess, state)
	if entry == nil {
		return nil, fmt.Errorf("tool base is unavailable")
	}
	base := entry.CloneForGenerationExcluding("ledger_read", "list_run_events", "read_output")
	plan := PlanToolTiers(base, selected, res)
	return buildSurfaceFromBase(sess, res, state, surfaceBuildRequest{
		selected: selected, base: base, skillReg: skillReg, plan: plan,
	})
}

// buildWidenedWith derives the same binding's surface with admitted appended as
// a tail (plan tools/05 D7). It reuses the frozen tier plan and the already
// loaded skill registry, so it performs no disk I/O and cannot change the
// prompt index, the core block, or the skill policy.
func buildWidenedWith(sess *chat.Session, res *config.Resolved, state *AgentSessionState, admitted []string) (*agentSurface, error) {
	if state.SkillRegFull == nil {
		return nil, fmt.Errorf("tool admission: no skill registry captured for this binding")
	}
	entry := entryBase(sess, state)
	if entry == nil {
		return nil, fmt.Errorf("tool base is unavailable")
	}
	base := entry.CloneForGenerationExcluding("ledger_read", "list_run_events", "read_output")
	return buildSurfaceFromBase(sess, res, state, surfaceBuildRequest{
		selected: state.Selected, base: base, skillReg: state.SkillRegFull,
		plan: state.TierPlan, admitted: admitted,
		// TryPublishAgentSurface (the admission-widening publication path
		// this candidate feeds) never writes AdvertisedToolSpecs onto the
		// session by design (plan tools-advertising/01: admission changes
		// execution authority only) - computing the advertised union here
		// would be thrown away unread on every load_tools call.
		skipAdvertised: true,
	})
}

// AgentSurface is the exported view of a widened agent surface. It exposes the
// built dispatcher so callers outside cliagents can close it or probe it.
type AgentSurface struct {
	// Dispatcher is the built tool dispatcher. The caller must close it when
	// it is no longer needed or will not be published to a session.
	Dispatcher *runtime.Dispatcher
}

// BuildWidenedWith derives the same binding's surface with admitted appended as
// a tail and returns it for the caller to inspect or publish. See buildWidenedWith.
func BuildWidenedWith(sess *chat.Session, res *config.Resolved, state *AgentSessionState, admitted []string) (*AgentSurface, error) {
	s, err := buildWidenedWith(sess, res, state, admitted)
	if err != nil {
		return nil, err
	}
	return &AgentSurface{Dispatcher: s.dispatcher}, nil
}

// surfaceBuildRequest is one surface build's inputs. binding is the only
// optional field: it overrides the live session binding for a generation that
// is built but not yet published, which is what a model switch is.
type surfaceBuildRequest struct {
	selected *agents.ResolvedAgent
	base     *tools.Registry
	skillReg *skills.Registry
	plan     ToolTierPlan
	admitted []string
	binding  *chat.ModelBinding
	// skipAdvertised skips the advertised-union computation for a build whose
	// caller never applies agentSurface.advertised to the session (currently
	// only buildWidenedWith: TryPublishAgentSurface never writes
	// AdvertisedToolSpecs, so computing it there was pure wasted work on the
	// admission-widening hot path - up to MaxAdvertisedTools schema
	// constructions thrown away on every load_tools call).
	skipAdvertised bool
}

func buildSurfaceFromBase(sess *chat.Session, res *config.Resolved, state *AgentSessionState, req surfaceBuildRequest) (*agentSurface, error) {
	selected, base, skillReg, plan, admitted := req.selected, req.base, req.skillReg, req.plan, req.admitted
	binding := sess.CurrentBinding()
	if req.binding != nil {
		binding = *req.binding
	}
	if binding.Completer == nil {
		return nil, fmt.Errorf("dispatcher: nil completer")
	}
	// Start from the pre-scope base so switching to a wider agent regains tools.
	// Apply root agent scope BEFORE building the dispatcher so the dispatcher
	// captures a scoped registry. This keeps the dispatcher and sess.Tools in
	// agreement (INV-AG-29 execution denial).
	registry := TieredRootRegistry(base, selected, state.Global.MandatoryToolDenylistAdditions, plan, admitted)
	// Authority is the root agent's whole authorized set, deferred tier
	// included: the tier split decides what the root model is shown, never what
	// this session may delegate. The skill policy and every nested handler read
	// authority; only the model-facing surface reads registry.
	authority, _ := ScopedRootRegistry(base, selected, state.Global.MandatoryToolDenylistAdditions)
	// The skill policy is built against the final live authority registry
	// The skill policy is built against the final live authority registry
	// (plan 43) and returned for the caller to install on commit.
	skillScope := SkillScopeFromAgentAndRegistry(selected, authority)
	var dispatcher *runtime.Dispatcher
	if NewSessionDispatcherVar != nil {
		var err error
		dispatcher, err = NewSessionDispatcherVar(dispatcherOptsForSurface(sess, res, state, binding, registry, authority, skillReg, skillScope, plan, sessionToolRoot(sess, state.WorkspaceRoot)))
		if err != nil {
			return nil, fmt.Errorf("dispatcher: %w", err)
		}
	}
	// The advertised union is computed from base (the full pre-scope
	// registry) and the frozen plan, NOT from registry: registry is scoped to
	// core-plus-admitted execution authority, while the advertised snapshot
	// must cover the whole admissible union regardless of what has been
	// admitted so far (plan tools-advertising/01). Skipped entirely when the
	// caller (buildWidenedWith) never applies it to the session.
	var advertised []provider.ToolSpec
	var advertisedDropped int
	if !req.skipAdvertised {
		advertised, advertisedDropped = advertisedToolSpecs(base, plan, state.Registry)
	}
	return &agentSurface{
		registry:          registry,
		dispatcher:        dispatcher,
		skillReg:          FilterSkillsForScope(skillReg, skillScope),
		plan:              plan,
		skillRegFull:      skillReg,
		skillScope:        skillScope,
		advertised:        advertised,
		advertisedDropped: advertisedDropped,
	}, nil
}

// dispatcherOptsForSurface builds the SessionDispatcherOpts for one surface
// build. The session owns the ledger store, so no rebuilt dispatcher opens one
// it would then close on publication - under the spool this surface carries.
// Callers hold state.mu, so the repository field is read directly.
func dispatcherOptsForSurface(sess *chat.Session, res *config.Resolved, state *AgentSessionState, binding chat.ModelBinding, registry, authority *tools.Registry, skillReg *skills.Registry, skillScope AgentSkillScope, plan ToolTierPlan, root string) SessionDispatcherOpts {
	cfg := config.SubagentConfig{}
	var modelCatalog []config.ProviderModelGroup
	if res != nil {
		cfg = res.Subagents
		modelCatalog = res.ModelCatalog()
	}
	var contextWiring ContextDispatcherWiring
	if ContextDispatcherForVar != nil {
		contextWiring = ContextDispatcherForVar(sess, cfg)
	}
	return SessionDispatcherOpts{
		// A rebuilt dispatcher must carry the operator's approval wiring, or
		// every nested subagent loop it builds runs ungated. This struct is
		// rebuilt on /agent, /model, and whenever the model calls load_tools,
		// so omitting it here silently un-gated delegation after the first
		// turn - which is exactly what happened the first time round.
		Approval:          sess.ApprovalSnapshot,
		ToolDenylist:      state.Global.MandatoryToolDenylistAdditions,
		Registry:          registry,
		AuthorityRegistry: authority,
		// The session owns the ledger store, so no rebuilt dispatcher opens one
		// it would then close on publication - under the spool this surface
		// carries. Callers hold state.mu, so the field is read directly.
		Repo: state.LedgerRepo,
		// Same story for the memory store (plan 77, E2): the same instance
		// configureChatWorkspace opened, never a second Open.
		Memory:                    state.Memory,
		MemoryConfig:              state.MemoryConfig,
		Completer:                 binding.Completer,
		Model:                     binding.Model,
		ProviderName:              binding.ProviderName,
		ModelGeneration:           binding.ModelGeneration,
		ModelGenerationFunc:       sess.CurrentModelGeneration,
		ModelCatalog:              modelCatalog,
		CompleterFactory:          NewProviderCompleterFactory(res),
		Config:                    cfg,
		ToolResultCapBytes:        sess.MaxToolResultChars,
		ToolRunTimeout:            sess.ToolRunTimeout,
		BatchResultBudgetBytes:    sess.BatchResultBudgetBytes,
		RefOnlyTools:              sess.RefOnlyTools,
		WorkspaceRoot:             root,
		MaxContextTokens:          sess.PromptBudget(),
		MaxTokens:                 sess.MaxTokens,
		Budget:                    sess.PromptBudget,
		Reasoning:                 sess.ReasoningSetting,
		SharedSQLite:              contextWiring.SharedSQLite,
		ContextPreparationManager: contextWiring.Preparation,
		ContextPreparationInput:   contextWiring.PreparationInput,
		// Re-supplied on EVERY rebuild, against THIS surface's authority
		// registry. A delegated task agent's own MCP servers are merged
		// through it just before the child registry is scoped, and the
		// consumer fails OPEN when it is nil - so dropping it on a rebuild
		// silently stripped every later delegation of its MCP tools with no
		// error and no notice. Nil only when the session has no MCP manager,
		// which is the genuine "MCP disabled" case.
		EnsureMCPTools: mcpToolEnsurerFor(state, authority),
		SkillReg:       skillReg,
		SkillScope:     skillScope,
		AgentRegistry:  state.Registry,
		DeferredTools:  plan.Candidates,
		Session:        sess,
		// This session already handed out truncated-output refs against the
		// spool the live surface holds. Reuse it so the republication below is
		// an identity re-publish rather than a revocation.
		RemainderSpool: func() *remainder.Spool {
			if RemainderSpoolFromRegistryVar != nil {
				return RemainderSpoolFromRegistryVar(sess.Tools)
			}
			return nil
		}(),
	}
}
