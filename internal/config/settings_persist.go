package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// GeneralSettings contains general and TUI settings for config updates.
type GeneralSettings struct {
	Theme                  string
	Mouse                  bool
	ShowReasoning          bool
	ShowIterationNotices   bool
	ShowPromptCacheNotices bool
	ScrollLines            int
	ApprovalDefault        string
	ScreenReader           bool
	ReducedMotion          bool
}

// ModelSettings contains model information for config updates.
type ModelSettings struct {
	Name                string
	ContextWindowTokens int
	MaxOutputTokens     int
	Reasoning           string
	ReasoningEfforts    []string
}

// ProviderSettings contains provider information for config updates.
type ProviderSettings struct {
	Name         string
	BaseURL      string
	APIKeyEnv    string
	DefaultModel string
	Models       []ModelSettings
}

// MCPServerSettings contains MCP server definition for config updates.
type MCPServerSettings struct {
	ID        string
	Transport string
	Command   string
	Args      []string
	Endpoint  string
	EnvNames  []string
}

// AgentFileSettings contains agent definition for markdown file persistence.
type AgentFileSettings struct {
	Name        string
	Description string
	Provider    string
	Model       string
	Tools       []string
	Skills      []string
	MCPServers  []string
}

func readConfigMap(path string) (map[string]any, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is empty")
	}
	data, err := os.ReadFile(path)
	var raw map[string]any
	if err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}
	return raw, nil
}

// UpdateGeneralConfig persists general and TUI options into the TOML
// configuration file at path. Locked and atomic (see updateConfigFile in
// persist_lock.go) so a concurrent edit to the same file from another
// goroutine cannot silently lose either write and a crash mid-write
// cannot corrupt the file.
func UpdateGeneralConfig(path string, view GeneralSettings) error {
	return updateConfigFile(path, func(raw map[string]any) error {
		tuiMap, _ := raw["tui"].(map[string]any)
		if tuiMap == nil {
			tuiMap = make(map[string]any)
		}
		if view.Theme != "" {
			tuiMap["theme"] = view.Theme
		}
		tuiMap["mouse"] = view.Mouse
		tuiMap["show_reasoning"] = view.ShowReasoning
		if view.ScrollLines > 0 {
			tuiMap["scroll_lines"] = view.ScrollLines
		}
		tuiMap["screen_reader"] = view.ScreenReader
		tuiMap["reduced_motion"] = view.ReducedMotion
		raw["tui"] = tuiMap

		chatMap, _ := raw["chat"].(map[string]any)
		if chatMap == nil {
			chatMap = make(map[string]any)
		}
		chatMap["show_iteration_notices"] = view.ShowIterationNotices
		chatMap["show_prompt_cache_notices"] = view.ShowPromptCacheNotices
		raw["chat"] = chatMap

		if view.ApprovalDefault != "" {
			apprMap, _ := raw["approvals"].(map[string]any)
			if apprMap == nil {
				apprMap = make(map[string]any)
			}
			apprMap["default_mode"] = view.ApprovalDefault
			raw["approvals"] = apprMap
		}
		return nil
	})
}

// UpdateActiveModelConfig updates the active provider and default model in
// mivia.toml. Locked and atomic (see updateConfigFile).
func UpdateActiveModelConfig(path string, provider, model string) error {
	return updateConfigFile(path, func(raw map[string]any) error {
		provMap, _ := raw["provider"].(map[string]any)
		if provMap == nil {
			provMap = make(map[string]any)
		}
		if provider != "" {
			provMap["name"] = provider
		}
		if model != "" {
			provMap["default_model"] = model
		}
		raw["provider"] = provMap

		if provider != "" && model != "" {
			providersMap, _ := raw["providers"].(map[string]any)
			if providersMap == nil {
				providersMap = make(map[string]any)
			}
			pEntry, _ := providersMap[provider].(map[string]any)
			if pEntry == nil {
				pEntry = make(map[string]any)
			}
			pEntry["default_model"] = model

			var models []map[string]any
			if rawModels, ok := pEntry["models"].([]any); ok {
				for _, m := range rawModels {
					if mMap, ok := m.(map[string]any); ok {
						models = append(models, mMap)
					}
				}
			} else if typedModels, ok := pEntry["models"].([]map[string]any); ok {
				models = typedModels
			}
			foundModel := false
			for _, m := range models {
				if m["name"] == model {
					foundModel = true
					break
				}
			}
			if !foundModel {
				models = append(models, map[string]any{"name": model, "context_window_tokens": 128000})
			}
			pEntry["models"] = models

			providersMap[provider] = pEntry
			raw["providers"] = providersMap
		}
		return nil
	})
}

