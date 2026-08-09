package workspace

import (
	"testing"
)

// TestGapOrgMemoryDBPathEmptyHome pins that OrgMemoryDBPath yields "" when no
// home directory is available, so callers can disable the org store instead
// of failing startup (the same contract UserSkillsDir documents).
func TestGapOrgMemoryDBPathEmptyHome(t *testing.T) {
	t.Setenv("HOME", "")
	if got := OrgMemoryDBPath(); got != "" {
		t.Errorf("OrgMemoryDBPath = %q, want empty when HOME is unavailable", got)
	}
}
