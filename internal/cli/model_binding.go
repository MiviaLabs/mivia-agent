package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// buildModelBinding prepares a complete provider/model generation without
// mutating the session. The caller publishes it through Session.SwitchBinding.
//
// state is the live agent session state, not a snapshot of it: a model switch
// rebuilds the whole surface, and the surface's authority registry, frozen tier
// split and skill registry exist only there. Passing a context snapshot is what
// made a /model switch silently narrow this session's authority. In-flight
// turns keep their captured binding generation.
func buildModelBinding(sess *chat.Session, res *config.Resolved, root, providerName, model string, state *agentSessionState) (chat.ModelBinding, error) {
	if sess == nil || res == nil {
		return chat.ModelBinding{}, fmt.Errorf("model binding requires a session and config")
	}
	agentCtx := state.context()
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	model, err := config.NormalizeModelName(model)
	if err != nil {
		return chat.ModelBinding{}, err
	}
	profile, selectable := configuredProfile(res, providerName, model)
	if !selectable {
		return chat.ModelBinding{}, fmt.Errorf("model is not selectable for provider %s", providerName)
	}
	comp, err := newProviderCompleter(res, providerName, model)
	if err != nil {
		return chat.ModelBinding{}, err
	}
	binding := chat.ModelBinding{ProviderName: providerName, Model: model, Completer: comp, Profile: profile}
	binding.PromptBudgetTokens = sess.PromptBudgetFor(profile)
	if root == "" {
		root = "."
	}
	skillReg, warnings, err := loadSessionSkills(root, agentCtx.AllowProjectSkills)
	if err != nil {
		return chat.ModelBinding{}, fmt.Errorf("load skills: %w", err)
	}
	warnSkillLoad(warnings)
	// Defense in depth: load already omitted project sources when gate is off.
	skillReg = filterSkillRegistryForGate(skillReg, agentCtx.AllowProjectSkills)
	skillScope := skillScopeFromAgent(agentCtx.Selected)
	// The binding powers direct slash turns, so it is limited to the selected
	// root agent. The dispatcher receives the full registry for task-agent
	// routing and enforces each selected task agent's policy independently.
	binding.SkillRegistry = filterSkillsForScope(skillReg, skillScope)
	toolBase, toolResultCap, surfaceGeneration := sess.AgentSurfaceSnapshot()
	binding.AgentSurfaceGeneration = surfaceGeneration
	if toolBase == nil {
		return binding, nil
	}
	// Preferred path: rebuild through the same factory /agent and tool
	// admission use. sess.Tools alone cannot reconstruct this session's
	// authority set, deferred tier or admitted tail, so a binding built from it
	// silently narrows delegation authority and orphans load_tools.
	built, err := modelSwitchSurface(sess, res, state, binding, skillReg)
	if err == nil && built == nil {
		// No captured agent surface: a plain generation clone is correct, and
		// it publishes no registry because there is no tiered surface to swap.
		built, err = unscopedModelSurface(sess, res, root, binding, toolBase, toolResultCap, agentCtx, skillReg, state.ledgerRepo())
	}
	if err != nil {
		return chat.ModelBinding{}, err
	}
	binding.Registry = built.registry
	binding.Dispatcher = built.dispatcher
	if built.skillReg != nil {
		binding.SkillRegistry = built.skillReg
	}
	return binding, nil
}

