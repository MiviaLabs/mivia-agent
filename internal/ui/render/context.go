package render

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

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

// ContextBar draws a context-fill bar of `blocks` cells: filled cells
// on the left, empty on the right, the fill rounded to the nearest
// cell so 4 blocks at 60% show 2 and a full sidebar width at 60% shows
// three fifths. Unicode tiers use ▰/▱; ASCII and no-TTY tiers use =/-
// so the same share reads on a plain terminal. Zero or negative widths
// draw nothing.
func ContextBar(pct, blocks int, tier theme.Tier) string {
	if blocks <= 0 {
		return ""
	}
	filled := min(blocks, max(0, (pct*blocks+50)/100))
	full, empty := "▰", "▱"
	if tier == theme.TierASCII || tier == theme.TierNoTTY {
		full, empty = "=", "-"
	}
	return strings.Repeat(full, filled) + strings.Repeat(empty, blocks-filled)
}
