package theme

import "fmt"

// ContrastCheck is one required foreground/background pair and the WCAG
// 2.1 ratio it must meet. Body text needs 4.5:1 (AA); large text and UI
// components need 3:1. AllContrastChecks is the single source of truth
// for which pairs the UI actually renders — role.RoleBorder is
// deliberately absent: no state is carried by border alone, so it is
// decorative and exempt from WCAG 1.4.11 (wireframes.md section 7).
type ContrastCheck struct {
	FG, BG Role
	Min    float64
	Label  string
}

func AllContrastChecks() []ContrastCheck {
	const (
		body  = 4.5
		large = 3.0
	)
	return []ContrastCheck{
		{RoleFG, RoleBG, body, "body text"},
		{RoleFGMuted, RoleBG, body, "muted text"},
		{RoleFGSubtle, RoleBG, large, "subtle text (large/UI use only)"},
		{RoleBorderFocus, RoleBG, large, "focus ring"},
		{RoleAccent, RoleBG, large, "accent chrome"},
		{RoleAccentFG, RoleAccent, body, "text on accent fill"},
		{RoleSuccess, RoleBG, large, "status word"},
		{RoleWarning, RoleBG, large, "status word"},
		{RoleDanger, RoleBG, large, "status word"},
		{RoleInfo, RoleBG, large, "status word"},
		{RoleKeyword, RoleBG, body, "syntax"},
		{RoleString, RoleBG, body, "syntax"},
		{RoleNumber, RoleBG, body, "syntax"},
		{RoleComment, RoleBG, large, "syntax, muted/secondary by convention"},
		{RoleFunction, RoleBG, body, "syntax"},
		{RoleType, RoleBG, body, "syntax"},
		{RoleVariable, RoleBG, body, "syntax"},
		{RoleDiffAddFG, RoleDiffAddBG, body, "diff add line"},
		{RoleDiffDelFG, RoleDiffDelBG, body, "diff del line"},
		{RoleDiffHunk, RoleBG, large, "diff hunk header"},
		{RoleLink, RoleBG, body, "link"},
		{RoleGutter, RoleBG, large, "gutter (functional, not decorative)"},
		{RoleFGInverse, RoleSuccess, body, "text on success fill"},
		{RoleFGInverse, RoleWarning, body, "text on warning fill"},
		{RoleFGInverse, RoleDanger, body, "text on danger fill"},
	}
}

// ContrastFailure is one gate-table check a theme did not meet.
type ContrastFailure struct {
	Check ContrastCheck
	Ratio float64
}

func (f ContrastFailure) String() string {
	return fmt.Sprintf("%s: %s/%s = %.2f:1, need %.1f:1 (%s)",
		f.Check.Label, f.Check.FG, f.Check.BG, f.Ratio, f.Check.Min, f.Check.Label)
}

// ValidateContrast checks every gate-table pair against a theme's
// colours and returns every failure found (nil if the theme passes
// clean).
func ValidateContrast(t Theme) ([]ContrastFailure, error) {
	var failures []ContrastFailure
	for _, chk := range AllContrastChecks() {
		fgHex, ok := t.Color(chk.FG)
		if !ok {
			return nil, fmt.Errorf("theme %s: missing colour for role %s", t.Name, chk.FG)
		}
		bgHex, ok := t.Color(chk.BG)
		if !ok {
			return nil, fmt.Errorf("theme %s: missing colour for role %s", t.Name, chk.BG)
		}
		ratio, err := contrastRatio(fgHex, bgHex)
		if err != nil {
			return nil, fmt.Errorf("theme %s: %w", t.Name, err)
		}
		if ratio < chk.Min {
			failures = append(failures, ContrastFailure{Check: chk, Ratio: ratio})
		}
	}
	return failures, nil
}