// unscopedModelSurface is the no-agent-surface fallback: it clones the
// current tools for a new generation and builds a dispatcher over them. It is
// only correct because a session with no captured agent surface also defers
// nothing, so the advertised registry IS the authority registry here.
func unscopedModelSurface(sess *chat.Session, res *config.Resolved, root string, binding chat.ModelBinding, toolBase *tools.Registry, toolResultCap int, agentCtx agentSessionContext, skillReg *skills.Registry, repo ledger.LedgerRepository) (*agentSurface, error) {
	// Start from a generation clone of the current (already agent-scoped) tools
	// so the new dispatcher cannot regain excluded tools.
	toolGeneration := toolBase.CloneForGenerationExcluding("ledger_read", "list_run_events", "read_output")
	contextWiring := contextDispatcherFor(sess, res.Subagents)
	// Rebuild the skill policy against the live generation (plan 43) so a
	// skill requiring a disabled/denied tool cannot activate after a switch.
	liveScope := skillScopeFromAgentAndRegistry(agentCtx.Selected, toolGeneration)
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry: toolGeneration,
		// Session-owned; see agentSessionState.LedgerRepo. Nil here is the
		// hand-built caller with no agent state, which owns no session either.
		Repo:                      repo,
		Completer:                 binding.Completer,
		Model:                     binding.Model,
		ProviderName:              binding.ProviderName,
		ModelGeneration:           sess.CurrentModelGeneration() + 1,
		ModelGenerationFunc:       sess.CurrentModelGeneration,
		ModelCatalog:              res.ModelCatalog(),
		CompleterFactory:          newProviderCompleterFactory(res),
		Config:                    res.Subagents,
		ToolResultCapBytes:        toolResultCap,
		WorkspaceRoot:             root,
		MaxContextTokens:          binding.PromptBudgetTokens,
		MaxTokens:                 res.MaxTokens,
		Budget:                    sess.PromptBudget,
		SharedSQLite:              contextWiring.sharedSQLite,
		ContextPreparationManager: contextWiring.preparation,
		ContextPreparationInput:   contextWiring.preparationInput,
		SkillReg:                  skillReg,
		SkillScope:                liveScope,
		AgentRegistry:             agentCtx.Registry,
		// The session keeps its truncated-output grants across the rebuild.
		RemainderSpool: RemainderSpoolFromRegistry(sess.Tools),
	})
	if err != nil {
		return nil, fmt.Errorf("dispatcher: %w", err)
	}
	return &agentSurface{dispatcher: dispatcher}, nil
}

// modelSwitchSurface rebuilds the live agent surface for a binding that is not
// published yet, so a model switch keeps everything the surface owns: the full
// authority registry, the frozen tier split (with load_tools still invocable),
// the admitted tail and the session's remainder spool.
//
// It returns (nil, nil) when there is no captured agent surface to rebuild
// from - tools-off sessions and callers with no agent state - which is exactly
// the case where nothing is deferred and a plain generation clone is already
// the correct answer.
func modelSwitchSurface(sess *chat.Session, res *config.Resolved, state *agentSessionState, binding chat.ModelBinding, skillReg *skills.Registry) (*agentSurface, error) {
	if state == nil {
		return nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.ToolBase == nil {
		return nil, nil
	}
	// The skill registry is frozen for the life of the agent binding, exactly
	// like the tier plan: skill re-discovery is /agent's job. Reuse the binding's
	// registry rather than the caller's fresh disk load, which is what
	// buildWidenedWith does for a tool admission. Anything else diverges - a
	// build-time commit is not the alternative, because SwitchBinding may still
	// refuse this binding and derived state must never outlive a refused switch.
	if state.SkillRegFull != nil {
		skillReg = state.SkillRegFull
	}
	// The dispatcher is built for the generation this binding will become, the
	// same value SwitchBinding assigns when it publishes.
	binding.ModelGeneration = sess.CurrentModelGeneration() + 1
	base := state.ToolBase.CloneForGenerationExcluding("ledger_read", "list_run_events", "read_output")
	return buildSurfaceFromBase(sess, res, state, surfaceBuildRequest{
		selected: state.Selected,
		base:     base,
		skillReg: skillReg,
		// The tier plan is frozen for the life of the agent binding: a model
		// switch is not a new binding, so it must not re-tier. Carrying the
		// admitted set keeps loaded tools loaded across the switch.
		plan:     state.TierPlan,
		admitted: sess.AdmittedTools(),
		binding:  &binding,
	})
}

// newProviderCompleter constructs a completer for one provider-qualified
// model. provider.NewForProvider is fail-closed: it rejects a provider with no
// configured runtime and one with no credential, both before any client is
// built, so this is the point where an agent's declared provider is really
// authorized (the parse-time name check is only a spelling check).
func newProviderCompleter(res *config.Resolved, providerName, model string) (provider.Completer, error) {
	if res == nil {
		return nil, fmt.Errorf("no resolved configuration to construct provider %q", providerName)
	}
	resCopy := *res
	resCopy.ProviderName = providerName
	resCopy.Model = model
	return provider.NewForProvider(&resCopy, providerName)
}

// newProviderCompleterFactory adapts newProviderCompleter to the dispatcher's
// factory seam. It returns nil when there is no configuration to build from,
// which leaves a routed agent's foreign provider failing closed rather than
// falling back to the session's completer.
func newProviderCompleterFactory(res *config.Resolved) func(string, string) (provider.Completer, error) {
	if res == nil {
		return nil
	}
	return func(providerName, model string) (provider.Completer, error) {
		return newProviderCompleter(res, providerName, model)
	}
}

// loadSessionSkills loads skill handlers for the session. When allowProject is
// false, only user skills are discovered so a workspace skill cannot shadow a
// user skill and then be stripped by the gate (leaving nothing).
func loadSessionSkills(root string, allowProject bool) (*skills.Registry, []string, error) {
	sources := []skills.Source{
		{Dir: workspace.UserSkillsDir(), Origin: skills.OriginUser},
	}
	if allowProject {
		if strings.TrimSpace(root) == "" {
			root = "."
		}
		sources = append(sources, skills.Source{
			Dir: workspace.SkillsDir(root), Origin: skills.OriginProject,
		})
	}
	return skills.LoadMarkdownSources(sources, skills.LoadOptions{
		ReservedNames: reservedSkillNames(), ReservedSlashTokens: reservedSlashTokens(),
	})
}

func reservedSkillNames() map[string]struct{} {
	return map[string]struct{}{
		handlerDelegate: {}, handlerOneshot: {}, handlerMultiStep: {},
	}
}

func reservedSlashTokens() map[string]struct{} {
	reserved := make(map[string]struct{})
	for _, command := range builtInSlashCommands() {
		reserved[command.Name] = struct{}{}
		for _, alias := range command.Aliases {
			reserved[alias] = struct{}{}
		}
	}
	return reserved
}

func warnSkillLoad(warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", warning)
	}
}