// UpdateProviderConfig adds or updates a provider and its models in
// mivia.toml. Locked and atomic (see updateConfigFile).
func UpdateProviderConfig(path string, pv ProviderSettings) error {
	return updateConfigFile(path, func(raw map[string]any) error {
		providersMap, _ := raw["providers"].(map[string]any)
		if providersMap == nil {
			providersMap = make(map[string]any)
		}

		pEntry, _ := providersMap[pv.Name].(map[string]any)
		if pEntry == nil {
			pEntry = make(map[string]any)
		}
		if pv.BaseURL != "" {
			pEntry["base_url"] = pv.BaseURL
		}
		if pv.APIKeyEnv != "" {
			pEntry["api_key_env"] = pv.APIKeyEnv
		}
		if pv.DefaultModel != "" {
			pEntry["default_model"] = pv.DefaultModel
		}

		var models []map[string]any
		for _, m := range pv.Models {
			mMap := map[string]any{"name": m.Name}
			if m.ContextWindowTokens > 0 {
				mMap["context_window_tokens"] = m.ContextWindowTokens
			}
			if m.MaxOutputTokens > 0 {
				mMap["max_output_tokens"] = m.MaxOutputTokens
			}
			if m.Reasoning != "" {
				mMap["reasoning"] = m.Reasoning
			}
			if len(m.ReasoningEfforts) > 0 {
				mMap["reasoning_efforts"] = m.ReasoningEfforts
			}
			models = append(models, mMap)
		}
		if len(models) > 0 {
			pEntry["models"] = models
		}
		providersMap[pv.Name] = pEntry
		raw["providers"] = providersMap
		return nil
	})
}

// RemoveProviderConfig removes a provider definition from mivia.toml.
// Locked and atomic (see updateConfigFile).
func RemoveProviderConfig(path string, name string) error {
	return updateConfigFile(path, func(raw map[string]any) error {
		providersMap, ok := raw["providers"].(map[string]any)
		if ok && providersMap != nil {
			delete(providersMap, name)
			raw["providers"] = providersMap
		}
		return nil
	})
}

// UpdateMCPServerConfig inserts or updates an MCP server entry in
// mivia.toml. Locked and atomic (see updateConfigFile).
func UpdateMCPServerConfig(path string, srv MCPServerSettings) error {
	return updateConfigFile(path, func(raw map[string]any) error {
		mcpMap, _ := raw["mcp"].(map[string]any)
		if mcpMap == nil {
			mcpMap = make(map[string]any)
		}
		mcpMap["enabled"] = true

		srvMap := map[string]any{
			"id": srv.ID,
		}
		trans := srv.Transport
		if trans == "" {
			if srv.Command != "" {
				trans = "stdio"
			} else if srv.Endpoint != "" {
				trans = "streamable_http"
			}
		}
		if trans != "" {
			srvMap["transport"] = trans
		}
		if srv.Command != "" {
			srvMap["command"] = srv.Command
		}
		if srv.Endpoint != "" {
			srvMap["url"] = srv.Endpoint
		}
		if len(srv.Args) > 0 {
			srvMap["args"] = srv.Args
		}
		if len(srv.EnvNames) > 0 {
			srvMap["env"] = srv.EnvNames
		}

		var existing []map[string]any
		if rawServers, ok := mcpMap["servers"].([]any); ok {
			for _, s := range rawServers {
				if sMap, ok := s.(map[string]any); ok {
					existing = append(existing, sMap)
				}
			}
		} else if typedServers, ok := mcpMap["servers"].([]map[string]any); ok {
			existing = typedServers
		}

		found := false
		for i := range existing {
			if existing[i]["id"] == srv.ID {
				existing[i] = srvMap
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, srvMap)
		}
		mcpMap["servers"] = existing
		raw["mcp"] = mcpMap
		return nil
	})
}

