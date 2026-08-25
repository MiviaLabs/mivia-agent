package config

import (
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestLoadDefaultsDeepSeekFlash pins that a load with no discoverable config
// fails closed rather than inventing a provider.
//
// It isolates HOME and MIVIA_CONFIG first. DefaultConfigCandidates searches
// $MIVIA_CONFIG, then <cwd>/.mivia/mivia.toml, then ~/.mivia/mivia.toml, so
// without isolation this test asserted a property of the developer's home
// directory: it passed only on a machine with no user config, and failed for
// anyone using the documented user-level setup.
func TestLoadDefaultsDeepSeekFlash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIVIA_CONFIG", "")
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

func TestResolvedValidateRejectsUnsafeSecretPathException(t *testing.T) {
	res := &Resolved{ProviderName: "deepseek", Model: "model", BaseURL: "https://example.test", APIKeyEnv: "KEY", Tools: ToolsConfig{SecretPathPatterns: []string{".env"}, SecretPathExceptions: []string{"../.env.example"}}}
	err := res.Validate()
	if err == nil || !strings.Contains(err.Error(), "secret path") {
		t.Fatalf("Validate() error = %v, want secret path error", err)
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
	// Membership, not count: the example documents optional per-model keys by
	// showing a configured model beside an unset one, so pinning the catalog
	// size here would fight the documentation instead of checking the endpoint
	// and credential wiring this test is about.
	if !ok || !slices.Contains(modelNames(pc.Models), "glm-5.2") ||
		pc.APIKeyEnv != "ZAI_API_KEY" || pc.BaseURL != "https://api.z.ai/api/paas/v4" {
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

// Timeout-budget resolution tests (EffectiveTimeoutSec, its overflow clamp,
// and RequestedTimeoutSec) live in timeout_test.go.
func TestModelCatalogCarriesOutputCeiling(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "small", context_window_tokens = 128000, max_output_tokens = 4096 }]

[chat]
max_tokens = 8192
`, "DEEPSEEK_API_KEY=test-key\n")
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.ModelProfiles[0].MaxOutputTokens; got != 4096 {
		t.Fatalf("model output ceiling = %d, want 4096", got)
	}
	if got := res.ModelCatalog()[0].Models[0].MaxOutputTokens; got != 4096 {
		t.Fatalf("catalog output ceiling = %d, want 4096", got)
	}
}

func TestModelCatalogRejectsInvalidOutputCeiling(t *testing.T) {
	for _, value := range []string{"-1", "128000"} {
		t.Run(value, func(t *testing.T) {
			path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "small", context_window_tokens = 128000, max_output_tokens = `+value+` }]

[chat]
max_tokens = 8192
`, "DEEPSEEK_API_KEY=test-key\n")
			if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
				t.Fatal("invalid model output ceiling was accepted")
			}
		})
	}
}