func configuredProfile(res *config.Resolved, providerName, model string) (config.ModelSpec, bool) {
	for _, group := range res.ModelCatalog() {
		if group.Provider != providerName || !group.Selectable {
			continue
		}
		for _, profile := range group.Models {
			if profile.Name == model {
				return profile, true
			}
		}
	}
	// Hand-built test configurations predate the catalog. Keep the active
	// provider projection usable while production-loaded configs stay strict.
	if providerName == res.ProviderName && res.AllowsModel(model) {
		return config.ModelSpec{Name: model, ContextWindowTokens: chat.DefaultMaxContextTokens}, true
	}
	return config.ModelSpec{}, false
}

func switchModelCommand(sess *chat.Session, res *config.Resolved, providerName, model string) error {
	if binding, prepared, err := sess.PrepareBinding(providerName, model); prepared {
		if err != nil {
			return err
		}
		return sess.SwitchBinding(binding)
	}
	selection := sess.CurrentSelection()
	// res may be nil on TUI paths constructed without config (tests and some
	// boot paths). Guard before reading ProviderRuntimes - matches the old
	// TUI switchModel nil check that this function absorbed.
	if providerName == selection.ProviderName && res != nil && len(res.ProviderRuntimes) == 0 {
		binding := sess.CurrentBinding()
		if binding.Completer == nil {
			if !sess.SelectModel(model) {
				return fmt.Errorf("model is not configured")
			}
			return nil
		}
		binding.Model = model
		binding.Profile.Name = model
		if binding.Profile.ContextWindowTokens <= 0 {
			binding.Profile.ContextWindowTokens = chat.DefaultMaxContextTokens
		}
		return sess.SwitchBinding(binding)
	}
	// Model switch without a captured agent context uses gate-default (no
	// project skills stripped only when the session already filtered tools).
	binding, err := buildModelBinding(sess, res, ".", providerName, model, &agentSessionState{
		AllowProjectSkills: true,
	})
	if err != nil {
		return err
	}
	return sess.SwitchBinding(binding)
}

// chatBindingFactory is the single provider/model construction path every
// surface rebuild goes through: the REPL and TUI model switches, and a catalog
// load whose saved session names a different provider or model. It closes over
// the live agentSessionState so each rebuild sees the current agent, tier plan
// and admitted set rather than a snapshot taken at startup.
func chatBindingFactory(sess *chat.Session, res *config.Resolved, root string, state *agentSessionState) func(string, string) (chat.ModelBinding, error) {
	return func(providerName, model string) (chat.ModelBinding, error) {
		return buildModelBinding(sess, res, root, providerName, model, state)
	}
}
