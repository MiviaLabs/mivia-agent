package config

import "testing"

// TestContextSummaryDefaultsOn pins the opt-out default. Compaction drops
// messages permanently, so the summary is the only record of what was
// removed; a workspace that configures nothing must not silently lose it.
// This was opt-in, which meant every unconfigured workspace compacted with no
// summary and no signal - the reported "compaction does nothing" symptom.
func TestContextSummaryDefaultsOn(t *testing.T) {
	if got := loadContextConfig(t, ""); !got.Summary.SummaryEnabled() {
		t.Fatal("unconfigured [context.summary] left the LLM summary disabled")
	}
}

// TestContextSummaryExplicitOptOut pins that the escape hatch still works: an
// explicit false must be honored, which is why the field is a pointer - a
// plain bool cannot tell "absent" from "set to false".
func TestContextSummaryExplicitOptOut(t *testing.T) {
	got := loadContextConfig(t, "\n[context.summary]\nenabled = false\n")
	if got.Summary.SummaryEnabled() {
		t.Fatal("[context.summary] enabled = false did not disable the summary")
	}
}

// TestContextSummaryExplicitOptIn keeps the redundant-but-valid spelling
// working, so an existing config that says enabled = true is unaffected.
func TestContextSummaryExplicitOptIn(t *testing.T) {
	got := loadContextConfig(t, "\n[context.summary]\nenabled = true\n")
	if !got.Summary.SummaryEnabled() {
		t.Fatal("[context.summary] enabled = true did not reach the resolved context config")
	}
}
