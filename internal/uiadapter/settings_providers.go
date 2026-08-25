package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func providerViewToSettings(v ports.ProviderView) config.ProviderSettings {
	var models []config.ModelSettings
	for _, m := range v.Models {
		models = append(models, config.ModelSettings{
			Name:                m.Name,
			ContextWindowTokens: m.ContextWindowTokens,
			MaxOutputTokens:     m.MaxOutputTokens,
			Reasoning:           m.Reasoning,
			ReasoningEfforts:    m.ReasoningEfforts,
		})
	}
	return config.ProviderSettings{
		Name:         v.Name,
		BaseURL:      v.BaseURL,
		APIKeyEnv:    v.APIKeyEnv,
		DefaultModel: v.DefaultModel,
		Models:       models,
	}
}

// settingsProviders
type settingsProviders struct{ *SettingsStore }

func (p settingsProviders) Providers() []ports.ProviderView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ports.ProviderView, len(p.providers))
	copy(out, p.providers)
	return out
}

func (p settingsProviders) Apply(_ context.Context, scope ports.Scope, e ports.ProviderEdit) (ports.SaveHandle, error) {
	return p.newSaveHandle(func() error { return p.applyProvider(e, scope) }), nil
}

func (s *SettingsStore) findProvider(name string) int {
	for i := range s.providers {
		if s.providers[i].Name == name {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) configPath() string {
	if s.res != nil && s.res.ConfigPath != "" {
		return s.res.ConfigPath
	}
	return config.UserConfigPath()
}

func (s *SettingsStore) applyProvider(e ports.ProviderEdit, scope ports.Scope) error {
	cfgPath := s.configPath()
	switch v := e.(type) {
	case ports.UpsertProvider:
		return s.applyUpsertProvider(v, cfgPath)
	case ports.RemoveProvider:
		return s.applyRemoveProvider(v, cfgPath)
	case ports.UpsertModel:
		return s.applyUpsertModel(v, cfgPath)
	case ports.RemoveModel:
		return s.applyRemoveModel(v, cfgPath)
	case ports.ActivateModel:
		return s.applyActivateModel(v)
	case ports.SetDefaultModel:
		return s.applySetDefaultModel(v, scope)
	case ports.SetProjectDefaultModel:
		return s.applySetProjectDefaultModel(v)
	case ports.ClearProjectDefaultModel:
		return s.applyClearProjectDefaultModel(v)
	default:
		return fmt.Errorf("unknown provider edit %T", e)
	}
}

// syncModelsAcrossScopeRowsLocked copies models onto every row in
// s.providers named providerName, not just the row the caller happened
// to mutate. A provider's Models list is catalog-wide, not scope-
// specific (see buildProviderViews: both a ScopeUser and a ScopeProject
// row for the same provider are populated from the same catalog entry),
// so a provider with a project default_model override - and therefore
// two rows - would otherwise leave its second row showing a stale Models
// slice after an UpsertModel/RemoveModel edit that only patched the
// first-found row.
func (s *SettingsStore) syncModelsAcrossScopeRowsLocked(providerName string, models []ports.ModelView) {
	for i := range s.providers {
		if s.providers[i].Name == providerName {
			s.providers[i].Models = models
		}
	}
}

func (s *SettingsStore) applyUpsertProvider(v ports.UpsertProvider, cfgPath string) error {
	if i := s.findProvider(v.Provider.Name); i >= 0 {
		// Preserve any existing scope-split rows for this provider (see
		// buildProviderViews): an UpsertProvider edit only carries one
		// ProviderView, so blindly overwriting every row sharing this name
		// would collapse a Project override row into a duplicate Global
		// one. Only the single found row is replaced; other-scope rows
		// keep their own Scope/DefaultModel and just pick up the shared,
		// non-scope-specific fields.
		s.providers[i] = v.Provider
		s.syncModelsAcrossScopeRowsLocked(v.Provider.Name, v.Provider.Models)
	} else {
		s.providers = append(s.providers, v.Provider)
	}
	if cfgPath != "" {
		_ = config.UpdateProviderConfig(cfgPath, providerViewToSettings(v.Provider))
	}
	return nil
}

func (s *SettingsStore) applyRemoveProvider(v ports.RemoveProvider, cfgPath string) error {
	i := s.findProvider(v.Name)
	if i < 0 {
		return fmt.Errorf("provider %q not found", v.Name)
	}
	// Drop every row for this provider, not just the first: a provider
	// with a project default_model override has two rows (see
	// buildProviderViews), and RemoveProvider means "remove the provider
	// entirely", not "remove its Global row and leave an orphaned Project
	// row behind".
	kept := s.providers[:0]
	for _, p := range s.providers {
		if p.Name != v.Name {
			kept = append(kept, p)
		}
	}
	s.providers = kept
	if cfgPath != "" {
		_ = config.RemoveProviderConfig(cfgPath, v.Name)
	}
	return nil
}

func (s *SettingsStore) applyUpsertModel(v ports.UpsertModel, cfgPath string) error {
	i := s.findProvider(v.Provider)
	if i < 0 {
		return fmt.Errorf("provider %q not found", v.Provider)
	}
	models := append([]ports.ModelView(nil), s.providers[i].Models...)
	found := false
	for j := range models {
		if models[j].Name == v.Model.Name {
			models[j] = v.Model
			found = true
			break
		}
	}
	if !found {
		models = append(models, v.Model)
	}
	s.syncModelsAcrossScopeRowsLocked(v.Provider, models)
	if cfgPath != "" {
		_ = config.UpdateProviderConfig(cfgPath, providerViewToSettings(s.providers[i]))
	}
	return nil
}

func (s *SettingsStore) applyRemoveModel(v ports.RemoveModel, cfgPath string) error {
	i := s.findProvider(v.Provider)
	if i < 0 {
		return fmt.Errorf("provider %q not found", v.Provider)
	}
	models := s.providers[i].Models
	removed := false
	next := make([]ports.ModelView, 0, len(models))
	for _, m := range models {
		if m.Name == v.Model {
			removed = true
			continue
		}
		next = append(next, m)
	}
	if !removed {
		return fmt.Errorf("model %q not found under %q", v.Model, v.Provider)
	}
	s.syncModelsAcrossScopeRowsLocked(v.Provider, next)
	if cfgPath != "" {
		_ = config.UpdateProviderConfig(cfgPath, providerViewToSettings(s.providers[i]))
	}
	return nil
}

func (s *SettingsStore) applyActivateModel(v ports.ActivateModel) error {
	target := s.findProvider(v.Provider)
	if target < 0 {
		return fmt.Errorf("provider %q not found", v.Provider)
	}
	foundModel := false
	for _, m := range s.providers[target].Models {
		if m.Name == v.Model {
			foundModel = true
			break
		}
	}
	if !foundModel {
		return fmt.Errorf("model %q not found under %q", v.Model, v.Provider)
	}
	if s.sess != nil && s.res != nil {
		if _, err := cliagents.SwitchModelCommand(s.sess, s.res, v.Provider, v.Model); err != nil {
			return fmt.Errorf("failed to switch model to %q (%s): %w", v.Model, v.Provider, err)
		}
		s.res.ProviderName = v.Provider
		s.res.Model = v.Model
	}
	// Active/ActiveModel are provider-wide, not per-scope: a provider
	// with both a Global and a Project row (see buildProviderViews) must
	// show BOTH rows as active/inactive together, so match by Name
	// rather than by target's own index - matching only target would
	// leave a same-provider Project row stuck showing Active=false after
	// switching to it.
	for i := range s.providers {
		s.providers[i].Active = (s.providers[i].Name == v.Provider)
		if s.providers[i].Active {
			s.providers[i].ActiveModel = v.Model
		}
	}
	if cfgPath := s.configPath(); cfgPath != "" {
		_ = config.UpdateActiveModelConfig(cfgPath, v.Provider, v.Model)
	}
	return nil
}

// findProviderModel validates that provider has a model named modelName in
// its catalog and returns the row index of provider's first (i.e. its
// ScopeUser) row, or -1 if either the provider or the model is unknown.
// The model list is identical across every scope row for a given
// provider name (both are copied from the same catalog entry in
// buildProviderViews), so any row can answer "does this model exist".
func (s *SettingsStore) findProviderModel(providerName, modelName string) int {
	target := s.findProvider(providerName)
	if target < 0 {
		return -1
	}
	for _, m := range s.providers[target].Models {
		if m.Name == modelName {
			return target
		}
	}
	return -1
}

// applySetDefaultModel writes v.Model as the persisted default_model for
// v.Provider in the config file that scope owns (see
// providerConfigPathForScope): ScopeUser is the base config, ScopeProject
// is the workspace's own mivia.toml. Unlike SetProjectDefaultModel, the
// destination is whichever scope the caller (the row's own Apply call)
// passed - editing a Global row's default writes the user file even
// when a project override exists and remains untouched; editing a
// Project row's default writes the project file. providerViews is
// rebuilt from disk afterward via buildProviderViews rather than patched
// in place because scope-aware default_model edits can add/remove rows,
// not just change a field (see buildProviderViews' doc comment).
func (s *SettingsStore) applySetDefaultModel(v ports.SetDefaultModel, scope ports.Scope) error {
	if s.findProviderModel(v.Provider, v.Model) < 0 {
		return fmt.Errorf("model %q not found under %q", v.Model, v.Provider)
	}
	path := s.providerConfigPathForScope(scope)
	if path == "" {
		if scope == ports.ScopeProject {
			return fmt.Errorf("no project config available to set a project default for %q", v.Provider)
		}
		return fmt.Errorf("no config file resolvable for %q", v.Provider)
	}
	if err := config.UpdateProviderDefaultModel(path, v.Provider, v.Model); err != nil {
		return fmt.Errorf("failed to persist default model: %w", err)
	}
	s.providers = s.buildProviderViews()
	return nil
}

// applySetProjectDefaultModel writes v.Model as v.Provider's project-scope
// default_model override, always targeting the project config file
// regardless of which row (Global or Project) the caller invoked this
// from - the "make this the default for THIS project" action offered on
// a Global row. Errors if there is no project config to write (no
// workspace, or the workspace IS the base config - see
// providerConfigPathForScope).
func (s *SettingsStore) applySetProjectDefaultModel(v ports.SetProjectDefaultModel) error {
	if s.findProviderModel(v.Provider, v.Model) < 0 {
		return fmt.Errorf("model %q not found under %q", v.Model, v.Provider)
	}
	path := s.providerConfigPathForScope(ports.ScopeProject)
	if path == "" {
		return fmt.Errorf("no project config available to set a project default for %q", v.Provider)
	}
	if err := config.UpdateProviderDefaultModel(path, v.Provider, v.Model); err != nil {
		return fmt.Errorf("failed to persist project default model: %w", err)
	}
	s.providers = s.buildProviderViews()
	return nil
}

// applyClearProjectDefaultModel removes v.Provider's project-scope
// default_model override, reverting its effective default to the global
// value. A missing project config file is treated as "nothing to
// clear" rather than an error, matching ClearProviderDefaultModel's own
// tolerance for clearing an already-absent override.
func (s *SettingsStore) applyClearProjectDefaultModel(v ports.ClearProjectDefaultModel) error {
	path := s.providerConfigPathForScope(ports.ScopeProject)
	if path == "" {
		return nil
	}
	if err := config.ClearProviderDefaultModel(path, v.Provider); err != nil {
		return fmt.Errorf("failed to clear project default model: %w", err)
	}
	s.providers = s.buildProviderViews()
	return nil
}
