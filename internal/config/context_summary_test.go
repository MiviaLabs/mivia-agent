package config

import "testing"

// TestContextSummaryFlagDefaultsOff pins that the LLM summary stays disabled
// unless the operator turns it on: a workspace that configures nothing keeps
// the structural-only compaction behavior.
func TestContextSummaryFlagDefaultsOff(t *testing.T) {
	if got := loadContextConfig(t, ""); got.Summary.Enabled {
		t.Fatal("unconfigured [context.summary] enabled the LLM summary")
	}
}

// TestContextSummaryFlagParses pins the smallest config that opens the summary
// policy gate: [context.summary] enabled = true.
func TestContextSummaryFlagParses(t *testing.T) {
	got := loadContextConfig(t, "\n[context.summary]\nenabled = true\n")
	if !got.Summary.Enabled {
		t.Fatal("[context.summary] enabled = true did not reach the resolved context config")
	}
}
