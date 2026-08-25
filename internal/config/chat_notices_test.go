package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestChatNoticesConfig_DefaultsToFalse(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5", context_window_tokens = 128000 }]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if res.ShowIterationNotices {
		t.Fatalf("ShowIterationNotices = true, want false (default)")
	}
	if res.ShowPromptCacheNotices {
		t.Fatalf("ShowPromptCacheNotices = true, want false (default)")
	}
}

func TestChatNoticesConfig_ExplicitTrue(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5", context_window_tokens = 128000 }]

[chat]
show_iteration_notices = true
show_prompt_cache_notices = true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ShowIterationNotices {
		t.Fatalf("ShowIterationNotices = false, want true")
	}
	if !res.ShowPromptCacheNotices {
		t.Fatalf("ShowPromptCacheNotices = false, want true")
	}
}

func TestUpdateChatNoticeConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5", context_window_tokens = 128000 }]

[chat]
system_prompt = "custom prompt"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := config.UpdateChatNoticeConfig(cfgPath, true, true); err != nil {
		t.Fatal(err)
	}

	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ShowIterationNotices {
		t.Fatalf("after update: ShowIterationNotices = false, want true")
	}
	if !res.ShowPromptCacheNotices {
		t.Fatalf("after update: ShowPromptCacheNotices = false, want true")
	}
	if res.SystemPrompt != "custom prompt" {
		t.Fatalf("system prompt corrupted: got %q, want 'custom prompt'", res.SystemPrompt)
	}

	// Update back to false
	if err := config.UpdateChatNoticeConfig(cfgPath, false, true); err != nil {
		t.Fatal(err)
	}
	res2, err := config.Load(config.LoadOptions{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if res2.ShowIterationNotices {
		t.Fatalf("after second update: ShowIterationNotices = true, want false")
	}
	if !res2.ShowPromptCacheNotices {
		t.Fatalf("after second update: ShowPromptCacheNotices = false, want true")
	}
}
