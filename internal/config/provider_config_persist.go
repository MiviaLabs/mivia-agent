package config

import (
	"fmt"
	"strings"
)

// UpdateProviderDefaultModel updates or sets the default_model key under
// [providers.<providerName>] in the TOML config file at path.
// If the provider section doesn't exist yet, it will be initialized with
// [providers.<providerName>]. Locked and atomic (see updateConfigFile in
// persist_lock.go) so a concurrent default-model edit to the same file
// from another goroutine cannot silently lose either write.
func UpdateProviderDefaultModel(path, providerName, modelName string) error {
	if providerName == "" {
		return fmt.Errorf("provider name is empty")
	}
	if modelName == "" {
		return fmt.Errorf("model name is empty")
	}
	return updateConfigFile(path, func(raw map[string]any) error {
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
		return nil
	})
}

// ClearProviderDefaultModel removes the default_model key from
// [providers.<providerName>] in the TOML config file at path, leaving
// the rest of that provider's stanza (if any) intact. If removing the
// key leaves the stanza empty, the stanza itself is dropped so clearing
// a project-scope override does not litter the project file with an
// inert [providers.x] table that sets nothing. A path/provider with no
// default_model key set is not an error: clearing an override that is
// already absent is a no-op, the same tolerance a repeated command
// should have. Locked and atomic (see updateConfigFile).
func ClearProviderDefaultModel(path, providerName string) error {
	if providerName == "" {
		return fmt.Errorf("provider name is empty")
	}
	return updateConfigFile(path, func(raw map[string]any) error {
		providersRaw, ok := raw["providers"].(map[string]any)
		if !ok || providersRaw == nil {
			return nil
		}
		pRaw, ok := providersRaw[providerName].(map[string]any)
		if !ok || pRaw == nil {
			return nil
		}
		if _, hasKey := pRaw["default_model"]; !hasKey {
			return nil
		}
		delete(pRaw, "default_model")
		if len(pRaw) == 0 {
			delete(providersRaw, providerName)
		} else {
			providersRaw[providerName] = pRaw
		}
		raw["providers"] = providersRaw
		return nil
	})
}

// LoadProviderDefaultOverrides reads path's own [providers.<name>]
// default_model keys, unmerged with any other config layer, and returns
// them as a provider-name-to-model map. It exists so a caller can tell
// which file actually SETS a given provider's default apart from
// Load()'s merged/effective result (loadFile's workspace overlay folds
// a project file's default_model into the same in-memory ProviderConfig
// the base file populated - see workspace_overlay_test.go - so the
// merged Resolved alone cannot say whether a provider's effective
// default came from the user file or a project override). An empty or
// missing path, or one with no [providers] table, returns an empty map
// and no error: this is a display-only read, not config validation, and
// must not block the settings screen from opening over a malformed or
// absent project file the way Load()'s closed-shape decode would.
// Providers with no default_model key are simply absent from the
// returned map rather than mapped to "". This is a read, not covered by
// updateConfigFile's write lock, but readConfigMap's os.ReadFile call is
// itself atomic with respect to any concurrent writeFileAtomic rename
// (a reader either sees the file entirely before or entirely after a
// rename, never a partial write).
func LoadProviderDefaultOverrides(path string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return out, nil
	}
	raw, err := readConfigMap(path)
	if err != nil {
		return out, err
	}
	providersRaw, ok := raw["providers"].(map[string]any)
	if !ok {
		return out, nil
	}
	for name, entry := range providersRaw {
		pRaw, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		dm, ok := pRaw["default_model"].(string)
		if !ok || strings.TrimSpace(dm) == "" {
			continue
		}
		out[name] = dm
	}
	return out, nil
}
