package config

import "testing"

func TestInspectRepositoryConfigDefaultsAndBounds(t *testing.T) {
	rc := resolveToolsConfig(ToolsConfig{})
	if rc.MaxInspectRepositoryBytes != 64<<10 {
		t.Fatalf("default MaxInspectRepositoryBytes = %d, want %d", rc.MaxInspectRepositoryBytes, 64<<10)
	}
	if err := validateToolResultBudgets(ToolsConfig{MaxInspectRepositoryBytes: MinInspectRepositoryBytes - 1}); err == nil {
		t.Fatal("expected rejection below floor")
	}
	if err := validateToolResultBudgets(ToolsConfig{MaxInspectRepositoryBytes: MaxInspectRepositoryBytesLimit + 1}); err == nil {
		t.Fatal("expected rejection above ceiling")
	}
	if err := validateToolResultBudgets(ToolsConfig{MaxInspectRepositoryBytes: MinInspectRepositoryBytes}); err != nil {
		t.Fatalf("floor should be accepted: %v", err)
	}
	if err := validateToolResultBudgets(ToolsConfig{MaxInspectRepositoryBytes: MaxInspectRepositoryBytesLimit}); err != nil {
		t.Fatalf("ceiling should be accepted: %v", err)
	}
}
