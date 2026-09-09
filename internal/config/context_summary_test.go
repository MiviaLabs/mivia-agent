package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadContextConfigErr loads a workspace config body and returns the load
// error, or nil. loadContextConfig fails the test on an error, so a rejection
// contract needs its own helper.
func loadContextConfigErr(t *testing.T, body string) error {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mivia.toml")
	base := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n"
	if err := os.WriteFile(path, []byte(base+body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(LoadOptions{ConfigPath: path})
	return err
}

// TestContextSummaryDefaultsOn pins that an absent [context.summary] key
// loads and leaves nothing that can turn the summarizer off. Compaction
// drops messages permanently, so the summary is the only record of what was
// removed. The summarizer is always enabled; no resolved switch exists.
func TestContextSummaryDefaultsOn(t *testing.T) {
	got := loadContextConfig(t, "")
	if got.Summary.Enabled != nil {
		t.Fatalf("Summary.Enabled = %v, want nil: an absent key must resolve to no switch at all", *got.Summary.Enabled)
	}
}

// TestContextSummaryExplicitOptOutIsALoadError pins the migration: the
// summarizer is always enabled, so `enabled = false` no longer describes a
// supported configuration. Load refuses it and names the key, instead of
// silently re-enabling a provider call an operator deliberately stopped.
//
// This replaces the former TestContextSummaryExplicitOptOut, which asserted
// that `enabled = false` resolved to a false switch. That premise is gone
// with the switch. A load error is a harder contract than the boolean it
// replaces: the operator is told, once, exactly what changed.
func TestContextSummaryExplicitOptOutIsALoadError(t *testing.T) {
	err := loadContextConfigErr(t, "\n[context.summary]\nenabled = false\n")
	if err == nil {
		t.Fatal("[context.summary] enabled = false loaded without error; the summarizer is always enabled")
	}
	message := err.Error()
	for _, want := range []string{"[context.summary]", "enabled"} {
		if !strings.Contains(message, want) {
			t.Fatalf("load error = %q, want it to name %q", message, want)
		}
	}
}

// TestContextSummaryExplicitOptIn keeps the redundant-but-valid spelling
// working, so an existing config that says enabled = true still loads.
func TestContextSummaryExplicitOptIn(t *testing.T) {
	if err := loadContextConfigErr(t, "\n[context.summary]\nenabled = true\n"); err != nil {
		t.Fatalf("[context.summary] enabled = true failed to load: %v", err)
	}
}
