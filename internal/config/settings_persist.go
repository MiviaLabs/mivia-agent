package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/pelletier/go-toml/v2"
)

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

func writeConfigMap(path string, raw map[string]any) error {
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

// UpdateGeneralConfig persists general and TUI options into the TOML configuration file at path.
func UpdateGeneralConfig(path string, view ports.GeneralView) error {
	raw, err := readConfigMap(path)
	if err != nil {
		return err
	}

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

	return writeConfigMap(path, raw)
}

// UpdateActiveModelConfig updates the active provider and default model in mivia.toml.
func UpdateActiveModelConfig(path string, provider, model string) error {
	raw, err := readConfigMap(path)
	if err != nil {
		return err
	}

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

	return writeConfigMap(path, raw)
}

// UpdateProviderConfig adds or updates a provider and its models in mivia.toml.
func UpdateProviderConfig(path string, pv ports.ProviderView) error {
	raw, err := readConfigMap(path)
	if err != nil {
		return err
	}

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

	return writeConfigMap(path, raw)
}

// RemoveProviderConfig removes a provider definition from mivia.toml.
func RemoveProviderConfig(path string, name string) error {
	raw, err := readConfigMap(path)
	if err != nil {
		return err
	}
	providersMap, ok := raw["providers"].(map[string]any)
	if ok && providersMap != nil {
		delete(providersMap, name)
		raw["providers"] = providersMap
	}
	return writeConfigMap(path, raw)
}

// UpdateMCPServerConfig inserts or updates an MCP server entry in mivia.toml.
func UpdateMCPServerConfig(path string, srv ports.MCPServerView) error {
	raw, err := readConfigMap(path)
	if err != nil {
		return err
	}

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

	return writeConfigMap(path, raw)
}

// RemoveMCPServerConfig deletes an MCP server entry from mivia.toml.
func RemoveMCPServerConfig(path string, id string) error {
	raw, err := readConfigMap(path)
	if err != nil {
		return err
	}
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

	return writeConfigMap(path, raw)
}

// WriteAgentFile writes an agent definition markdown file with YAML frontmatter.
func WriteAgentFile(dir string, ag ports.AgentView, systemPrompt string) error {
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
	return os.WriteFile(target, []byte(sb.String()), 0o600)
}

// RemoveAgentFile deletes an agent markdown file from the given directory.
func RemoveAgentFile(dir string, name string) error {
	if dir == "" || name == "" {
		return nil
	}
	target := filepath.Join(dir, fmt.Sprintf("%s.md", name))
	err := os.Remove(target)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
