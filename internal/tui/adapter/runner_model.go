package adapter

// Model selection: split from runner.go to keep it under the go-structure
// soft cap. Provider/model resolution, catalog grouping, and /model switch.

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
)

var _ ports.ModelSelectionRunner = (*CommandRunner)(nil)

func (r *CommandRunner) handleModel(args string) ports.CommandOutcome {
	if r.activeSession() == nil || r.res == nil {
		return ports.CommandOutcome{Err: "session or configuration not initialized"}
	}
	args = strings.TrimSpace(args)
	if args != "" {
		if first, rest, ok := splitFirstWhitespace(args); ok && rest != "" {
			if canonical, found := findProvider(r.res, first); found {
				return r.SelectModelForProvider(context.Background(), canonical, rest)
			}
		}
		return r.SelectModel(context.Background(), args)
	}
	groups := r.availableModelsByProvider()
	if len(groups) == 0 {
		return ports.CommandOutcome{Err: "no models loaded"}
	}
	return ports.CommandOutcome{ModelChoiceGroups: groups}
}

func splitFirstWhitespace(s string) (first, rest string, ok bool) {
	idx := strings.IndexFunc(s, unicode.IsSpace)
	if idx < 0 {
		return s, "", false
	}
	first = s[:idx]
	rest = strings.TrimSpace(s[idx:])
	return first, rest, true
}

func findProvider(res *config.Resolved, name string) (string, bool) {
	if res == nil {
		return "", false
	}
	for _, group := range res.ModelCatalog() {
		if strings.EqualFold(group.Provider, name) {
			return group.Provider, true
		}
	}
	if res.ProviderRuntimes != nil {
		for rName := range res.ProviderRuntimes {
			if strings.EqualFold(rName, name) {
				return rName, true
			}
		}
	}
	return "", false
}

// availableModelsByProvider returns the selectable catalog grouped by
// provider, in catalog order. The first group's provider name is the
// currently selected provider; later groups keep their catalog order
// so the picker stays stable across re-opens. An empty catalog
// falls back to the session's current model in a single flat group
// with no provider header.
func (r *CommandRunner) availableModelsByProvider() []ports.ModelChoiceGroup {
	// Defensive only: availableModelsByProvider has one caller, handleModel,
	// which already returns early on r.res == nil (see the guard at the top
	// of this file) before ever reaching this call - so this nil case is not
	// reachable through the CommandRunner's public API today.
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

// resolveExplicitModelSelection parses provider/model compatibility syntax:
// provider prefix against catalog providers, provider prefix against runtimes,
// or a slash-split identifier matching a known provider or runtime (case-folded).
// It returns (provider, model, true) if recognized, or ("", "", false).
// It contains only explicit provider/runtime prefix and slash-cut compatibility logic;
// it does not perform exact owner counting across catalog groups or fall back to
// the session's current provider.
func resolveExplicitModelSelection(res *config.Resolved, name string) (string, string, bool) {
	if res == nil {
		return "", "", false
	}
	name = strings.TrimSpace(name)

	// 1. Explicit provider prefix matching a catalog provider
	for _, group := range res.ModelCatalog() {
		prefix := group.Provider + "/"
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			return group.Provider, name[len(prefix):], true
		}
	}

	// 2. Prefix matching a configured provider runtime
	if res.ProviderRuntimes != nil {
		for p := range res.ProviderRuntimes {
			prefix := strings.ToLower(p) + "/"
			if strings.HasPrefix(strings.ToLower(name), prefix) {
				return p, name[len(prefix):], true
			}
		}
	}

	// 3. Name containing a slash matching known provider name
	if p, m, ok := strings.Cut(name, "/"); ok && p != "" && m != "" {
		for _, group := range res.ModelCatalog() {
			if strings.EqualFold(group.Provider, p) {
				return group.Provider, m, true
			}
		}
		if res.ProviderRuntimes != nil {
			for rName := range res.ProviderRuntimes {
				if strings.EqualFold(rName, p) {
					return rName, m, true
				}
			}
		}
	}

	return "", "", false
}

// SelectModelForProvider switches the session's active model to the specified
// model under the given provider without performing provider resolution.
func (r *CommandRunner) SelectModelForProvider(_ context.Context, provider, model string) ports.CommandOutcome {
	return r.switchModel(provider, model)
}

// SelectModel switches the session's active model.
func (r *CommandRunner) SelectModel(_ context.Context, name string) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil || r.res == nil {
		return ports.CommandOutcome{Err: "session or configuration not initialized"}
	}
	name = strings.TrimSpace(name)
	owners := r.res.ModelOwners(name)
	switch len(owners) {
	case 1:
		return r.switchModel(owners[0], name)
	case 0:
		if p, m, ok := resolveExplicitModelSelection(r.res, name); ok {
			return r.switchModel(p, m)
		}
		selProvider := r.res.ProviderName
		if sel := sess.CurrentSelection(); sel.ProviderName != "" {
			selProvider = sel.ProviderName
		}
		return r.switchModel(selProvider, name)
	default: // > 1
		if p, m, ok := resolveExplicitModelSelection(r.res, name); ok {
			return r.switchModel(p, m)
		}
		return ports.CommandOutcome{
			Err: fmt.Sprintf("model %q is ambiguous across providers: %s; run /model <provider> %s to switch", name, strings.Join(owners, ", "), name),
		}
	}
}

func (r *CommandRunner) switchModel(providerName, modelName string) ports.CommandOutcome {
	sess := r.activeSession()
	if sess == nil || r.res == nil {
		return ports.CommandOutcome{Err: "session or configuration not initialized"}
	}

	discarded, err := agents.SwitchModelCommand(sess, r.res, providerName, modelName)
	if err != nil {
		msg := fmt.Sprintf("failed to switch model to %q (%s): %v", modelName, providerName, err)
		if others := r.res.OtherProvidersWithModel(providerName, modelName); len(others) == 1 {
			msg += fmt.Sprintf(" (found under provider %s - run /model %s %s to switch)", others[0], others[0], modelName)
		} else if len(others) > 1 {
			msg += fmt.Sprintf(" (found under providers: %s - run /model <provider> %s to switch)", strings.Join(others, ", "), modelName)
		}
		return ports.CommandOutcome{Err: msg}
	}
	// r.res is the *config.Resolved shared by every pooled session
	// (NewSessionPool keeps the exact pointer NewCommandRunner was given),
	// and chat.NewSession seeds a brand-new session's initial binding
	// straight from res.ProviderName/res.Model. Writing this session's
	// resolved choice onto it would silently change the workspace's
	// configured default for every OTHER session the pool creates
	// afterward - this command is /model, scoped to the active session,
	// not a workspace-wide default change (that is the Settings screen's
	// job; see settings_providers.go).
	notice := fmt.Sprintf("Model set to %s (%s).", modelName, providerName)
	if discarded != "" {
		notice += fmt.Sprintf(" (Reasoning effort override %q discarded).", discarded)
	}
	return ports.CommandOutcome{Notice: notice}
}
