package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestLoadDefaultsDeepSeekFlash(t *testing.T) {
	_, err := Load(LoadOptions{AllowMissingConfig: true})
	if err == nil || !strings.Contains(err.Error(), "models must be non-empty") {
		t.Fatalf("missing config error = %v", err)
	}
}

func TestResolvedValidateRejectsUnsafeAPIKeyEnvironmentName(t *testing.T) {
	res := &Resolved{ProviderName: "deepseek", Model: "model", BaseURL: "https://example.test", APIKeyEnv: "KEY\nforged"}
	if err := res.Validate(); err == nil {
		t.Fatal("unsafe api_key_env accepted")
	}
}

func TestLoadTOMLAndEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	env := filepath.Join(dir, ".env")
	toml := `env_file = "` + filepath.ToSlash(env) + `"

[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 128000 }]
default_model = "deepseek-v4-pro"

[chat]
max_tokens = 8192
`
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("DEEPSEEK_API_KEY=secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != DeepSeekProModel {
		t.Fatalf("model: %s", res.Model)
	}
	if !res.APIKeySet || res.APIKey != "secret-key" {
		t.Fatalf("api key not resolved")
	}
}

func TestLoadZAIFromTOMLAndProviderOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(cfg, []byte("env_file = \""+filepath.ToSlash(env)+"\"\n\n[provider]\nname = \"deepseek\"\n\n[providers.zai]\nmodels = [{ name = \"glm-5.2\", context_window_tokens = 128000 }]\n\n[chat]\nmax_tokens = 8192\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("ZAI_API_KEY=zai-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg, ProviderOverride: "zai"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProviderName != "zai" || res.Model != "glm-5.2" || res.BaseURL != "https://api.z.ai/api/paas/v4" || res.APIKeyEnv != "ZAI_API_KEY" || !res.APIKeySet {
		t.Fatalf("resolved=%+v", res)
	}
}

func TestExampleConfigIncludesZAI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".mivia", "mivia.toml.example"))
	if err != nil {
		t.Fatal(err)
	}
	var file File
	if err := toml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	pc, ok := file.Providers["zai"]
	if !ok || len(pc.Models) != 1 || pc.Models[0].Name != "glm-5.2" || pc.APIKeyEnv != "ZAI_API_KEY" || pc.BaseURL != "https://api.z.ai/api/paas/v4" {
		t.Fatalf("zai config=%+v present=%v", pc, ok)
	}
}

func TestModelOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 128000 }]
[chat]
max_tokens = 8192
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg, ModelOverride: DeepSeekProModel})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != DeepSeekProModel {
		t.Fatalf("model: %s", res.Model)
	}
}

func TestManagedModelsResolutionAndValidation(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		body     string
		override string
		want     string
		wantErr  string
	}{
		{
			name: "first model is the managed default",
			body: "models = [{name=\"A\", context_window_tokens=128000}, {name=\"B\", context_window_tokens=128000}]\n[chat]\nmax_tokens=8192\n",
			want: "A",
		},
		{
			name: "configured default wins",
			body: "models = [{name=\"A\", context_window_tokens=128000}, {name=\"B\", context_window_tokens=128000}]\ndefault_model = \" B \"\n[chat]\nmax_tokens=8192\n",
			want: "B",
		},
		{
			name:     "managed override must be listed",
			body:     "models = [{name=\"A\", context_window_tokens=128000}, {name=\"B\", context_window_tokens=128000}]\n[chat]\nmax_tokens=8192\n",
			override: "Z",
			wantErr:  "--model is not in models (A, B)",
		},
		{
			name:    "configured default must be listed",
			body:    "models = [{name=\"A\", context_window_tokens=128000}, {name=\"B\", context_window_tokens=128000}]\ndefault_model = \"Z\"\n[chat]\nmax_tokens=8192\n",
			wantErr: "default_model is not in models (A, B)",
		},
		{
			name:     "empty catalog rejects an override",
			body:     "default_model = \"custom\"\n",
			override: "anything",
			wantErr:  "models must be non-empty",
		},
		{
			name:     "empty catalog rejects a long override",
			body:     "default_model = \"custom\"\n",
			override: strings.Repeat("x", 257),
			wantErr:  "models must be non-empty",
		},
		{
			name:    "empty declared entry reports its source index",
			body:    "models = [{name=\"A\", context_window_tokens=128000}, {name=\"A\", context_window_tokens=128000}, {name=\"\", context_window_tokens=128000}]\n[chat]\nmax_tokens=8192\n",
			wantErr: "models[1] is a duplicate",
		},
		{
			name:    "control characters are rejected without echoing the value",
			body:    "models = [{name=\"A\\u001b]52;c;unsafe\", context_window_tokens=128000}]\n[chat]\nmax_tokens=8192\n",
			wantErr: "models[0] is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "mivia.toml")
			contents := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\n" + tt.body
			if err := os.WriteFile(cfg, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := Load(LoadOptions{ConfigPath: cfg, ModelOverride: tt.override})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "unsafe") {
					t.Fatalf("error exposed rejected model: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Model != tt.want {
				t.Fatalf("model = %q, want %q", res.Model, tt.want)
			}
		})
	}
}

