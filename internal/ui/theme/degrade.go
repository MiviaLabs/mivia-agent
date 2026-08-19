package theme

import (
	"io"

	"github.com/charmbracelet/colorprofile"
)

// Tier is the colour-profile degradation tier. It re-exports
// colorprofile.Profile directly rather than wrapping it, so Detect below
// stays a thin pass-through to the library the design calls for
// (research-panes.md section 2.1) instead of a hand-rolled duplicate.
type Tier = colorprofile.Profile

const (
	TierTrueColor Tier = colorprofile.TrueColor
	Tier256       Tier = colorprofile.ANSI256
	Tier16        Tier = colorprofile.ANSI
	TierASCII     Tier = colorprofile.Ascii
	TierNoTTY     Tier = colorprofile.NoTTY
)

// Detect resolves the colour tier from the output stream and environment,
// honouring NO_COLOR, CLICOLOR, CLICOLOR_FORCE, TERM and COLORTERM.
func Detect(w io.Writer, env []string) Tier {
	return colorprofile.Detect(w, env)
}

// Style is the fully-resolved, tier-appropriate representation of one
// role: a hex colour where the tier supports it, an explicit ANSI16
// index at the 16-colour tier, and structural emphasis that survives
// every tier including no-colour (research-panes.md section 2.1: NO_COLOR
// "disables colors but preserves text decoration").
type Style struct {
	Hex     string // set for TrueColor and Tier256; empty otherwise
	ANSI16  int    // set for Tier16; -1 otherwise
	NoColor bool   // true for TierASCII and TierNoTTY
	Bold    bool
	Dim     bool
}

// Resolve returns the Style a role should render as at the given tier.
// Tier256 uses the theme's truecolor hex and lets the terminal/library
// downsample it (research.md finding 8: safe at 256, only 16 needs an
// explicit map); Tier16 uses the theme's authored ANSI16 index directly,
// never a computed nearest match.
func (t Theme) Resolve(r Role, tier Tier) Style {
	bold, dim := Emphasis(r)
	style := Style{ANSI16: -1, Bold: bold, Dim: dim}

	switch tier {
	case colorprofile.TrueColor, colorprofile.ANSI256:
		if hex, ok := t.Color(r); ok {
			style.Hex = hex
		}
	case colorprofile.ANSI:
		if idx, ok := t.Ansi16(r); ok {
			style.ANSI16 = idx
		}
	default: // Ascii, NoTTY, Unknown
		style.NoColor = true
	}
	return style
}
