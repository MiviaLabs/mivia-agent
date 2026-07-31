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
	resCopy := *res
	resCopy.ProviderName = providerName
	resCopy.Model = model
	comp, err := provider.NewForProvider(&resCopy, providerName)
	if err != nil {
		return chat.ModelBinding{}, err
	}
	binding := chat.ModelBinding{ProviderName: providerName, Model: model, Completer: comp, Profile: profile}
	binding.PromptBudgetTokens = sess.PromptBudgetFor(profile)
	if root == "" {
		root = "."
	}
	skillReg, warnings, err := loadSessionSkills(root)
	if err != nil {
		return chat.ModelBinding{}, fmt.Errorf("load skills: %w", err)
	}
	warnSkillLoad(warnings)
	skillReg = filterSkillRegistryForGate(skillReg, agentCtx.AllowProjectSkills)
	binding.SkillRegistry = skillReg
	if sess.Tools == nil {
		return binding, nil
	}
	// Start from a generation clone of the current (already agent-scoped) tools
	// so the new dispatcher cannot regain excluded tools.
	toolGeneration := sess.Tools.CloneForGenerationExcluding("ledger_read", "list_run_events")
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:           toolGeneration,
		Completer:          comp,
		Model:              model,
		Config:             res.Subagents,
		ToolResultCapBytes: sess.MaxToolResultChars,
		MaxContextTokens:   binding.PromptBudgetTokens,
		MaxTokens:          res.MaxTokens,
		Budget:             sess.PromptBudget,
		SkillReg:           skillReg,
	})
	if err != nil {
		return chat.ModelBinding{}, fmt.Errorf("dispatcher: %w", err)
	}
	binding.Dispatcher = dispatcher
	return binding, nil
}

func loadSessionSkills(root string) (*skills.Registry, []string, error) {
	return skills.LoadMarkdownSources([]skills.Source{
		{Dir: workspace.UserSkillsDir(), Origin: skills.OriginUser},
		{Dir: workspace.SkillsDir(root), Origin: skills.OriginProject},
	}, skills.LoadOptions{ReservedNames: reservedSkillNames(), ReservedSlashTokens: reservedSlashTokens()})
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
	// boot paths). Guard before reading ProviderRuntimes — matches the old
	// TUI switchModel nil check that this function absorbed.
	if providerName == selection.ProviderName && res != nil && len(res.ProviderRuntimes) == 0 {
		if !sess.SelectModel(model) {
			return fmt.Errorf("model is not configured")
		}
		return nil
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
