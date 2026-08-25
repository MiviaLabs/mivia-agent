package render

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestContextRole verifies ContextRole's context-fill percentage to
// semantic-role mapping, including boundary values and out-of-range
// input above 100.
func TestContextRole(t *testing.T) {
	tests := []struct {
		name string
		pct  int
		want theme.Role
	}{
		{"zero", 0, theme.RoleFGSubtle},
		{"below-subtle-upper-bound", 49, theme.RoleFGSubtle},
		{"info-lower-bound", 50, theme.RoleInfo},
		{"below-info-upper-bound", 69, theme.RoleInfo},
		{"warning-lower-bound", 70, theme.RoleWarning},
		{"below-warning-upper-bound", 89, theme.RoleWarning},
		{"danger-lower-bound", 90, theme.RoleDanger},
		{"full", 100, theme.RoleDanger},
		{"overfull", 999, theme.RoleDanger},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContextRole(tc.pct); got != tc.want {
				t.Errorf("ContextRole(%d) = %q, want %q", tc.pct, got, tc.want)
			}
		})
	}
}
