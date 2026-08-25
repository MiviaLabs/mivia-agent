package render

import "github.com/MiviaLabs/mivia-agent/internal/ui/theme"

// ContextRole returns the semantic theme role for a context-fill percentage.
// The thresholds match the topbar's existing coloring contract (70/90) with an
// additional RoleInfo step below 70% to surface early signal in the status line,
// where the ctx pill currently renders with no color at any fill level.
//
// pct >= 90 → RoleDanger (critical, matches existing topbar danger boundary)
// pct >= 70 → RoleWarning (caution, matches existing topbar warning boundary)
// pct >= 50 → RoleInfo (informational notice: context is in active use)
// pct < 50 → RoleFGSubtle (neutral; comparison-based so pct > 100 is still danger)
func ContextRole(pct int) theme.Role {
	switch {
	case pct >= 90:
		return theme.RoleDanger
	case pct >= 70:
		return theme.RoleWarning
	case pct >= 50:
		return theme.RoleInfo
	default:
		return theme.RoleFGSubtle
	}
}
