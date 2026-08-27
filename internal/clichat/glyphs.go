package clichat

// Glyphs centralize the single-character status markers used across the TUI
// render surface (toolui, toolpanel, chatblock_render, brand).
const (
	// GlyphCheck marks a succeeded state.
	GlyphCheck = "✓"
	// GlyphCross marks a failed state.
	GlyphCross = "✗"
	// GlyphDiamond marks an agent-kind action.
	GlyphDiamond = "◆"
	// GlyphLozenge marks a skill-kind action.
	GlyphLozenge = "◇"
	// GlyphTriR is the right-pointing triangle for a collapsed section.
	GlyphTriR = "▸" // right-pointing triangle (collapsed)
	glyphTriD = "▾" // down-pointing triangle (expanded)
)
