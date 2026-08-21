package cli

import (
	"fmt"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// classicAgentState is the root agent context for the classic REPL/one-shot
// chat path. The TUI stores the same pointer on TUIModel.agentState.
var classicAgentState *AgentSessionState

// AgentSessionState is the mid-session mutable agent context. Startup and
// /agent switch share this so model-switch rebuilds keep the selected agent.
type AgentSessionState struct {
	mu                 sync.Mutex
	Global             config.AgentsGlobal
	Selected           *agents.ResolvedAgent
	AllowProjectSkills bool
	Registry           *agents.AgentRegistry
	WorkspaceRoot      string
	// ToolBase is the post-dispatcher, pre-scope registry for re-scoping.
	// Nil when tools are off.
	ToolBase *tools.Registry
	// MCPManager owns the session-wide MCP clients. Agent switches borrow it to
	// discover newly selected server tools without starting another client.
	MCPManager *mcp.Manager
	// SkillScope is the immutable per-instance skill policy for the selected
	// root agent, including the final live tool registry snapshot (plan 43).
	// Set at dispatcher attach and agent switch; read by the TUI slash path.
	SkillScope AgentSkillScope
	// TierPlan is the frozen core/deferred tool split for the current agent
	// binding (plan tools/05 D8). Computed once per binding; never recomputed
	// while it lives, so the prompt index it feeds stays byte-stable.
	TierPlan toolTierPlan
	// SkillRegFull is the current binding's unfiltered skill registry. Surface
	// widening reuses it so admitting a tool performs no skill disk I/O.
	SkillRegFull *skills.Registry
	// LedgerRepo is the session-lifetime ledger repository every surface rebuild
	// passes to NewSessionDispatcher. It exists so no dispatcher ever OWNS a
	// ledger store: a republished surface carries the live remainder spool, the
	// spool captured its ContentStore at construction, and publication closes
	// the dispatcher it replaced. A per-dispatcher store would therefore be
	// closed out from under the spool by the first tool admission. Nil means the
	// caller supplied a shared store (the dispatcher borrows it) or tools are off.
	LedgerRepo ledger.LedgerRepository
	// ownedLedgerStore is the durable repository this session opened and must
	// close at cleanup. Nil when LedgerRepo is the process-wide memory default.
	ownedLedgerStore *ledger.StorageLedgerRepository
	// LastSchemaMass is the most recent advertised schema-mass measurement for
	// this session's surface (plan tools/05 D5 telemetry). It is written by the
	// three publications that can change the split or the admitted tail: attach,
	// /agent switch and tool admission. A /model rebuild republishes the same
	// frozen tiers with the same admitted tail, so it deliberately leaves this
	// measurement alone rather than re-emitting an identical one.
	LastSchemaMass   schemaMass
	BaselinePrompt   string
	BaselineMaxSteps int
	BaselineCaptured bool
	// Memory is the session-lifetime memory store, opened once by
	// configureChatWorkspace and never closed here - the same store
	// tools.DefaultOptions.Memory wires into memory_save/memory_search (plan
	// 77, E1). Nil when memory is disabled or tools are off. Callers hold
	// state.mu (applySessionAgent does), so the field is read directly,
	// matching the LedgerRepo convention above.
	Memory memory.Store
	// MemoryConfig is the resolved [memory] section, read alongside Memory
	// to build the core-tier injection block (coreMemoryBlock).
	MemoryConfig config.MemoryConfig
}

// DisplayName is the status dialog's "agent" row: the locked, nil-safe read
// of the currently selected agent's name. Exported for internal/legacytui's
// dialog rendering, which cannot lock the unexported mu field directly.
func (s *AgentSessionState) DisplayName() string {
	if s == nil {
		return "root fallback"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Selected == nil {
		return "root fallback"
	}
	return s.Selected.Name
}

// DisplaySource is the status dialog's "source" row: the locked, nil-safe
// read of the currently selected agent's provenance source. Exported for
// internal/legacytui's dialog rendering, which cannot lock the unexported mu
// field directly.
func (s *AgentSessionState) DisplaySource() string {
	if s == nil {
		return "compiled"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Selected == nil {
		return "compiled"
	}
	return string(s.Selected.Provenance.Source)
}

func (s *AgentSessionState) context() agentSessionContext {
	if s == nil {
		return agentSessionContext{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return agentSessionContext{
		Global:             s.Global,
		Selected:           s.Selected,
		Registry:           s.Registry,
		AllowProjectSkills: s.AllowProjectSkills,
	}
}

// ledgerRepo is the session-owned ledger repository for callers that do NOT
// hold s.mu. Surface builds read the field directly under the lock.
func (s *AgentSessionState) ledgerRepo() ledger.LedgerRepository {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LedgerRepo
}

// memoryStore and memoryConfig mirror ledgerRepo for callers that do NOT
// hold s.mu (plan 77, E1/E2).
func (s *AgentSessionState) memoryStore() memory.Store {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Memory
}

func (s *AgentSessionState) memoryConfig() config.MemoryConfig {
	if s == nil {
		return config.MemoryConfig{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.MemoryConfig
}

// setSkillScope stores the selected root agent's skill policy. Writers that
// already hold s.mu (applySessionAgent) assign the field directly.
func (s *AgentSessionState) setSkillScope(scope AgentSkillScope) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.SkillScope = scope
	s.mu.Unlock()
}

// SkillScopeSnapshot returns a copy of the current root skill policy for the
// TUI slash path. A nil state or unset scope yields the open zero value.
func (s *AgentSessionState) SkillScopeSnapshot() AgentSkillScope {
	if s == nil {
		return AgentSkillScope{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SkillScope
}

// AgentListRow is one selectable entry for the /agent dialog and listings.
type AgentListRow struct {
	Name        string
	Description string
	Current     bool
}

// ApplySessionAgent switches the root agent for the idle session. busy is the
// TUI waiting flag; active turns and switch guards are checked on sess.
// It reuses ToolBase for re-scope and rebuilds the dispatcher like model switch.
func ApplySessionAgent(sess *chat.Session, res *config.Resolved, state *AgentSessionState, name string, busy bool) error {
	if sess == nil || state == nil {
		return fmt.Errorf("agent switch requires a session and agent state")
	}
	// Selection and all session-owned surfaces are one logical transaction.
	// Candidate construction is intentionally inside this lock so two /agent
	// requests cannot publish different surfaces under one selected name.
	state.mu.Lock()
	defer state.mu.Unlock()
	if busy {
		return fmt.Errorf("finish current work first")
	}
	release, err := sess.BeginSurfaceSwitch()
	if err != nil {
		return err
	}
	defer release()
	if err := sess.CheckSwitchAllowed(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("agent name is empty")
	}
	if state.Registry == nil {
		return fmt.Errorf("no agents loaded")
	}
	selected, err := agents.Select(state.Registry, name)
	if err != nil {
		return err
	}
	if err := ensureSelectedMCPTools(state, selected); err != nil {
		return fmt.Errorf("MCP tools: %w", err)
	}
	if !state.BaselineCaptured {
		state.BaselinePrompt, state.BaselineMaxSteps = sess.AgentSettings()
		state.BaselineCaptured = true
	}
	prompt, maxSteps := selectedAgentSettings(&selected, state)
	var candidate *agentSurface
	if sess.Tools != nil && state.ToolBase != nil {
		candidate, err = buildAgentScopedSurface(sess, res, state, &selected)
		if err != nil {
			return err
		}
		warnDisabledAgentTools(&selected, disabledForAgent(&selected, state.ToolBase))
		warnAdvertisedToolsTruncated(&selected, candidate.advertisedDropped)
	}
	// Commit selection and every session-owned surface only after all candidate
	// construction and validation has succeeded.
	sel := selected
	state.Selected = &sel
	if candidate != nil {
		candidate.commitTo(state)
	}
	if candidate == nil {
		if res != nil {
			res.SystemPrompt = prompt
		}
		sess.SetAgentSettings(prompt, maxSteps, coreMemoryBlockForState(state))
		return nil
	}
	prompt = promptWithDeferredIndex(prompt, state.TierPlan)
	if res != nil {
		res.SystemPrompt = prompt
	}
	commitAgentSwitchSurface(sess, res, state, candidate, sel.Name, prompt, maxSteps)
	return nil
}

// commitAgentSwitchSurface publishes a successfully built agent-switch
// candidate and wires its admission state. A new binding starts from its own
// core tier: admissions never carry across an /agent switch (plan tools/05
// D4).
func commitAgentSwitchSurface(sess *chat.Session, res *config.Resolved, state *AgentSessionState, candidate *agentSurface, agentName, prompt string, maxSteps int) {
	sess.ResetAdmissions()
	sess.PublishAgentSurface(prompt, maxSteps, candidate.registry, candidate.dispatcher, candidate.skillReg, coreMemoryBlockForState(state), candidate.advertised)
	sess.SetRemainderSpool(RemainderSpoolFromRegistry(candidate.registry))
	recordSchemaMassLocked(sess, state, candidate.plan, nil, agentName, "agent_switch")
	if state.TierPlan.Deferred() {
		sess.SetSurfaceWidener(newSurfaceWidener(sess, res, state))
		sess.SetAdmissionBinding(agentName, state.TierPlan.Digest)
	} else {
		sess.SetSurfaceWidener(nil)
		sess.SetAdmissionBinding("", "")
	}
}

func selectedAgentSettings(selected *agents.ResolvedAgent, state *AgentSessionState) (string, int) {
	if selected != nil && strings.TrimSpace(selected.SystemPrompt) != "" {
		return selected.SystemPrompt, selectedMaxTurns(selected, state.BaselineMaxSteps)
	}
	if selected != nil && selected.MaxTurns != nil {
		return state.BaselinePrompt, *selected.MaxTurns
	}
	return state.BaselinePrompt, state.BaselineMaxSteps
}

func selectedMaxTurns(selected *agents.ResolvedAgent, baseline int) int {
	if selected != nil && selected.MaxTurns != nil {
		return *selected.MaxTurns
	}
	return baseline
}

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
	plan         toolTierPlan
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
	skillReg, warnings, err := loadSessionSkills(root, state.AllowProjectSkills)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	warnSkillLoad(warnings)
	skillReg = filterSkillRegistryForGate(skillReg, state.AllowProjectSkills)
	base := state.ToolBase.CloneForGenerationExcluding("ledger_read", "list_run_events", "read_output")
	plan := planToolTiers(base, selected, res)
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
	base := state.ToolBase.CloneForGenerationExcluding("ledger_read", "list_run_events", "read_output")
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

// surfaceBuildRequest is one surface build's inputs. binding is the only
// optional field: it overrides the live session binding for a generation that
// is built but not yet published, which is what a model switch is.
type surfaceBuildRequest struct {
	selected *agents.ResolvedAgent
	base     *tools.Registry
	skillReg *skills.Registry
	plan     toolTierPlan
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
	root := state.WorkspaceRoot
	if root == "" {
		root = "."
	}
	// Start from the pre-scope base so switching to a wider agent regains tools.
	// Apply root agent scope BEFORE building the dispatcher so the dispatcher
	// captures a scoped registry. This keeps the dispatcher and sess.Tools in
	// agreement (INV-AG-29 execution denial).
	registry := tieredRootRegistry(base, selected, state.Global.MandatoryToolDenylistAdditions, plan, admitted)
	// Authority is the root agent's whole authorized set, deferred tier
	// included: the tier split decides what the root model is shown, never what
	// this session may delegate. The skill policy and every nested handler read
	// authority; only the model-facing surface reads registry.
	authority, _ := scopedRootRegistry(base, selected, state.Global.MandatoryToolDenylistAdditions)
	// The skill policy is built against the final live authority registry
	// (plan 43) and returned for the caller to install on commit.
	skillScope := skillScopeFromAgentAndRegistry(selected, authority)
	dispatcher, err := NewSessionDispatcher(dispatcherOptsForSurface(sess, res, state, binding, registry, authority, skillReg, skillScope, plan, root))
	if err != nil {
		return nil, fmt.Errorf("dispatcher: %w", err)
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
		advertised, advertisedDropped = advertisedToolSpecs(base, plan)
	}
	return &agentSurface{
		registry:          registry,
		dispatcher:        dispatcher,
		skillReg:          filterSkillsForScope(skillReg, skillScope),
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
func dispatcherOptsForSurface(sess *chat.Session, res *config.Resolved, state *AgentSessionState, binding chat.ModelBinding, registry, authority *tools.Registry, skillReg *skills.Registry, skillScope AgentSkillScope, plan toolTierPlan, root string) SessionDispatcherOpts {
	cfg := config.SubagentConfig{}
	var modelCatalog []config.ProviderModelGroup
	if res != nil {
		cfg = res.Subagents
		modelCatalog = res.ModelCatalog()
	}
	contextWiring := contextDispatcherFor(sess, cfg)
	return SessionDispatcherOpts{
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
		CompleterFactory:          newProviderCompleterFactory(res),
		Config:                    cfg,
		ToolResultCapBytes:        sess.MaxToolResultChars,
		BatchResultBudgetBytes:    sess.BatchResultBudgetBytes,
		RefOnlyTools:              sess.RefOnlyTools,
		WorkspaceRoot:             root,
		MaxContextTokens:          sess.PromptBudget(),
		MaxTokens:                 sess.MaxTokens,
		Budget:                    sess.PromptBudget,
		Reasoning:                 sess.ReasoningSetting,
		SharedSQLite:              contextWiring.SharedSQLite,
		ContextPreparationManager: contextWiring.preparation,
		ContextPreparationInput:   contextWiring.preparationInput,
		SkillReg:                  skillReg,
		SkillScope:                skillScope,
		AgentRegistry:             state.Registry,
		DeferredTools:             plan.Candidates,
		Session:                   sess,
		// This session already handed out truncated-output refs against the
		// spool the live surface holds. Reuse it so the republication below is
		// an identity re-publish rather than a revocation.
		RemainderSpool: RemainderSpoolFromRegistry(sess.Tools),
	}
}
