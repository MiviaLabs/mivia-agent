package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadContextConfig(t *testing.T, body string) ContextConfig {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mivia.toml")
	base := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n"
	if err := os.WriteFile(path, []byte(base+body), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return res.Context
}

// TestContextLimitsAreOperatorOwned pins that the durable ceilings are
// configuration, uncapped unless the operator says otherwise.
func TestContextLimitsAreOperatorOwned(t *testing.T) {
	if got := loadContextConfig(t, ""); got != (ContextConfig{}) {
		t.Fatalf("unconfigured context limits = %+v, want every bound uncapped", got)
	}
	got := loadContextConfig(t, "\n[context]\nmax_source_event_bytes = 1024\nmax_checkpoint_bytes = 2048\nmax_commit_events = 8\nmax_commit_event_bytes = 4096\nmax_session_state_bytes = 8192\nmax_export_bytes = 16384\n")
	want := ContextConfig{
		MaxSourceEventBytes: 1024, MaxCheckpointBytes: 2048, MaxCommitEvents: 8,
		MaxCommitEventBytes: 4096, MaxSessionStateBytes: 8192, MaxExportBytes: 16384,
	}
	if got != want {
		t.Fatalf("configured context limits = %+v, want %+v", got, want)
	}
}

// TestNegativeContextCeilingClampsToUncapped keeps a nonsensical ceiling from
// becoming the one thing these bounds exist to prevent: a refused turn.
func TestNegativeContextCeilingClampsToUncapped(t *testing.T) {
	got := loadContextConfig(t, "\n[context]\nmax_checkpoint_bytes = -1\nmax_source_event_bytes = -4096\n")
	if got.MaxCheckpointBytes != 0 || got.MaxSourceEventBytes != 0 {
		t.Fatalf("negative ceilings = %+v, want clamped to uncapped", got)
	}
}
