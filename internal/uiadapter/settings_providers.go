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

func (p settingsProviders) Apply(_ context.Context, _ ports.Scope, e ports.ProviderEdit) (ports.SaveHandle, error) {
	return p.newSaveHandle(func() error { return p.applyProvider(e) }), nil
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

func (s *SettingsStore) applyProvider(e ports.ProviderEdit) error {
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
		return s.applySetDefaultModel(v)
	default:
		return fmt.Errorf("unknown provider edit %T", e)
	}
}

func (s *SettingsStore) applyUpsertProvider(v ports.UpsertProvider, cfgPath string) error {
	if i := s.findProvider(v.Provider.Name); i >= 0 {
		s.providers[i] = v.Provider
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
	s.providers = append(s.providers[:i], s.providers[i+1:]...)
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
	found := false
	for j := range s.providers[i].Models {
		if s.providers[i].Models[j].Name == v.Model.Name {
			s.providers[i].Models[j] = v.Model
			found = true
			break
		}
	}
	if !found {
		s.providers[i].Models = append(s.providers[i].Models, v.Model)
	}
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
	for j := range models {
		if models[j].Name == v.Model {
			s.providers[i].Models = append(models[:j], models[j+1:]...)
			removed = true
			break
		}
	}
	if !removed {
		return fmt.Errorf("model %q not found under %q", v.Model, v.Provider)
	}
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
	for i := range s.providers {
		s.providers[i].Active = (i == target)
	}
	s.providers[target].ActiveModel = v.Model
	if cfgPath := s.configPath(); cfgPath != "" {
		_ = config.UpdateActiveModelConfig(cfgPath, v.Provider, v.Model)
	}
	return nil
}

func (s *SettingsStore) applySetDefaultModel(v ports.SetDefaultModel) error {
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
	cfgPath := s.configPath()
	if cfgPath != "" {
		if err := config.UpdateProviderDefaultModel(cfgPath, v.Provider, v.Model); err != nil {
			return fmt.Errorf("failed to persist default model: %w", err)
		}
	}
	s.providers[target].DefaultModel = v.Model
	return nil
}
