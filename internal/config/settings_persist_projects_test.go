package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

type projectConfigTestStruct struct {
	EnvFile   string `toml:"env_file"`
	Worktrees struct {
		BranchPrefix string `toml:"branch_prefix"`
	} `toml:"worktrees"`
	Chat struct {
		SystemPrompt    string  `toml:"system_prompt"`
		Temperature     float64 `toml:"temperature"`
		MaxTokens       int     `toml:"max_tokens"`
		MaxPromptTokens int     `toml:"max_prompt_tokens"`
		MaxSteps        int     `toml:"max_steps"`
	} `toml:"chat"`
	Tools struct {
		RunTimeoutSec int `toml:"run_timeout_seconds"`
	} `toml:"tools"`
	Subagents struct {
		StoreBackend string `toml:"store_backend"`
		StorePath    string `toml:"store_path"`
	} `toml:"subagents"`
	Harness struct {
		Sandbox bool `toml:"sandbox"`
	} `toml:"harness"`
	Privacy struct {
		RedactToolArgs bool `toml:"redact_tool_args"`
	} `toml:"privacy"`
}

func assertProjectConfigFields(t *testing.T, raw projectConfigTestStruct) {
	t.Helper()
	if raw.EnvFile != ".env.local" || raw.Worktrees.BranchPrefix != "feat/" {
		t.Errorf("env/worktree mismatch: %+v", raw)
	}
	if raw.Chat.SystemPrompt != "Custom prompt" || raw.Chat.Temperature != 0.7 {
		t.Errorf("chat prompt/temp mismatch: %+v", raw.Chat)
	}
	if raw.Chat.MaxTokens != 4096 || raw.Chat.MaxPromptTokens != 8192 || raw.Chat.MaxSteps != 25 {
		t.Errorf("chat limits mismatch: %+v", raw.Chat)
	}
	if raw.Tools.RunTimeoutSec != 1200 || raw.Subagents.StoreBackend != "sqlite" {
		t.Errorf("tools/subagents mismatch: %+v", raw)
	}
	if raw.Subagents.StorePath != ".mivia/custom.db" || raw.Harness.Sandbox || !raw.Privacy.RedactToolArgs {
		t.Errorf("flags mismatch: %+v", raw)
	}
}

func TestUpdateProjectConfig_NewFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "mivia.toml")

	ps := ProjectSettings{
		EnvFile:         ".env.local",
		BranchPrefix:    "feat/",
		SystemPrompt:    "Custom prompt",
		Temperature:     "0.7",
		MaxTokens:       "4096",
		MaxPromptTokens: "8192",
		MaxSteps:        "25",
		RunTimeoutSec:   1200,
		StoreBackend:    "sqlite",
		StorePath:       ".mivia/custom.db",
		Sandbox:         false,
		RedactToolArgs:  true,
	}

	if err := UpdateProjectConfig(cfgPath, ps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var raw projectConfigTestStruct
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v\ncontent: %s", err, string(data))
	}
	assertProjectConfigFields(t, raw)
}

func TestUpdateProjectConfig_PreservesExistingSectionsAndDeletesDefaults(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "mivia.toml")

	initial := `
[providers.ollama]
base_url = "http://localhost:11434"
default_model = "llama3.2"

[chat]
temperature = 0.5
max_tokens = 2048
max_prompt_tokens = 4096
max_steps = 10
system_prompt = "Initial prompt"
`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	ps := ProjectSettings{
		Temperature:     "default",
		MaxTokens:       "default",
		MaxPromptTokens: "default",
		MaxSteps:        "unlimited (0)",
	}

	if err := UpdateProjectConfig(cfgPath, ps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v\ncontent: %s", err, string(data))
	}

	providers, ok := raw["providers"].(map[string]any)
	if !ok || providers["ollama"] == nil {
		t.Errorf("expected [providers.ollama] preserved, got: %s", string(data))
	}

	chat, ok := raw["chat"].(map[string]any)
	if !ok {
		t.Fatalf("expected [chat] section, got: %s", string(data))
	}
	if chat["temperature"] != nil || chat["max_tokens"] != nil || chat["max_prompt_tokens"] != nil {
		t.Errorf("expected default keys removed, got %v", chat)
	}
	if chat["max_steps"] != int64(0) || chat["system_prompt"] != "Initial prompt" {
		t.Errorf("expected max_steps=0 and preserved prompt, got %v", chat)
	}
}

func TestUpdateProjectConfig_MaxStepsZeroAndDefault(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "mivia.toml")

	initial := `
[chat]
max_steps = 50
`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// 1. Test ps.MaxSteps = "0"
	ps := ProjectSettings{MaxSteps: "0"}
	if err := UpdateProjectConfig(cfgPath, ps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, _ := os.ReadFile(cfgPath)
	var raw map[string]any
	_ = toml.Unmarshal(data, &raw)
	chat := raw["chat"].(map[string]any)
	if chat["max_steps"] != int64(0) {
		t.Errorf("expected max_steps = 0, got %v", chat["max_steps"])
	}

	// 2. Test ps.MaxSteps = "default" -> removes key
	ps = ProjectSettings{MaxSteps: "default"}
	if err := UpdateProjectConfig(cfgPath, ps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, _ = os.ReadFile(cfgPath)
	var raw2 map[string]any
	_ = toml.Unmarshal(data, &raw2)
	chat = raw2["chat"].(map[string]any)
	if chat["max_steps"] != nil {
		t.Errorf("expected max_steps removed, got %v", chat["max_steps"])
	}
}

func TestUpdateProjectConfig_EmptyPath(t *testing.T) {
	if err := UpdateProjectConfig("", ProjectSettings{EnvFile: ".env"}); err == nil {
		t.Error("expected error for empty config path, got nil")
	}
}

func TestUpdateProjectConfig_PartialUpdates(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "mivia.toml")

	ps := ProjectSettings{
		Sandbox:        true,
		RedactToolArgs: true,
	}
	if err := UpdateProjectConfig(cfgPath, ps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var raw struct {
		Harness struct {
			Sandbox bool `toml:"sandbox"`
		} `toml:"harness"`
		Privacy struct {
			RedactToolArgs bool `toml:"redact_tool_args"`
		} `toml:"privacy"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !raw.Harness.Sandbox || !raw.Privacy.RedactToolArgs {
		t.Errorf("flags mismatch: %+v", raw)
	}
}
