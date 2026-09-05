package uiadapter

// Model selection: split from runner.go to keep it under the go-structure
// soft cap. Provider/model resolution, catalog grouping, and /model switch.

import (
	"context"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func (r *CommandRunner) handleModel(args string) ports.CommandOutcome {
	if r.activeSession() == nil || r.res == nil {
		return ports.CommandOutcome{Err: "session or configuration not initialized"}
	}
	if args != "" {
		return r.SelectModel(context.Background(), args)
	}
	groups := r.availableModelsByProvider()
	if len(groups) == 0 {
		return ports.CommandOutcome{Err: "no models loaded"}
	}
	return ports.CommandOutcome{ModelChoiceGroups: groups}
}

// availableModelsByProvider returns the selectable catalog grouped by
// provider, in catalog order. The first group's provider name is the
// currently selected provider; later groups keep their catalog order
// so the picker stays stable across re-opens. An empty catalog
// falls back to the session's current model in a single flat group
// with no provider header.
func (r *CommandRunner) availableModelsByProvider() []ports.ModelChoiceGroup {
	if r.res == nil {
		return nil
	}
	var groups []ports.ModelChoiceGroup
	for _, group := range r.res.ModelCatalog() {
		if !group.Selectable {
			continue
		}
		names := make([]string, 0, len(group.Models))
		for _, m := range group.Models {
			names = append(names, m.Name)
		}
		if len(names) == 0 {
			continue
		}
		groups = append(groups, ports.ModelChoiceGroup{
			Provider: group.Provider,
			Models:   names,
		})
	}
	sess := r.activeSession()
	if len(groups) == 0 && sess != nil {
		if cur := sess.CurrentModel(); cur != "" {
			return []ports.ModelChoiceGroup{{Models: []string{cur}}}
		}
	}
	return groups
}

func resolveProviderAndModel(res *config.Resolved, selProvider, name string) (string, string) {
	name = strings.TrimSpace(name)
	providerName := res.ProviderName
	if selProvider != "" {
		providerName = selProvider
	}

	// 1. Explicit provider prefix matching a catalog provider
	for _, group := range res.ModelCatalog() {
		prefix := group.Provider + "/"
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			return group.Provider, name[len(prefix):]
		}
	}

	// 1b. Prefix matching a configured provider runtime
	if res.ProviderRuntimes != nil {
		for p := range res.ProviderRuntimes {
			prefix := strings.ToLower(p) + "/"
			if strings.HasPrefix(strings.ToLower(name), prefix) {
				return p, name[len(prefix):]
			}
		}
	}

	// 1c. Name containing a slash matching known provider name
	if p, m, ok := strings.Cut(name, "/"); ok && p != "" && m != "" {
		for _, group := range res.ModelCatalog() {
			if strings.EqualFold(group.Provider, p) {
				return group.Provider, m
			}
		}
		if res.ProviderRuntimes != nil {
			for rName := range res.ProviderRuntimes {
				if strings.EqualFold(rName, p) {
					return rName, m
				}
			}
		}
	}

	// 2. Search unique provider in catalog. A name matching more than one
	// Selectable provider is NOT resolved here - silently picking the first
	// catalog-order match would be an unannounced provider switch (different
	// auth, base URL, and wire behavior) on nothing but name coincidence,
	// the exact class of surprise a same-named model across providers (e.g.
	// "claude-sonnet-5" under both an OpenAI-compatible proxy and the native
	// anthropic provider) causes. Falling through here leaves providerName
	// as today's default (current selection), and SwitchModelCommand's
	// resulting "not available" error carries the ambiguity via
	// res.OtherProvidersWithModel in SelectModel's error path below - naming
	// every match so the user picks explicitly with /model <provider> <name>
	// rather than the tool guessing for them.
	var matchedProvider string
	matches := 0
	for _, group := range res.ModelCatalog() {
		if !group.Selectable {
			continue
		}
		for _, m := range group.Models {
			if m.Name == name {
				matchedProvider = group.Provider
				matches++
				break
			}
		}
	}
	if matches == 1 {
		return matchedProvider, name
	}

	return providerName, name
}

// SelectModel switches the session's active model.
func (r *CommandRunner) SelectModel(_ context.Context, name string) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil || r.res == nil {
		return ports.CommandOutcome{Err: "session or configuration not initialized"}
	}
	selProvider := ""
	if sel := sess.CurrentSelection(); sel.ProviderName != "" {
		selProvider = sel.ProviderName
	}
	providerName, modelName := resolveProviderAndModel(r.res, selProvider, name)

	discarded, err := cliagents.SwitchModelCommand(sess, r.res, providerName, modelName)
	if err != nil {
		msg := fmt.Sprintf("failed to switch model to %q (%s): %v", modelName, providerName, err)
		if others := r.res.OtherProvidersWithModel(providerName, modelName); len(others) == 1 {
			msg += fmt.Sprintf(" (found under provider %s - run /model %s %s to switch)", others[0], others[0], modelName)
		} else if len(others) > 1 {
			msg += fmt.Sprintf(" (found under providers: %s - run /model <provider> %s to switch)", strings.Join(others, ", "), modelName)
		}
		return ports.CommandOutcome{Err: msg}
	}
	r.res.ProviderName = providerName
	r.res.Model = modelName
	notice := fmt.Sprintf("Model set to %s (%s).", modelName, providerName)
	if discarded != "" {
		notice += fmt.Sprintf(" (Reasoning effort override %q discarded).", discarded)
	}
	return ports.CommandOutcome{Notice: notice}
}