func TestPrivacyRedactToolArgsDefaultOff(t *testing.T) {
	t.Setenv("MIVIA_REDACT_TOOL_ARGS", "")
	// Unset for real - Setenv empty still sets; use clear
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

func TestMessagingConfigDefaults(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Subagents.Messaging
	if !m.IsEnabled() {
		t.Fatal("messaging should be enabled by default")
	}
	if m.MaxBodyBytes != 2048 {
		t.Fatalf("MaxBodyBytes = %d, want 2048", m.MaxBodyBytes)
	}
	if m.MaxMessagesPerTask != 32 {
		t.Fatalf("MaxMessagesPerTask = %d, want 32", m.MaxMessagesPerTask)
	}
	if m.MailboxCapacity != 32 {
		t.Fatalf("MailboxCapacity = %d, want 32", m.MailboxCapacity)
	}
	if m.MaxPendingQuestions != 1 {
		t.Fatalf("MaxPendingQuestions = %d, want 1", m.MaxPendingQuestions)
	}
	// Routing defaults (plan 53.04) — always active with policy mode.
	if m.Routing.Mode != "policy" {
		t.Fatalf("Routing.Mode = %q, want policy", m.Routing.Mode)
	}
	if m.Routing.MaxAsksPerTask != 4 {
		t.Fatalf("MaxAsksPerTask = %d, want 4", m.Routing.MaxAsksPerTask)
	}
	if m.Routing.MaxReferralDepth != 2 {
		t.Fatalf("MaxReferralDepth = %d, want 2", m.Routing.MaxReferralDepth)
	}
	if m.Routing.MaxReferralSpawnsPerRun != 4 {
		t.Fatalf("MaxReferralSpawnsPerRun = %d, want 4", m.Routing.MaxReferralSpawnsPerRun)
	}
}

func TestMessagingConfigFromTOML(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
models = [{name="deepseek-v4-flash", context_window_tokens=128000}]
[chat]
max_tokens = 8192
[subagents.messaging]
enabled = false
max_body_bytes = 512
max_messages_per_task = 4
mailbox_capacity = 8
max_pending_questions = 2
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Subagents.Messaging
	// Messaging is always enabled; enabled=false in TOML is ignored.
	if !m.IsEnabled() {
		t.Fatal("messaging must remain enabled even when TOML says enabled=false")
	}
	if m.MaxBodyBytes != 512 {
		t.Fatalf("MaxBodyBytes = %d, want 512", m.MaxBodyBytes)
	}
	if m.MaxMessagesPerTask != 4 {
		t.Fatalf("MaxMessagesPerTask = %d, want 4", m.MaxMessagesPerTask)
	}
	if m.MailboxCapacity != 8 {
		t.Fatalf("MailboxCapacity = %d, want 8", m.MailboxCapacity)
	}
	if m.MaxPendingQuestions != 2 {
		t.Fatalf("MaxPendingQuestions = %d, want 2", m.MaxPendingQuestions)
	}
}

func TestMessagingConfigEnabledTrueExplicit(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
models = [{name="deepseek-v4-flash", context_window_tokens=128000}]
[chat]
max_tokens = 8192
[subagents.messaging]
enabled = true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Subagents.Messaging.IsEnabled() {
		t.Fatal("enabled=true must stick")
	}
	// Unset numeric knobs still get defaults.
	if res.Subagents.Messaging.MaxBodyBytes != 2048 {
		t.Fatalf("MaxBodyBytes default = %d", res.Subagents.Messaging.MaxBodyBytes)
	}
}

// TestLoadRejectsMalformedBaseURL is the regression for DC-9/DC-13: the old
// prefix-only check accepted these values and every request failed at runtime.
// Each must now be refused at load, without echoing the raw value.
func TestLoadRejectsMalformedBaseURL(t *testing.T) {
	oversized := "https://" + strings.Repeat("a", 10<<10) + ".example.com/v1"
	tests := []struct {
		name      string
		tomlValue string // spelling inside the TOML file
		rawValue  string // decoded value; the error must not echo it
	}{
		{name: "port-only host", tomlValue: "https://:443", rawValue: "https://:443"},
		{name: "empty authority", tomlValue: "https:///v1", rawValue: "https:///v1"},
		{name: "embedded space in host", tomlValue: "https://exa mple.com", rawValue: "https://exa mple.com"},
		// \u007f is the TOML escape for DEL (precedent: the models test).
		{name: "control character in host", tomlValue: "https://exa\\u007fmple.com", rawValue: "https://exa\u007fmple.com"},
		{name: "userinfo carries credentials", tomlValue: "https://user:pass@api.deepseek.com/v1", rawValue: "https://user:pass@api.deepseek.com/v1"},
		{name: "fragment is not an endpoint", tomlValue: "https://api.deepseek.com/v1#frag", rawValue: "https://api.deepseek.com/v1#frag"},
		{name: "oversized value", tomlValue: oversized, rawValue: oversized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(LoadOptions{ConfigPath: writeBaseURLConfig(t, tt.tomlValue)})
			if err == nil {
				t.Fatalf("Load accepted malformed base_url %q", tt.rawValue)
			}
			if !strings.Contains(err.Error(), "base_url") {
				t.Fatalf("error does not name base_url: %v", err)
			}
			if strings.Contains(err.Error(), tt.rawValue) {
				t.Fatalf("error echoed the raw base_url value: %v", err)
			}
		})
	}
}

func writeBaseURLConfig(t *testing.T, baseURL string) string {
	t.Helper()
	return writeCatalogConfig(t, "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nbase_url = \""+baseURL+"\"\nmodels = [{ name = \"deepseek-v4-flash\", context_window_tokens = 128000 }]\n\n[chat]\nmax_tokens = 8192\n", "DEEPSEEK_API_KEY=test-key\n")
}

// TestLoadAcceptsValidBaseURL guards an over-strict fix: a valid https URL
// keeps loading (trimmed) and an omitted base_url keeps the registry default.
func TestLoadAcceptsValidBaseURL(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeBaseURLConfig(t, "https://api.deepseek.com/v1/")})
	if err != nil {
		t.Fatalf("valid https base_url rejected: %v", err)
	}
	if res.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("BaseURL = %q, want trimmed %q", res.BaseURL, "https://api.deepseek.com/v1")
	}

	res, err = Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatalf("empty base_url rejected: %v", err)
	}
	if res.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("empty base_url did not default to the registry URL: %q", res.BaseURL)
	}
}

// TestValidateBaseURLRejectsHostlessUnit pins the refusal of empty and
// hostless values. "https://" was already refused at load (resolveLoaded
// trims it to "https:"), so it stays a guard case, not a RED case.
func TestValidateBaseURLRejectsHostlessUnit(t *testing.T) {
	if err := validateBaseURL("", "deepseek"); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("validateBaseURL(\"\") = %v", err)
	}
	if err := validateBaseURL("https://", "deepseek"); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("validateBaseURL(\"https://\") = %v", err)
	}

	if _, err := Load(LoadOptions{ConfigPath: writeBaseURLConfig(t, "https://")}); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("hostless https base_url accepted at load: %v", err)
	}
}

