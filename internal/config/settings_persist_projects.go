package config

import (
	"strconv"
)

// ProjectSettings contains project-scoped configuration for config updates.
type ProjectSettings struct {
	EnvFile         string
	BranchPrefix    string
	SystemPrompt    string
	Temperature     string
	MaxTokens       string
	MaxPromptTokens string
	MaxSteps        string
	RunTimeoutSec   int
	StoreBackend    string
	StorePath       string
	Sandbox         bool
	RedactToolArgs  bool
}

// UpdateProjectConfig persists project settings into the TOML configuration
// file at path. Locked and atomic (see updateConfigFile).
func UpdateProjectConfig(path string, ps ProjectSettings) error {
	return updateConfigFile(path, func(raw map[string]any) error {
		if ps.EnvFile != "" {
			raw["env_file"] = ps.EnvFile
		}
		updateProjectWorktrees(raw, ps)
		updateProjectChat(raw, ps)
		updateProjectTools(raw, ps)
		updateProjectSubagents(raw, ps)
		updateProjectHarness(raw, ps)
		updateProjectPrivacy(raw, ps)
		return nil
	})
}

func updateProjectWorktrees(raw map[string]any, ps ProjectSettings) {
	if ps.BranchPrefix != "" {
		wtMap, _ := raw["worktrees"].(map[string]any)
		if wtMap == nil {
			wtMap = make(map[string]any)
		}
		wtMap["branch_prefix"] = ps.BranchPrefix
		raw["worktrees"] = wtMap
	}
}

func updateProjectChat(raw map[string]any, ps ProjectSettings) {
	chatMap, _ := raw["chat"].(map[string]any)
	if chatMap == nil {
		chatMap = make(map[string]any)
	}
	if ps.SystemPrompt != "" {
		chatMap["system_prompt"] = ps.SystemPrompt
	}
	updateProjectChatNumbers(chatMap, ps)
	raw["chat"] = chatMap
}

func updateProjectChatNumbers(chatMap map[string]any, ps ProjectSettings) {
	if ps.Temperature != "" {
		if ps.Temperature == "default" {
			delete(chatMap, "temperature")
		} else if f, err := strconv.ParseFloat(ps.Temperature, 64); err == nil {
			chatMap["temperature"] = f
		}
	}
	if ps.MaxTokens != "" {
		if ps.MaxTokens == "default" {
			delete(chatMap, "max_tokens")
		} else if n, err := strconv.Atoi(ps.MaxTokens); err == nil {
			chatMap["max_tokens"] = n
		}
	}
	if ps.MaxPromptTokens != "" {
		if ps.MaxPromptTokens == "default" {
			delete(chatMap, "max_prompt_tokens")
		} else if n, err := strconv.Atoi(ps.MaxPromptTokens); err == nil {
			chatMap["max_prompt_tokens"] = n
		}
	}
	if ps.MaxSteps != "" {
		if ps.MaxSteps == "default" {
			delete(chatMap, "max_steps")
		} else if ps.MaxSteps == "unlimited (0)" || ps.MaxSteps == "0" {
			chatMap["max_steps"] = 0
		} else if n, err := strconv.Atoi(ps.MaxSteps); err == nil {
			chatMap["max_steps"] = n
		}
	}
}

func updateProjectTools(raw map[string]any, ps ProjectSettings) {
	if ps.RunTimeoutSec > 0 {
		toolsMap, _ := raw["tools"].(map[string]any)
		if toolsMap == nil {
			toolsMap = make(map[string]any)
		}
		toolsMap["run_timeout_seconds"] = ps.RunTimeoutSec
		raw["tools"] = toolsMap
	}
}

func updateProjectSubagents(raw map[string]any, ps ProjectSettings) {
	subMap, _ := raw["subagents"].(map[string]any)
	if subMap == nil {
		subMap = make(map[string]any)
	}
	if ps.StoreBackend != "" {
		subMap["store_backend"] = ps.StoreBackend
	}
	if ps.StorePath != "" {
		subMap["store_path"] = ps.StorePath
	}
	raw["subagents"] = subMap
}

func updateProjectHarness(raw map[string]any, ps ProjectSettings) {
	harnessMap, _ := raw["harness"].(map[string]any)
	if harnessMap == nil {
		harnessMap = make(map[string]any)
	}
	harnessMap["sandbox"] = ps.Sandbox
	raw["harness"] = harnessMap
}

func updateProjectPrivacy(raw map[string]any, ps ProjectSettings) {
	privMap, _ := raw["privacy"].(map[string]any)
	if privMap == nil {
		privMap = make(map[string]any)
	}
	privMap["redact_tool_args"] = ps.RedactToolArgs
	raw["privacy"] = privMap
}
