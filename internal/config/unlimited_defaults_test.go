package config

import (
	"testing"
)

// TestResolveSubagentConfigZeroStaysZero verifies that a SubagentConfig with
// all numeric fields set to 0 remains 0 after resolution — zero means unlimited.
func TestResolveSubagentConfigZeroStaysZero(t *testing.T) {
	cfg := resolveSubagentConfig(SubagentConfig{
		MaxWorkers:             0,
		MaxDepth:               0,
		MaxFanout:              0,
		DefaultTimeout:         0,
		DefaultBudget:          0,
		NestedSteps:            0,
		MaxAuditRounds:         0,
		HandleRetentionSeconds: 0,
	})

	if cfg.MaxWorkers != 0 {
		t.Errorf("MaxWorkers: got %d, want 0 (unlimited)", cfg.MaxWorkers)
	}
	if cfg.MaxDepth != 0 {
		t.Errorf("MaxDepth: got %d, want 0 (unlimited)", cfg.MaxDepth)
	}
	if cfg.MaxFanout != 0 {
		t.Errorf("MaxFanout: got %d, want 0 (unlimited)", cfg.MaxFanout)
	}
	if cfg.DefaultTimeout != 0 {
		t.Errorf("DefaultTimeout: got %d, want 0 (unlimited)", cfg.DefaultTimeout)
	}
	if cfg.DefaultBudget != 0 {
		t.Errorf("DefaultBudget: got %d, want 0 (unlimited)", cfg.DefaultBudget)
	}
	if cfg.NestedSteps != 0 {
		t.Errorf("NestedSteps: got %d, want 0 (unlimited)", cfg.NestedSteps)
	}
	if cfg.MaxAuditRounds != 0 {
		t.Errorf("MaxAuditRounds: got %d, want 0 (unlimited)", cfg.MaxAuditRounds)
	}
	if cfg.HandleRetentionSeconds != 0 {
		t.Errorf("HandleRetentionSeconds: got %d, want 0 (unlimited)", cfg.HandleRetentionSeconds)
	}
}

// TestResolveToolsConfigZeroStaysZero verifies that a ToolsConfig with all
// numeric caps set to 0 remains 0 after resolution — zero means uncapped.
func TestResolveToolsConfigZeroStaysZero(t *testing.T) {
	tc := resolveToolsConfig(ToolsConfig{
		MaxReadBytes:      0,
		MaxWriteKB:        0,
		MaxOutputBytes:    0,
		MaxListDirEntries: 0,
	})

	if tc.MaxReadBytes != 0 {
		t.Errorf("MaxReadBytes: got %d, want 0 (uncapped)", tc.MaxReadBytes)
	}
	if tc.MaxWriteKB != 0 {
		t.Errorf("MaxWriteKB: got %d, want 0 (uncapped)", tc.MaxWriteKB)
	}
	if tc.MaxOutputBytes != 0 {
		t.Errorf("MaxOutputBytes: got %d, want 0 (uncapped)", tc.MaxOutputBytes)
	}
	if tc.MaxListDirEntries != 0 {
		t.Errorf("MaxListDirEntries: got %d, want 0 (uncapped)", tc.MaxListDirEntries)
	}
}

// TestDefaultMaxStepsIsZero verifies that when no max_steps is configured, the
// resolved MaxSteps is nil (unset), which the agent loop treats as 0 (unlimited).
func TestDefaultMaxStepsIsZero(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.MaxSteps != nil {
		t.Errorf("MaxSteps: got %d, want nil (unlimited)", *res.MaxSteps)
	}
}