// TestLoadHTTPBaseURLEnvGate pins the http gate: refused without
// MIVIA_ALLOW_INSECURE_HTTP=1, accepted with it, never loosening structure.
//
// Uses a non-loopback host: a loopback http base_url (127.0.0.1/::1/
// localhost) is accepted unconditionally regardless of this env var - every
// builtin provider now gets a verified-loopback dial pin at construction
// (provider.NewForProvider), not just ollama - so it would not exercise the
// gate this test pins. See TestValidateBaseURLOllamaLoopbackRelaxation for
// the loopback exception itself.
func TestLoadHTTPBaseURLEnvGate(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "")
	if _, err := Load(LoadOptions{ConfigPath: writeBaseURLConfig(t, "http://example.com:9/v1")}); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("http base_url accepted without MIVIA_ALLOW_INSECURE_HTTP=1: %v", err)
	}

	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	res, err := Load(LoadOptions{ConfigPath: writeBaseURLConfig(t, "http://example.com:9/v1")})
	if err != nil {
		t.Fatalf("http base_url rejected with MIVIA_ALLOW_INSECURE_HTTP=1: %v", err)
	}
	if res.BaseURL != "http://example.com:9/v1" {
		t.Fatalf("BaseURL = %q", res.BaseURL)
	}

	if _, err := Load(LoadOptions{ConfigPath: writeBaseURLConfig(t, "http://")}); err == nil {
		t.Fatal("hostless http base_url accepted with MIVIA_ALLOW_INSECURE_HTTP=1")
	}
}

// TestValidateBaseURLLengthBoundary pins the DC-6 length guard at its edge:
// exactly maxBaseURLLength loads, one byte past the cap is refused.
func TestValidateBaseURLLengthBoundary(t *testing.T) {
	host := strings.Repeat("a", maxBaseURLLength-len("https://")-len(".example.com/v1"))
	exact := "https://" + host + ".example.com/v1"
	over := "https://" + host + "a.example.com/v1"
	if len(exact) != maxBaseURLLength || len(over) != maxBaseURLLength+1 {
		t.Fatalf("boundary fixtures: exact=%d over=%d", len(exact), len(over))
	}

	if err := validateBaseURL(exact, "deepseek"); err != nil {
		t.Fatalf("validateBaseURL(exactly maxBaseURLLength) = %v", err)
	}
	if err := validateBaseURL(over, "deepseek"); err == nil {
		t.Fatal("validateBaseURL(maxBaseURLLength+1) accepted")
	}

	if _, err := Load(LoadOptions{ConfigPath: writeBaseURLConfig(t, exact)}); err != nil {
		t.Fatalf("Load rejected base_url of exactly maxBaseURLLength: %v", err)
	}
	if _, err := Load(LoadOptions{ConfigPath: writeBaseURLConfig(t, over)}); err == nil {
		t.Fatal("Load accepted base_url of maxBaseURLLength+1")
	}
}

// FuzzValidateBaseURLNeverPanics asserts validateBaseURL never panics and that
// an accepted value always satisfies the structural contract (absolute, https
// or http, non-empty host, no userinfo, no fragment, bounded length). The
// property is independent of the http gate, which only narrows the set.
func FuzzValidateBaseURLNeverPanics(f *testing.F) {
	seedValidateBaseURLCorpus(f)
	f.Fuzz(func(t *testing.T, raw string) {
		if err := validateBaseURL(raw, "deepseek"); err != nil {
			return
		}
		u, perr := url.Parse(raw)
		if perr != nil {
			t.Fatalf("accepted base_url does not parse: %v", perr)
		}
		if !u.IsAbs() || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			t.Fatalf("accepted base_url violates the structural contract")
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			t.Fatalf("accepted base_url has scheme %q", u.Scheme)
		}
		if len(raw) > maxBaseURLLength {
			t.Fatalf("accepted base_url exceeds maxBaseURLLength")
		}
	})
}

// seedValidateBaseURLCorpus primes the target: empty, malformed, and valid.
func seedValidateBaseURLCorpus(f *testing.F) {
	f.Add("")
	f.Add("https://")
	f.Add("https://:443")
	f.Add("https:///v1")
	f.Add("https://exa mple.com")
	f.Add("https://exa\x7fmple.com")
	f.Add("https://user:pass@api.deepseek.com/v1")
	f.Add("https://api.deepseek.com/v1#frag")
	f.Add("ftp://example.com")
	f.Add("https://api.deepseek.com/v1")
	f.Add("http://127.0.0.1:9/v1")
	f.Add("https://" + strings.Repeat("a", maxBaseURLLength+1) + ".example.com/v1")
}
