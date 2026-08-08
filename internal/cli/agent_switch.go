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
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// classicAgentState is the root agent context for the classic REPL/one-shot
// chat path. The TUI stores the same pointer on tuiModel.agentState.
var classicAgentState *agentSessionState

// agentSessionState is the mid-session mutable agent context. Startup and
// /agent switch share this so model-switch rebuilds keep the selected agent.
type agentSessionState struct {
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
	SkillScope agentSkillScope
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
}

func (s *agentSessionState) context() agentSessionContext {
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
func (s *agentSessionState) ledgerRepo() ledger.LedgerRepository {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LedgerRepo
}

// setSkillScope stores the selected root agent's skill policy. Writers that
// already hold s.mu (applySessionAgent) assign the field directly.
func (s *agentSessionState) setSkillScope(scope agentSkillScope) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.SkillScope = scope
	s.mu.Unlock()
}

// skillScopeSnapshot returns a copy of the current root skill policy for the
// TUI slash path. A nil state or unset scope yields the open zero value.
func (s *agentSessionState) skillScopeSnapshot() agentSkillScope {
	if s == nil {
		return agentSkillScope{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SkillScope
}

// agentListRow is one selectable entry for the /agent dialog and listings.
type agentListRow struct {
	Name        string
	Description string
	Current     bool
}

// agentListRows builds ordered rows from a registry. Pure; unit-tested without TUI.
func agentListRows(reg *agents.AgentRegistry, current string) []agentListRow {
	if reg == nil {
		return nil
	}
	current = strings.TrimSpace(current)
	names := reg.Names()
	out := make([]agentListRow, 0, len(names))
	for _, name := range names {
		a, ok := reg.Get(name)
		if !ok {
			continue
		}
		out = append(out, agentListRow{
			Name:        a.Name,
			Description: a.Description,
			Current:     a.Name == current,
		})
	}
	return out
}

func currentAgentName(state *agentSessionState) string {
	if state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.Selected == nil {
		return ""
	}
	return state.Selected.Name
}

func formatAgentAvailable(reg *agents.AgentRegistry) string {
	if reg == nil || reg.Len() == 0 {
		return "(none)"
	}
	rows := make([]string, 0, reg.Len())
	for _, name := range reg.Names() {
		a, ok := reg.Get(name)
		if ok {
			rows = append(rows, name+"("+string(a.Provenance.Source)+")")
		}
	}
	return strings.Join(rows, ", ")
}

func formatAgentSet(name string) string {
	return "agent set to " + name
}

func formatAgentCurrent(name string, reg *agents.AgentRegistry) string {
	if name == "" {
		name = "(compiled default)"
	}
	return fmt.Sprintf("current agent=%s\nusage: /agent <name>\navailable: %s", name, formatAgentAvailable(reg))
}

// applySessionAgent switches the root agent for the idle session. busy is the
// TUI waiting flag; active turns and switch guards are checked on sess.
// It reuses ToolBase for re-scope and rebuilds the dispatcher like model switch.
func applySessionAgent(sess *chat.Session, res *config.Resolved, state *agentSessionState, name string, busy bool) error {
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
		sess.SetAgentSettings(prompt, maxSteps)
		return nil
	}
	prompt = promptWithDeferredIndex(prompt, state.TierPlan)
	if res != nil {
		res.SystemPrompt = prompt
	}
	// A new binding starts from its own core tier: admissions never carry
	// across an /agent switch (plan tools/05 D4).
	sess.ResetAdmissions()
	sess.PublishAgentSurface(prompt, maxSteps, candidate.registry, candidate.dispatcher, candidate.skillReg)
	sess.SetRemainderSpool(RemainderSpoolFromRegistry(candidate.registry))
	recordSchemaMassLocked(sess, state, candidate.plan, nil, sel.Name, "agent_switch")
	if state.TierPlan.Deferred() {
		sess.SetSurfaceWidener(newSurfaceWidener(sess, res, state))
		sess.SetAdmissionBinding(sel.Name, state.TierPlan.Digest)
	} else {
		sess.SetSurfaceWidener(nil)
		sess.SetAdmissionBinding("", "")
	}
	return nil
}

func selectedAgentSettings(selected *agents.ResolvedAgent, state *agentSessionState) (string, int) {
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
	skillScope   agentSkillScope
}

// commitTo installs a successfully built surface's derived state. Callers hold
// state.mu.
func (s *agentSurface) commitTo(state *agentSessionState) {
	state.TierPlan = s.plan
	state.SkillRegFull = s.skillRegFull
	state.SkillScope = s.skillScope
}

// buildAgentScopedSurface builds a fresh agent binding's surface: it loads
// skills from disk, freezes this binding's core/deferred tool split, and admits
// nothing. Every admission after this point reuses the frozen plan.
func buildAgentScopedSurface(sess *chat.Session, res *config.Resolved, state *agentSessionState, selected *agents.ResolvedAgent) (*agentSurface, error) {
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
func buildWidenedWith(sess *chat.Session, res *config.Resolved, state *agentSessionState, admitted []string) (*agentSurface, error) {
	if state.SkillRegFull == nil {
		return nil, fmt.Errorf("tool admission: no skill registry captured for this binding")
	}
	base := state.ToolBase.CloneForGenerationExcluding("ledger_read", "list_run_events", "read_output")
	return buildSurfaceFromBase(sess, res, state, surfaceBuildRequest{
		selected: state.Selected, base: base, skillReg: state.SkillRegFull,
		plan: state.TierPlan, admitted: admitted,
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
}

func buildSurfaceFromBase(sess *chat.Session, res *config.Resolved, state *agentSessionState, req surfaceBuildRequest) (*agentSurface, error) {
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
	cfg := config.SubagentConfig{}
	var modelCatalog []config.ProviderModelGroup
	if res != nil {
		cfg = res.Subagents
		modelCatalog = res.ModelCatalog()
	}
	contextWiring := contextDispatcherFor(sess, cfg)
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:          registry,
		AuthorityRegistry: authority,
		// The session owns the ledger store, so no rebuilt dispatcher opens one
		// it would then close on publication - under the spool this surface
		// carries. Callers hold state.mu, so the field is read directly.
		Repo:                      state.LedgerRepo,
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
		WorkspaceRoot:             root,
		MaxContextTokens:          sess.PromptBudget(),
		MaxTokens:                 sess.MaxTokens,
		Budget:                    sess.PromptBudget,
		Reasoning:                 sess.ReasoningSetting,
		SharedSQLite:              contextWiring.sharedSQLite,
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
	})
	if err != nil {
		return nil, fmt.Errorf("dispatcher: %w", err)
	}
	return &agentSurface{
		registry:     registry,
		dispatcher:   dispatcher,
		skillReg:     filterSkillsForScope(skillReg, skillScope),
		plan:         plan,
		skillRegFull: skillReg,
		skillScope:   skillScope,
	}, nil
}
