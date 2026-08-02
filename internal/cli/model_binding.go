package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// buildModelBinding prepares a complete provider/model generation without
// mutating the session. The caller publishes it through Session.SwitchBinding.
// agentCtx is the same agent-aware context used at startup so scope and skill
// gating survive dispatcher rebuilds; in-flight turns keep their captured
// binding generation.
func buildModelBinding(sess *chat.Session, res *config.Resolved, root, providerName, model string, agentCtx agentSessionContext) (chat.ModelBinding, error) {
	if sess == nil || res == nil {
		return chat.ModelBinding{}, fmt.Errorf("model binding requires a session and config")
	}
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
	// Start from a generation clone of the current (already agent-scoped) tools
	// so the new dispatcher cannot regain excluded tools.
	toolGeneration := toolBase.CloneForGenerationExcluding("ledger_read", "list_run_events", "read_output")
	contextWiring := contextDispatcherFor(sess, res.Subagents)
	// Rebuild the skill policy against the live generation (plan 43) so a
	// skill requiring a disabled/denied tool cannot activate after a switch.
	liveScope := skillScopeFromAgentAndRegistry(agentCtx.Selected, toolGeneration)
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:                  toolGeneration,
		Completer:                 comp,
		Model:                     model,
		ProviderName:              providerName,
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
	})
	if err != nil {
		return chat.ModelBinding{}, fmt.Errorf("dispatcher: %w", err)
	}
	binding.Dispatcher = dispatcher
	return binding, nil
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
		// This path rewrites the profile in place instead of resolving a new
		// one, so anything model-specific left behind belongs to the PREVIOUS
		// model. RenameModel is the single door every in-place rename uses.
		binding.RenameModel(model)
		if binding.Profile.ContextWindowTokens <= 0 {
			binding.Profile.ContextWindowTokens = chat.DefaultMaxContextTokens
		}
		return sess.SwitchBinding(binding)
	}
	// Model switch without a captured agent context uses gate-default (no
	// project skills stripped only when the session already filtered tools).
	binding, err := buildModelBinding(sess, res, ".", providerName, model, agentSessionContext{
		AllowProjectSkills: true,
	})
	if err != nil {
		return err
	}
	return sess.SwitchBinding(binding)
}
