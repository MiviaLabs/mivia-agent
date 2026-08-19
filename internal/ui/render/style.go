// Package render turns theme roles into concrete lipgloss styles and
// renders structured event bodies (diffs, minimal markdown) into styled
// text. Every function here is pure: input in, string out, no I/O, no
// package state.
package render

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Lip converts a resolved theme.Style into a foreground lipgloss.Style.
// Structural emphasis (Bold/Dim) applies regardless of colour tier so it
// survives NO_COLOR; colour is applied only when the Style carries one.
func Lip(s theme.Style) lipgloss.Style {
	st := lipgloss.NewStyle().Bold(s.Bold).Faint(s.Dim)
	if s.Hex != "" {
		st = st.Foreground(lipgloss.Color(s.Hex))
	} else if s.ANSI16 >= 0 {
		st = st.Foreground(lipgloss.Color(strconv.Itoa(s.ANSI16)))
	}
	return st
}

// Role resolves a theme role at the given tier and returns its
// foreground lipgloss style, the common case for component call sites.
func Role(t theme.Theme, tier theme.Tier, r theme.Role) lipgloss.Style {
	return Lip(t.Resolve(r, tier))
}

// WithBg layers a background role onto an existing style. A tier with no
// colour for the role (e.g. the no-colour/ASCII tier) contributes no
// background, matching the degradation ladder.
func WithBg(st lipgloss.Style, t theme.Theme, tier theme.Tier, r theme.Role) lipgloss.Style {
	bg := t.Resolve(r, tier)
	if bg.Hex != "" {
		return st.Background(lipgloss.Color(bg.Hex))
	}
	if bg.ANSI16 >= 0 {
		return st.Background(lipgloss.Color(strconv.Itoa(bg.ANSI16)))
	}
	return st
}

// FormatArgs renders a tool-call argument map as a stable, sorted
// "k=v k2=v2" string. Shared by every component that displays a tool
// call (transcript, approval).
func FormatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, args[k]))
	}
	return strings.Join(parts, " ")
}
