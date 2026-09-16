package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider/registry"
)

// Provider and model selection: picking the active [providers.*] stanza and
// model from the config plus CLI overrides, and normalizing every declared
// provider stanza before that selection runs.

func resolveProvider(file File, opts LoadOptions) (string, ProviderConfig, string, error) {
	if file.Providers == nil {
		file.Providers = map[string]ProviderConfig{}
	}
	name := strings.TrimSpace(file.Provider.Name)
	if name == "" {
		name = DefaultProvider
	}
	if opts.ProviderOverride != "" {
		name = strings.TrimSpace(opts.ProviderOverride)
	}
	name = strings.ToLower(name)
	descriptor, ok := registry.Lookup(name)
	if !ok {
		return "", ProviderConfig{}, "", fmt.Errorf("unknown provider %q (supported: %s)", name, strings.Join(registry.Names(), ", "))
	}
	pc, ok := file.Providers[name]
	if !ok {
		return "", ProviderConfig{}, "", fmt.Errorf("provider %q is not configured: no [providers.%s] section in the active config (declare it there or in ~/.mivia/mivia.toml; run 'mivia setup')", name, name)
	}
	if len(pc.Models) == 0 {
		return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: models must be non-empty", name)
	}
	defaultModel := strings.TrimSpace(pc.DefaultModel)
	model := pc.Models[0].Name
	if defaultModel != "" {
		normalizedDefault, err := NormalizeModelName(defaultModel)
		if err != nil {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: default_model is invalid", name)
		}
		defaultModel = normalizedDefault
		if !slices.Contains(modelNames(pc.Models), normalizedDefault) {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: default_model is not in models (%s)", name, strings.Join(modelNames(pc.Models), ", "))
		}
		pc.DefaultModel = defaultModel
		model = defaultModel
	}
	if pc.BaseURL == "" {
		pc.BaseURL = descriptor.DefaultURL
	}
	if pc.APIKeyEnv == "" {
		pc.APIKeyEnv = descriptor.DefaultAPIKeyEnv
	}
	if strings.TrimSpace(opts.ModelOverride) != "" {
		override, err := NormalizeModelName(opts.ModelOverride)
		if err != nil {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: --model is invalid", name)
		}
		if !slices.Contains(modelNames(pc.Models), override) {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: --model is not in models (%s)", name, strings.Join(modelNames(pc.Models), ", "))
		}
		model = override
	}
	return name, pc, model, nil
}

func normalizeProviderConfigs(file *File, maxTokens int) error {
	if file.Providers == nil {
		file.Providers = map[string]ProviderConfig{}
	}
	seen := make(map[string]string, len(file.Providers))
	normalized := make(map[string]ProviderConfig, len(file.Providers))
	for rawName, pc := range file.Providers {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			return fmt.Errorf("provider name is empty")
		}
		if previous, ok := seen[name]; ok && previous != rawName {
			return fmt.Errorf("provider names %q and %q collide by case", previous, rawName)
		}
		if _, ok := registry.Lookup(name); !ok {
			return fmt.Errorf("unknown provider %q", name)
		}
		if pc.LegacyModel != nil {
			return fmt.Errorf("[providers.%s]: model is no longer supported; declare models", name)
		}
		models, err := normalizeModels(pc.Models, maxTokens, name)
		if err != nil {
			return fmt.Errorf("[providers.%s]: %w", name, err)
		}
		pc.Models = models
		if pc.BaseURL == "" {
			d, _ := registry.Lookup(name)
			pc.BaseURL = d.DefaultURL
		}
		if pc.APIKeyEnv == "" {
			d, _ := registry.Lookup(name)
			pc.APIKeyEnv = d.DefaultAPIKeyEnv
		}
		normalized[name] = pc
		seen[name] = rawName
	}
	file.Providers = normalized
	return nil
}

func modelNames(models []ModelSpec) []string {
	names := make([]string, len(models))
	for i, model := range models {
		names[i] = model.Name
	}
	return names
}

func cloneModelSpecs(models []ModelSpec) []ModelSpec {
	return slices.Clone(models)
}
