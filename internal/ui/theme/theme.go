package theme

// Theme is data, not code: a Role -> hex colour map, plus an explicit
// 16-colour tier map and the CVD budget this theme trades against. A
// view layer never holds a literal colour, only a Role, and looks it up
// through a Theme via Resolve.
type Theme struct {
	Name       string          `json:"name"`
	Label      string          `json:"label"`
	Dark       bool            `json:"dark"`
	FirstParty bool            `json:"first_party"`
	CVDBudget  float64         `json:"cvd_budget"`
	Credits    string          `json:"credits,omitempty"`
	Colors     map[Role]string `json:"colors"`
	// ANSI16 gives each role an explicit ANSI SGR colour index (0-15) for
	// the 16-colour degradation tier. A generic nearest-RGB downsample
	// turns an achromatic accent to silver (research.md finding 8); this
	// map is the fix, authored per theme rather than computed.
	ANSI16 map[Role]int `json:"ansi16"`
}

// Color returns the truecolor hex value for a role, and whether it is
// defined.
func (t Theme) Color(r Role) (string, bool) {
	v, ok := t.Colors[r]
	return v, ok
}

// Ansi16 returns the ANSI SGR colour index (0-15) for a role, and whether
// it is defined.
func (t Theme) Ansi16(r Role) (int, bool) {
	v, ok := t.ANSI16[r]
	return v, ok
}

// emphasisRoles is structural typographic hierarchy, not per-theme data:
// NO_COLOR preserves bold/dim/underline (research-panes.md section 2.1),
// so these are always applied regardless of colour tier.
var (
	boldRoles = map[Role]bool{
		RoleAccent:      true,
		RoleBorderFocus: true,
	}
	dimRoles = map[Role]bool{
		RoleFGMuted:  true,
		RoleFGSubtle: true,
		RoleComment:  true,
		RoleGutter:   true,
	}
)

// Emphasis reports the typographic weight a role carries independent of
// colour, so it survives NO_COLOR and the no-colour/ASCII tier.
func Emphasis(r Role) (bold, dim bool) {
	return boldRoles[r], dimRoles[r]
}