func TestLegacyModelKeyIsRejected(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mivia.toml")
	if err := os.WriteFile(cfg, []byte("[provider]\nname = \"deepseek\"\n[providers.deepseek]\nmodels = [{name=\"declared\", context_window_tokens=128000}]\nmodel = \"legacy\"\n[chat]\nmax_tokens=8192\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(LoadOptions{ConfigPath: cfg}); err == nil || !strings.Contains(err.Error(), "model is no longer supported") {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectHTTPBaseURL(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
base_url = "http://127.0.0.1:9/v1"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(LoadOptions{ConfigPath: cfg})
	if err == nil {
		t.Fatal("expected https error")
	}
}

func TestEffectiveTimeoutSec(t *testing.T) {
	if got := EffectiveTimeoutSec(0); got != DefaultOrchestrationTimeoutSec {
		t.Fatalf("zero config: got %d want %d", got, DefaultOrchestrationTimeoutSec)
	}
	if got := EffectiveTimeoutSec(120); got != 120 {
		t.Fatalf("configured: got %d want 120", got)
	}
	if got := EffectiveTimeoutSec(60, 0, 300, 90); got != 300 {
		t.Fatalf("max override: got %d want 300", got)
	}
	if got := EffectiveTimeoutSec(0, 0); got != DefaultOrchestrationTimeoutSec {
		t.Fatalf("all zero: got %d want ceiling", got)
	}
}

func TestPrivacyRedactToolArgsDefaultOff(t *testing.T) {
	t.Setenv("MIVIA_REDACT_TOOL_ARGS", "")
	// Unset for real — Setenv empty still sets; use clear
	os.Unsetenv("MIVIA_REDACT_TOOL_ARGS")
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Privacy.RedactToolArgs {
		t.Fatal("redact_tool_args must default false")
	}
}

func TestPrivacyRedactToolArgsEnvOn(t *testing.T) {
	t.Setenv("MIVIA_REDACT_TOOL_ARGS", "1")
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Privacy.RedactToolArgs {
		t.Fatal("env should enable redaction")
	}
}

func TestPrivacyRedactToolArgsTOML(t *testing.T) {
	os.Unsetenv("MIVIA_REDACT_TOOL_ARGS")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
models = [{name="deepseek-v4-flash", context_window_tokens=128000}]
[chat]
max_tokens = 8192
[privacy]
redact_tool_args = true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Privacy.RedactToolArgs {
		t.Fatal("toml should enable redaction")
	}
}

func TestResolveToolsConfig_RunAllowlistExclusive(t *testing.T) {
	// When both RunAllowlist AND RunAllowlistOnly are set, RunAllowlistOnly wins
	// and RunAllowlist is reset to nil.
	tc := resolveToolsConfig(ToolsConfig{
		RunAllowlist:     []string{"echo", "cat"},
		RunAllowlistOnly: []string{"git", "make"},
	})
	if len(tc.RunAllowlist) != 0 {
		t.Errorf("RunAllowlist should be cleared when RunAllowlistOnly is set: got %v", tc.RunAllowlist)
	}
	if len(tc.RunAllowlistOnly) != 2 {
		t.Errorf("RunAllowlistOnly should be preserved: got %v", tc.RunAllowlistOnly)
	}
	if tc.RunAllowlistOnly[0] != "git" || tc.RunAllowlistOnly[1] != "make" {
		t.Errorf("RunAllowlistOnly values changed: got %v", tc.RunAllowlistOnly)
	}
}

func TestResolveToolsConfig_EnvAllowlistExclusive(t *testing.T) {
	// When both EnvAllowlist AND EnvAllowlistOnly are set, EnvAllowlistOnly wins
	// and EnvAllowlist is reset to nil.
	tc := resolveToolsConfig(ToolsConfig{
		EnvAllowlist:     []string{"MY_VAR", "FOO"},
		EnvAllowlistOnly: []string{"GIT_*", "PATH"},
	})
	if len(tc.EnvAllowlist) != 0 {
		t.Errorf("EnvAllowlist should be cleared when EnvAllowlistOnly is set: got %v", tc.EnvAllowlist)
	}
	if len(tc.EnvAllowlistOnly) != 2 {
		t.Errorf("EnvAllowlistOnly should be preserved: got %v", tc.EnvAllowlistOnly)
	}
	if tc.EnvAllowlistOnly[0] != "GIT_*" || tc.EnvAllowlistOnly[1] != "PATH" {
		t.Errorf("EnvAllowlistOnly values changed: got %v", tc.EnvAllowlistOnly)
	}
}

func TestResolveToolsConfig_RunAllowlistOnlyWithoutConflict(t *testing.T) {
	// When only RunAllowlistOnly is set (no RunAllowlist), it should be preserved.
	tc := resolveToolsConfig(ToolsConfig{
		RunAllowlistOnly: []string{"go", "python"},
	})
	if len(tc.RunAllowlistOnly) != 2 {
		t.Errorf("RunAllowlistOnly should be preserved: got %v", tc.RunAllowlistOnly)
	}
	if tc.RunAllowlistOnly[0] != "go" || tc.RunAllowlistOnly[1] != "python" {
		t.Errorf("RunAllowlistOnly values changed: got %v", tc.RunAllowlistOnly)
	}
	// RunAllowlist should remain nil/empty.
	if len(tc.RunAllowlist) != 0 {
		t.Errorf("RunAllowlist should be empty: got %v", tc.RunAllowlist)
	}
}

func TestResolveToolsConfig_EnvAllowlistOnlyWithoutConflict(t *testing.T) {
	// When only EnvAllowlistOnly is set (no EnvAllowlist), it should be preserved.
	tc := resolveToolsConfig(ToolsConfig{
		EnvAllowlistOnly: []string{"CI_*", "NODE_*"},
	})
	if len(tc.EnvAllowlistOnly) != 2 {
		t.Errorf("EnvAllowlistOnly should be preserved: got %v", tc.EnvAllowlistOnly)
	}
	if tc.EnvAllowlistOnly[0] != "CI_*" || tc.EnvAllowlistOnly[1] != "NODE_*" {
		t.Errorf("EnvAllowlistOnly values changed: got %v", tc.EnvAllowlistOnly)
	}
	// EnvAllowlist should remain nil/empty.
	if len(tc.EnvAllowlist) != 0 {
		t.Errorf("EnvAllowlist should be empty: got %v", tc.EnvAllowlist)
	}
}

func TestResolveToolsConfig_BothEmptyNoConflict(t *testing.T) {
	// When neither Allowlist nor AllowlistOnly are set, nothing should change.
	tc := resolveToolsConfig(ToolsConfig{})
	if len(tc.RunAllowlist) != 0 {
		t.Errorf("RunAllowlist should be empty: got %v", tc.RunAllowlist)
	}
	if len(tc.RunAllowlistOnly) != 0 {
		t.Errorf("RunAllowlistOnly should be empty: got %v", tc.RunAllowlistOnly)
	}
	if len(tc.EnvAllowlist) != 0 {
		t.Errorf("EnvAllowlist should be empty: got %v", tc.EnvAllowlist)
	}
	if len(tc.EnvAllowlistOnly) != 0 {
		t.Errorf("EnvAllowlistOnly should be empty: got %v", tc.EnvAllowlistOnly)
	}
}

func TestSubagentConfigDefaults(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	// All bounds default to 0 (unlimited).
	if res.Subagents.MaxWorkers != 0 {
		t.Fatalf("MaxWorkers: got %d want 0 (unlimited)", res.Subagents.MaxWorkers)
	}
	if res.Subagents.MaxDepth != 0 {
		t.Fatalf("MaxDepth: got %d want 0 (unlimited)", res.Subagents.MaxDepth)
	}
	if res.Subagents.MaxFanout != 0 {
		t.Fatalf("MaxFanout: got %d want 0 (unlimited)", res.Subagents.MaxFanout)
	}
	if res.Subagents.DefaultTimeout != 0 {
		t.Fatalf("DefaultTimeout: got %d want 0", res.Subagents.DefaultTimeout)
	}
	if res.Subagents.SystemPrompt != "" {
		t.Fatalf("SystemPrompt should be empty at config level (dispatcher resolves it)")
	}
}

func TestSubagentConfigFromTOML(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
models = [{name="deepseek-v4-flash", context_window_tokens=128000}]
[chat]
max_tokens = 8192
[subagents]
max_workers = 8
max_depth = 5
max_fanout = 32
default_timeout_seconds = 120
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.MaxWorkers != 8 {
		t.Fatalf("MaxWorkers: got %d want 8", res.Subagents.MaxWorkers)
	}
	if res.Subagents.MaxDepth != 5 {
		t.Fatalf("MaxDepth: got %d want 5", res.Subagents.MaxDepth)
	}
	if res.Subagents.MaxFanout != 32 {
		t.Fatalf("MaxFanout: got %d want 32", res.Subagents.MaxFanout)
	}
	if res.Subagents.DefaultTimeout != 120 {
		t.Fatalf("DefaultTimeout: got %v want 120s", res.Subagents.DefaultTimeout)
	}
}
