package config

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// UpdateChatNoticeConfig updates or sets the show_iteration_notices and
// show_prompt_cache_notices keys under [chat] in the TOML config file at path.
func UpdateChatNoticeConfig(path string, showIteration, showPromptCache bool) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}

	data, err := os.ReadFile(path)
	var raw map[string]any
	if err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("unmarshal config: %w", err)
		}
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	chatRaw, ok := raw["chat"].(map[string]any)
	if !ok || chatRaw == nil {
		chatRaw = make(map[string]any)
	}

	chatRaw["show_iteration_notices"] = showIteration
	chatRaw["show_prompt_cache_notices"] = showPromptCache
	raw["chat"] = chatRaw

	out, err := toml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal updated config: %w", err)
	}

	return os.WriteFile(path, out, 0o600)
}
