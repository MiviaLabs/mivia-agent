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

// ContextCells is how many of `blocks` cells a fill percentage claims,
// rounded to the nearest cell and clamped to the bar. It is the single
// place a share becomes a cell count, so a bar drawn in one run and a
// bar drawn in two (see the sidebar's floor/conversation split) cannot
// disagree about where the fill ends.
func ContextCells(pct, blocks int) int {
	if blocks <= 0 {
		return 0
	}
	return min(blocks, max(0, (pct*blocks+50)/100))
}

// ContextGlyphs returns the filled and empty bar cell for a tier.
// Unicode tiers use ▰/▱; ASCII and no-TTY tiers use =/- so the same
// share still reads on a plain terminal.
func ContextGlyphs(tier theme.Tier) (full, empty string) {
	if tier == theme.TierASCII || tier == theme.TierNoTTY {
		return "=", "-"
	}
	return "▰", "▱"
}

// ContextBar draws a context-fill bar of `blocks` cells: filled cells
// on the left, empty on the right. Zero or negative widths draw
// nothing. Callers that need the fill split into differently styled
// runs compose it from ContextCells and ContextGlyphs instead.
func ContextBar(pct, blocks int, tier theme.Tier) string {
	if blocks <= 0 {
		return ""
	}
	filled := ContextCells(pct, blocks)
	full, empty := ContextGlyphs(tier)
	return strings.Repeat(full, filled) + strings.Repeat(empty, blocks-filled)
}
