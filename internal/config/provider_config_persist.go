package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// UpdateProviderDefaultModel updates or sets the default_model key under
// [providers.<providerName>] in the TOML config file at path.
// If the provider section doesn't exist yet, it will be initialized with [providers.<providerName>].
func UpdateProviderDefaultModel(path, providerName, modelName string) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	if providerName == "" {
		return fmt.Errorf("provider name is empty")
	}
	if modelName == "" {
		return fmt.Errorf("model name is empty")
	}

	data, err := os.ReadFile(path)
	var raw map[string]any
	if err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("unmarshal config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config: %w", err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	providersRaw, ok := raw["providers"].(map[string]any)
	if !ok || providersRaw == nil {
		providersRaw = make(map[string]any)
	}

	pRaw, ok := providersRaw[providerName].(map[string]any)
	if !ok || pRaw == nil {
		pRaw = make(map[string]any)
	}

	pRaw["default_model"] = modelName
	providersRaw[providerName] = pRaw
	raw["providers"] = providersRaw

	out, err := toml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal updated config: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
	}

	return os.WriteFile(path, out, 0o600)
}
