package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// intPtr is duplicated from internal/clichat.
func intPtr(n int) *int { return &n }

// loadPickerConfig is duplicated from internal/clichat.
func loadPickerConfig(t *testing.T) *config.Resolved {
	return loadPickerConfigWithEnv(t, "DEEPSEEK_API_KEY=picker-key\n")
}

// loadPickerConfigWithEnv is duplicated from internal/clichat.
func loadPickerConfigWithEnv(t *testing.T, envContents string) *config.Resolved {
	t.Helper()
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte(envContents), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mivia.toml")
	body := "env_file = \"" + filepath.ToSlash(env) + "\"\n\n" + `[provider]
name = "deepseek"

[providers.deepseek]
models = [
  { name = "deepseek/one", context_window_tokens = 128000 },
  { name = "deepseek/two", context_window_tokens = 128000 },
]

[providers.openrouter]
models = [
  { name = "openai/gpt-4o-mini", context_window_tokens = 128000 },
]

[chat]
max_tokens = 8192
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	return res
}
