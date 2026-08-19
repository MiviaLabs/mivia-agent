// Package theme is the single source of style for internal/ui: semantic
// roles, never raw colours at call sites. This file defines the role
// enum only; embedded theme data, the contrast/CVD validators, the
// palette search, and the degradation ladder land after user review of
// this list.
package theme

// Role names one semantic colour slot. A Theme maps every Role to a
// colour; a view layer never holds a literal colour, only a Role.
type Role string

// Base roles. wireframes.md section 7.
const (
	RoleBG          Role = "bg"
	RoleBGSubtle    Role = "bg-subtle"
	RoleBGInset     Role = "bg-inset"
	RoleFG          Role = "fg"
	RoleFGMuted     Role = "fg-muted"
	RoleFGSubtle    Role = "fg-subtle"
	RoleBorder      Role = "border"       // decorative; no state, exempt from WCAG 1.4.11
	RoleBorderFocus Role = "border-focus" // carries state; must meet 3:1
	RoleAccent      Role = "accent"       // chrome only: prompt marker, focus ring, selection. Never a status.
	RoleAccentFG    Role = "accent-fg"
	RoleSuccess     Role = "success"
	RoleWarning     Role = "warning"
	RoleDanger      Role = "danger"
	RoleInfo        Role = "info"
)

// Syntax roles. wireframes.md section 7.
const (
	RoleKeyword  Role = "keyword"
	RoleString   Role = "string"
	RoleNumber   Role = "number"
	RoleComment  Role = "comment"
	RoleFunction Role = "function"
	RoleType     Role = "type"
	RoleVariable Role = "variable"
)

// Diff roles. wireframes.md section 7.
const (
	RoleDiffAddFG Role = "diff-add-fg"
	RoleDiffAddBG Role = "diff-add-bg"
	RoleDiffDelFG Role = "diff-del-fg"
	RoleDiffDelBG Role = "diff-del-bg"
	RoleDiffHunk  Role = "diff-hunk"
)

// Roles the Phase 0 mock needed that the original supplied list did not
// contain. wireframes.md section 7 / research.md finding 3.
const (
	RoleBGSelection   Role = "bg-selection"     // picker/completion selected row; not accent
	RoleDiffAddEmphBG Role = "diff-add-emph-bg" // word-level diff emphasis
	RoleDiffDelEmphBG Role = "diff-del-emph-bg" // word-level diff emphasis
	RoleGutter        Role = "gutter"           // dimmer than border, not decorative
	RoleLink          Role = "link"             // actionable file paths/URLs; distinct from info
	RoleFGInverse     Role = "fg-inverse"       // text on success/warning/danger fills
)

// AllRoles lists every role a Theme must define, in the order shown in
// wireframes-panes.md section 18.
func AllRoles() []Role {
	return []Role{
		RoleBG, RoleBGSubtle, RoleBGInset,
		RoleFG, RoleFGMuted, RoleFGSubtle,
		RoleBorder, RoleBorderFocus,
		RoleAccent, RoleAccentFG,
		RoleSuccess, RoleWarning, RoleDanger, RoleInfo,
		RoleKeyword, RoleString, RoleNumber, RoleComment, RoleFunction, RoleType, RoleVariable,
		RoleDiffAddFG, RoleDiffAddBG, RoleDiffDelFG, RoleDiffDelBG, RoleDiffHunk,
		RoleBGSelection, RoleDiffAddEmphBG, RoleDiffDelEmphBG, RoleGutter, RoleLink, RoleFGInverse,
	}
}

// StatusRoles is the set that must stay mutually separable under
// dichromat simulation. research-panes.md section 3: accent is chrome
// and is exempt from this check.
func StatusRoles() []Role {
	return []Role{RoleSuccess, RoleWarning, RoleDanger, RoleInfo}
}
