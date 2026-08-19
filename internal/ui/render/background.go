package render

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// FillBG paints text onto role r's background.
//
// A lipgloss Background() on a block colours only up to the first SGR
// reset inside it, and styled text is a chain of runs that each end in
// one. Every run after the first therefore drew on the terminal's own
// background: dark rectangles behind a light dialog's code sample, and a
// screen that kept the terminal's colour after the theme changed. FillBG
// re-establishes the background after every reset, so a fill is whole
// rather than patchy.
//
// A run that carries its OWN background (a diff line, a selected picker
// row) still wins: this only paints what is otherwise unpainted, and
// picks up again where that run resets.
//
// Callers pad rows to the width they want covered first - FillBG colours
// the cells it is given and adds none.
//
// It returns text unchanged at a tier with no colour for r, matching the
// degradation ladder WithBg and Bordered follow: a colour fill without
// colour is nothing, and NO_COLOR output must stay byte-identical.
func FillBG(t theme.Theme, tier theme.Tier, r theme.Role, text string) string {
	seq := bgSeq(t.Resolve(r, tier))
	if seq == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = seq + reopenBG(line, seq) + ansi.ResetStyle
	}
	return strings.Join(lines, "\n")
}

// bgSeq is the SGR sequence that sets a resolved style's colour as the
// background, or "" when the style carries no colour. It is built with
// the same encoder lipgloss uses, so the bytes match what Background()
// emits for the identical colour.
func bgSeq(s theme.Style) string {
	switch {
	case s.Hex != "":
		return ansi.NewStyle().BackgroundColor(lipgloss.Color(s.Hex)).String()
	case s.ANSI16 >= 0:
		return ansi.NewStyle().BackgroundColor(lipgloss.Color(strconv.Itoa(s.ANSI16))).String()
	}
	return ""
}

// reopenBG re-emits seq after every SGR reset in line. Only the two
// reset forms lipgloss writes are matched; no other sequence is parsed,
// let alone rewritten, so nothing else in the line can be corrupted.
func reopenBG(line, seq string) string {
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+seq)
	return strings.ReplaceAll(line, ansi.ResetStyle, ansi.ResetStyle+seq)
}
