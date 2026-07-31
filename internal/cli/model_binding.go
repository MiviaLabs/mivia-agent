package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// buildModelBinding prepares a complete provider/model generation without
// mutating the session. The caller publishes it through Session.SwitchBinding.
func buildModelBinding(sess *chat.Session, res *config.Resolved, root, providerName, model string) (chat.ModelBinding, error) {
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
	if sess.Tools == nil {
		return binding, nil
	}
	if root == "" {
		root = "."
	}
	toolGeneration := sess.Tools.CloneForGeneration()
	skillReg, err := skills.LoadMarkdown(workspace.SkillsDir(root), comp, model)
	if err != nil {
		return chat.ModelBinding{}, fmt.Errorf("load skills: %w", err)
	}
	dispatcher, err := NewSessionDispatcherWithContext(toolGeneration, comp, model, res.Subagents, sess.MaxToolResultChars, binding.PromptBudgetTokens, res.MaxTokens, skillReg)
	if err != nil {
		return chat.ModelBinding{}, fmt.Errorf("dispatcher: %w", err)
	}
	binding.Dispatcher = dispatcher
	return binding, nil
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
	selection := sess.CurrentSelection()
	if providerName == selection.ProviderName && len(res.ProviderRuntimes) == 0 {
		if !sess.SelectModel(model) {
			return fmt.Errorf("model is not configured")
		}
		return nil
	}
	if binding, prepared, err := sess.PrepareBinding(providerName, model); prepared {
		if err != nil {
			return err
		}
		return sess.SwitchBinding(binding)
	}
	binding, err := buildModelBinding(sess, res, ".", providerName, model)
	if err != nil {
		return err
	}
	return sess.SwitchBinding(binding)
}
