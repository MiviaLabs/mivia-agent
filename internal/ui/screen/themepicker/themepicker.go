// Package themepicker is the alt-screen modal (build spec section 3.4)
// that live-previews and selects an app-wide theme.
package themepicker

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

var _ app.Screen = Screen{}

// previewAccentGlyph is the preview's prompt marker, styled with
// RoleAccent - the same convention the composer's own "> " prompt uses,
// so the preview reads as a small sample of real chrome rather than an
// arbitrary swatch.
const previewAccentGlyph = "> "

// previewSample is the fixed sentence the preview's prompt line
// renders.
const previewSample = "Add retry with backoff to the uploader."

// The code-read and diff samples below exist because a prompt line
// alone never exercises RoleKeyword/RoleFunction/RoleType/RoleVariable
// or the diff add/del roles - exactly the roles most likely to fail a
// contrast or CVD check (theme/contrast.go checks them, but a human
// browsing themes could not see them before this). docs/design/
// mivia-ui-mock.html section 7 shows the same two additions.
//
// previewFuncName and previewParamType are pulled out because the
// tests reference them directly, so the sample and its assertions
// cannot drift apart silently.
const (
	previewFuncName  = "put"
	previewParamType = "context.Context"

	previewDiffDelLine = "return u.raw.Put(ctx, k, b)"
	previewDiffAddLine = "return retry.Do(ctx, u.policy, put)"
)

// Screen wraps picker.Model over a theme name list. Selecting an item
// emits app.ThemeSelectedMsg; cancelling emits app.PopScreenMsg. Neither
// mutates app-level state directly - only the router does that.
type Screen struct {
	Theme  theme.Theme
	Tier   theme.Tier
	picker picker.Model

	// themes is the full list New() was given, kept so the preview can
	// look up the highlighted row's actual theme.Theme (colours, not
	// just its name) as the cursor moves - before Enter ever applies it.
	themes []theme.Theme

	// width and height are the live terminal size, for centering the
	// dialog frame. 0 renders uncentered (tests pin exact strings).
	width  int
	height int
}

// New builds a picker over the given themes, rendered with the current
// (pre-selection) theme so the modal itself stays legible mid-pick.
func New(th theme.Theme, tier theme.Tier, themes []theme.Theme) Screen {
	names := make([]string, len(themes))
	for i, t := range themes {
		names[i] = t.Name
	}
	return Screen{Theme: th, Tier: tier, picker: picker.New(th, tier, names), themes: themes}
}

// previewTheme resolves the picker's currently highlighted row to its
// theme.Theme. It falls back to the screen's own (still-active) theme
// when nothing is highlighted, which only happens with an empty list.
func (s Screen) previewTheme() theme.Theme {
	name, ok := s.picker.Selected()
	if !ok {
		return s.Theme
	}
	for _, t := range s.themes {
		if t.Name == name {
			return t
		}
	}
	return s.Theme
}

func (s Screen) Init() tea.Cmd { return nil }

// ViewFlags holds the alternate screen: a theme preview is a cockpit
// modal, and the router re-enters the cockpit when it pops.
func (s Screen) ViewFlags() app.ViewFlags { return app.ViewFlags{AltScreen: true} }

func (s Screen) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		s.width, s.height = size.Width, size.Height
		return s, nil
	}
	if changed, ok := msg.(app.ThemeChangedMsg); ok {
		s.Theme, s.Tier = changed.Theme, changed.Tier
		s.picker.Theme, s.picker.Tier = changed.Theme, changed.Tier
		return s, nil
	}
	next, cmd := s.picker.Update(msg)
	s.picker = next
	if cmd == nil {
		return s, nil
	}
	// picker.Model's Update only ever produces a non-nil Cmd for "enter"
	// (SelectMsg) or "esc" (CancelMsg) - see internal/ui/component/picker.
	// The fallthrough below is unreachable through picker's real
	// behavior today; it exists so this stays correct (drop the Msg,
	// not panic) if that vocabulary ever grows without a matching case
	// here landing first.
	switch m := cmd().(type) {
	case picker.SelectMsg:
		return s, func() tea.Msg { return app.ThemeSelectedMsg{Name: m.Item} }
	case picker.CancelMsg:
		return s, func() tea.Msg { return app.PopScreenMsg{} }
	}
	return s, nil
}

func (s Screen) View() string {
	hint := "[enter] select  [esc] cancel  type to filter"
	body := s.picker.View() + "\n\n" + s.previewView()
	return render.Dialog(s.Theme, s.Tier, s.width, s.height, "select a theme", body, hint)
}

// previewView renders a prompt line, a syntax-highlighted code read,
// and a diff hunk, all styled with the highlighted row's theme - live,
// as the cursor moves, not only once Enter applies it.
func (s Screen) previewView() string {
	pt := s.previewTheme()
	prompt := render.Role(pt, s.Tier, theme.RoleAccent).Render(previewAccentGlyph)
	body := render.Role(pt, s.Tier, theme.RoleFG).Render(previewSample)
	return prompt + body + "\n\n" + s.previewCode(pt) + "\n\n" + render.Diff(pt, s.Tier, previewDiff())
}

// previewCode is a small, real-shaped Go function read, hand-tokenized
// rather than run through a general syntax highlighter: none exists
// elsewhere in this codebase (theme/role.go's syntax roles had no
// renderer at all before this), and building one is out of scope for a
// picker preview.
func (s Screen) previewCode(pt theme.Theme) string {
	role := func(r theme.Role, text string) string { return render.Role(pt, s.Tier, r).Render(text) }
	plain := func(text string) string { return role(theme.RoleFG, text) }

	sig := role(theme.RoleKeyword, "func") + plain(" (u *") + role(theme.RoleType, "Uploader") + plain(") ") +
		role(theme.RoleFunction, previewFuncName) + plain("(") +
		role(theme.RoleVariable, "ctx") + plain(" ") + role(theme.RoleType, previewParamType) + plain(", ") +
		role(theme.RoleVariable, "k") + plain(" ") + role(theme.RoleType, "string") + plain(") ") +
		role(theme.RoleType, "error") + plain(" {")
	body := plain("    ") + role(theme.RoleKeyword, "return") + plain(" ") +
		role(theme.RoleVariable, "u") + plain(".raw.") + role(theme.RoleFunction, "Put") +
		plain("(") + role(theme.RoleVariable, "ctx") + plain(", ") +
		role(theme.RoleVariable, "k") + plain(", ") + role(theme.RoleVariable, "b") + plain(")")
	return sig + "\n" + body + "\n" + plain("}")
}

// previewDiff is a fixed one-hunk diff: the same shape a real edit_file
// tool call renders, so the picker's diff roles are judged on realistic
// content, not a swatch invented for this modal alone.
func previewDiff() uievent.Diff {
	return uievent.Diff{
		Path: "internal/storage/s3_uploader.go",
		Hunks: []uievent.DiffHunk{{
			Header: "@@ -14,3 +14,4 @@ func (u *Uploader) put(",
			Lines: []uievent.DiffLine{
				{Kind: uievent.DiffLineDel, Text: previewDiffDelLine},
				{Kind: uievent.DiffLineAdd, Text: previewDiffAddLine},
			},
		}},
	}
}
