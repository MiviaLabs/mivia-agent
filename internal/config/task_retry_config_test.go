package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTaskRetryConfigDefaultsToDisabled pins the safe default: with no
// [subagents.retry] section, MaxRetries stays 0 (disabled) - resolveSubagentConfig
// must never fill in a non-zero default for it, unlike SchemaRetryMax.
func TestTaskRetryConfigDefaultsToDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
models = [{name="deepseek-v4-flash", context_window_tokens=128000}]
[chat]
max_tokens = 8192
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.TaskRetry.MaxRetries != 0 {
		t.Fatalf("TaskRetry.MaxRetries = %d, want 0 (disabled by default)", res.Subagents.TaskRetry.MaxRetries)
	}
}

func TestTaskRetryConfigFromTOML(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
models = [{name="deepseek-v4-flash", context_window_tokens=128000}]
[chat]
max_tokens = 8192
[subagents.retry]
max_retries = 3
base_backoff_seconds = 0.5
max_backoff_seconds = 10
backoff_factor = 2.0
jitter_fraction = 0.25
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Subagents.TaskRetry
	if r.MaxRetries != 3 {
		t.Fatalf("MaxRetries = %d, want 3", r.MaxRetries)
	}
	if r.BaseBackoffSeconds != 0.5 {
		t.Fatalf("BaseBackoffSeconds = %v, want 0.5", r.BaseBackoffSeconds)
	}
	if r.MaxBackoffSeconds != 10 {
		t.Fatalf("MaxBackoffSeconds = %v, want 10", r.MaxBackoffSeconds)
	}
	if r.BackoffFactor != 2.0 {
		t.Fatalf("BackoffFactor = %v, want 2.0", r.BackoffFactor)
	}
	if r.JitterFraction != 0.25 {
		t.Fatalf("JitterFraction = %v, want 0.25", r.JitterFraction)
	}
}
