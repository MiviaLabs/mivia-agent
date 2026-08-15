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
	// Summary is behavior policy, not a durable bound, and resolves to a
	// decided value (opt-out default). Compare the ceilings only.
	got := loadContextConfig(t, "")
	got.Summary = ContextSummaryConfig{}
	if got != (ContextConfig{}) {
		t.Fatalf("unconfigured context limits = %+v, want every bound uncapped", got)
	}
	got = loadContextConfig(t, "\n[context]\nmax_source_event_bytes = 1024\nmax_checkpoint_bytes = 2048\nmax_commit_events = 8\nmax_commit_event_bytes = 4096\nmax_session_state_bytes = 8192\nmax_export_bytes = 16384\nsummary_metadata_bytes = 24576\ncheckpoint_metadata_bytes = 32768\n")
	got.Summary = ContextSummaryConfig{}
	want := ContextConfig{
		MaxSourceEventBytes: 1024, MaxCheckpointBytes: 2048, MaxCommitEvents: 8,
		MaxCommitEventBytes: 4096, MaxSessionStateBytes: 8192, MaxExportBytes: 16384,
		SummaryMetadataBytes: 24576, CheckpointMetadataBytes: 32768,
	}
	if got != want {
		t.Fatalf("configured context limits = %+v, want %+v", got, want)
	}
}

// TestSummaryAndCheckpointMetadataLimitsDefaultUncapped pins that the
// model-generated summary envelope and checkpoint metadata column are uncapped
// unless the operator sets a bound - the compiled-in defaults remain fallback
// ceilings, never authoritative.
func TestSummaryAndCheckpointMetadataLimitsDefaultUncapped(t *testing.T) {
	got := loadContextConfig(t, "\n[context]\nsummary_metadata_bytes = 4096\n")
	if got.SummaryMetadataBytes != 4096 || got.CheckpointMetadataBytes != 0 {
		t.Fatalf("summary metadata limit = %d, checkpoint metadata limit = %d; want 4096 and uncapped (0)", got.SummaryMetadataBytes, got.CheckpointMetadataBytes)
	}
	got = loadContextConfig(t, "\n[context]\ncheckpoint_metadata_bytes = 8192\n")
	if got.CheckpointMetadataBytes != 8192 || got.SummaryMetadataBytes != 0 {
		t.Fatalf("checkpoint metadata limit = %d, summary metadata limit = %d; want 8192 and uncapped (0)", got.CheckpointMetadataBytes, got.SummaryMetadataBytes)
	}
}

// TestNegativeContextCeilingClampsToUncapped keeps a nonsensical ceiling from
// becoming the one thing these bounds exist to prevent: a refused turn.
func TestNegativeContextCeilingClampsToUncapped(t *testing.T) {
	got := loadContextConfig(t, "\n[context]\nmax_checkpoint_bytes = -1\nmax_source_event_bytes = -4096\nsummary_metadata_bytes = -1\ncheckpoint_metadata_bytes = -2\n")
	if got.MaxCheckpointBytes != 0 || got.MaxSourceEventBytes != 0 {
		t.Fatalf("negative ceilings = %+v, want clamped to uncapped", got)
	}
	if got.SummaryMetadataBytes != 0 || got.CheckpointMetadataBytes != 0 {
		t.Fatalf("negative metadata ceilings = %+v, want clamped to uncapped", got)
	}
}