// RemoveMCPServerConfig deletes an MCP server entry from mivia.toml.
// Locked and atomic (see updateConfigFile).
func RemoveMCPServerConfig(path string, id string) error {
	return updateConfigFile(path, func(raw map[string]any) error {
		mcpMap, ok := raw["mcp"].(map[string]any)
		if !ok || mcpMap == nil {
			return nil
		}

		var existing []map[string]any
		if rawServers, ok := mcpMap["servers"].([]any); ok {
			for _, s := range rawServers {
				if sMap, ok := s.(map[string]any); ok {
					if sMap["id"] != id {
						existing = append(existing, sMap)
					}
				}
			}
		} else if typedServers, ok := mcpMap["servers"].([]map[string]any); ok {
			for _, s := range typedServers {
				if s["id"] != id {
					existing = append(existing, s)
				}
			}
		}
		mcpMap["servers"] = existing
		raw["mcp"] = mcpMap
		return nil
	})
}

// WriteAgentFile writes an agent definition markdown file with YAML
// frontmatter. Locked per target path (see lockPersistFile) and written
// atomically via writeFileAtomic so a concurrent edit to the SAME agent
// file from another goroutine cannot interleave with this write, and a
// crash mid-write cannot leave a truncated agent file behind.
func WriteAgentFile(dir string, ag AgentFileSettings, systemPrompt string) error {
	if dir == "" {
		return fmt.Errorf("agents directory is empty")
	}
	if ag.Name == "" {
		return fmt.Errorf("agent name is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agents directory: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", ag.Name))
	if ag.Description != "" {
		sb.WriteString(fmt.Sprintf("description: %q\n", ag.Description))
	}
	if ag.Provider != "" {
		sb.WriteString(fmt.Sprintf("provider: %s\n", ag.Provider))
	}
	if ag.Model != "" {
		sb.WriteString(fmt.Sprintf("model: %s\n", ag.Model))
	}
	if len(ag.Tools) > 0 {
		sb.WriteString("tools:\n")
		for _, t := range ag.Tools {
			sb.WriteString(fmt.Sprintf("  - %s\n", t))
		}
	}
	if len(ag.Skills) > 0 {
		sb.WriteString("skills:\n")
		for _, s := range ag.Skills {
			sb.WriteString(fmt.Sprintf("  - %s\n", s))
		}
	}
	if len(ag.MCPServers) > 0 {
		sb.WriteString("mcp_servers:\n")
		for _, s := range ag.MCPServers {
			sb.WriteString(fmt.Sprintf("  - %s\n", s))
		}
	}
	sb.WriteString("---\n\n")
	sb.WriteString(systemPrompt)
	if !strings.HasSuffix(systemPrompt, "\n") {
		sb.WriteString("\n")
	}

	target := filepath.Join(dir, fmt.Sprintf("%s.md", ag.Name))
	unlock := lockPersistFile(target)
	defer unlock()
	return writeFileAtomic(target, []byte(sb.String()), 0o600)
}

// RemoveAgentFile deletes an agent markdown file from the given directory.
// Locked against a concurrent WriteAgentFile to the same path (see
// lockPersistFile) so a remove cannot interleave with an in-flight write.
func RemoveAgentFile(dir string, name string) error {
	if dir == "" || name == "" {
		return nil
	}
	target := filepath.Join(dir, fmt.Sprintf("%s.md", name))
	unlock := lockPersistFile(target)
	defer unlock()
	err := os.Remove(target)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
