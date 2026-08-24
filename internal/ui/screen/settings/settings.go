// Package settings is the full-screen /settings modal: a left nav
// sidebar (General, Models, MCP, Agents, Automations) beside a detail
// pane, keeping the top bar and a status row. See
// docs/design/settings-screen.md for the design this package
// implements.
package settings

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

var _ app.Screen = Screen{}

// sectionCount is the fixed number of nav entries: General, Projects,
// Automations, Agents, Skills, Models, MCP, in that order everywhere
// (nav, breadcrumb, sectionNames, the theme-walk fixture). A slice
// length rather than a named const per section would let the two
// drift.
const sectionCount = 7

// Screen is the settings modal. It owns the frame (top bar, nav,
// status row); each section owns only its own detail body.
type Screen struct {
	Theme theme.Theme
	Tier  theme.Tier

	top  topbar.Model
	keys *keymap.Map

	sections []section // len == sectionCount, fixed order
	nav      int       // index of the highlighted/active section
	focus    render.Side

	overlay string // the generated help screen, drawn in place of the body
	notice  string

	// quitArmed is true between the first ctrl+c and the second, the
	// same double-press quit guard conversation.Screen's own quitArmed
	// implements (UX Rule 1.3). This screen has nothing analogous to an
	// in-flight turn to cancel on the first press, so the first press
	// only arms the guard and warns; the second press quits the app.
	quitArmed bool

	width, height int
}

// OwnsQuit reports true so the app router (app.OwnsQuit) forwards
// ctrl+c to this screen's own Update instead of quitting on the first
// press - the settings modal holds real in-flight state (cursor
// position, filters, a save in flight), unlike a quick pick-one-and-go
// dialog, so a stray ctrl+c must not discard it with no warning.
func (s Screen) OwnsQuit() bool { return true }

var _ app.OwnsQuit = Screen{}

// sectionNames is the deep-link vocabulary "/settings <name>" resolves
// against, in nav order. Lowercase, matching SectionIndex's own
// case-folding. Projects and Skills have no ports-backed section yet
// (no domain model has been scoped for either, the same state Automations
// was in before settings-screen.md §12 defined one) - they are
// placeholders like every other nil-backed section until one lands.
var sectionNames = []string{"general", "projects", "automations", "agents", "skills", "models", "mcp"}

// SectionIndex resolves a deep-link section name (as "/settings models"
// or CommandOutcome.SettingsSection would carry) to its nav index,
// case-insensitively. An empty name resolves to 0 (General): opening
// settings with no argument is not a deep link, it is the default.
// The caller - not this package - decides what an unresolved name
// means; commands.go's openSettingsScreen turns a false ok into a
// Notice rather than silently opening on General, so a typo'd deep
// link is never mistaken for "no argument was given" (settings-screen.md §6).
func SectionIndex(name string) (int, bool) {
	if name == "" {
		return 0, true
	}
	name = strings.ToLower(name)
	for i, n := range sectionNames {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

// New builds the settings screen over store, opened on the section at
// initialNav (clamped into range - callers resolve a name to an index
// with SectionIndex first). Every store field may be nil; a nil
// field's section renders "unavailable" rather than the screen failing
// to build (settings-screen.md §4).
//
// Sections are constructed EAGERLY, not lazily on first navigation: the
// app-wide theme walk (TestSelectingThemeLeavesNoValueOnTheOldTheme)
// only reaches values already live on the router, and a section built
// later would keep whatever theme it was born with.
func New(t theme.Theme, tier theme.Tier, top topbar.Model, store ports.Settings, initialNav int) Screen {
	s := Screen{
		Theme: t, Tier: tier, top: top,
		keys: keymap.New(keymap.Default()),
		sections: []section{
			newGeneralSection(store.General),
			newPlaceholderSection("Projects"),
			newAutomationsSection(store.Automations),
			newAgentsSection(store.Agents),
			newPlaceholderSection("Skills"),
			newModelsSection(store.Providers),
			newMCPSection(store.MCP),
		},
		focus: render.Left,
	}
	for _, sec := range s.sections {
		sec.SetTheme(t, tier)
	}
	if initialNav < 0 {
		initialNav = 0
	}
	if initialNav >= len(s.sections) {
		initialNav = len(s.sections) - 1
	}
	s.nav = initialNav
	s.top.SetBreadcrumb([]string{"settings", s.sections[s.nav].Title()})
	return s
}

func (s Screen) Init() tea.Cmd { return nil }

// ViewFlags holds the alternate screen, like every other pushed modal.
func (s Screen) ViewFlags() app.ViewFlags { return app.ViewFlags{AltScreen: true} }

// reservedRows is every row the frame claims for itself: the top bar
// plus its margin, and the status row. Whatever remains is the body's
// exact height (settings-screen.md §2).
func (s Screen) reservedRows() int {
	return s.top.Height() + 1 + 1
}

func (s Screen) bodyHeight() int {
	return render.DialogBodyRows(s.height)
}

// contentWidth is the inner width convention used for dialog calculations.
func contentWidth(width int) int {
	return render.DialogBodyWidth(width)
}

// navWidth is the left nav's column count: SplitNavShare of the body
// width, clamped to the settings-specific [Min,Max] band - narrower
// than render.SplitNavMax, which is sized for a file list, not five
// words. Below the band's own floor doubled, the two-pane layout cannot
// be framed at all and both panes fall back to half the width, the same
// degenerate guard SplitWidths uses.
func navWidth(bodyWidth int) int {
	if bodyWidth < uikitconfig.SettingsNavMin*2+1 {
		return bodyWidth / 2
	}
	w := bodyWidth * render.SplitNavShare / 100
	if w < uikitconfig.SettingsNavMin {
		return uikitconfig.SettingsNavMin
	}
	if w > uikitconfig.SettingsNavMax {
		return uikitconfig.SettingsNavMax
	}
	return w
}

func (s Screen) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		bw := render.DialogBodyWidth(msg.Width)
		bh := render.DialogBodyRows(msg.Height)
		nw := navWidth(bw)
		detailW := bw - nw - 1
		if detailW < 0 {
			detailW = 0
		}
		for _, sec := range s.sections {
			sec.SetSize(detailW, bh)
		}
		return s, nil
	case app.ThemeChangedMsg:
		s.Theme, s.Tier = msg.Theme, msg.Tier
		s.top.SetTheme(msg.Theme, msg.Tier)
		for _, sec := range s.sections {
			sec.SetTheme(msg.Theme, msg.Tier)
		}
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	case tea.MouseClickMsg:
		if s.overlay != "" {
			s.overlay = ""
			return s, tea.ClearScreen
		}
	}
	// Every other message (a section's own async save-result Msg -
	// generalSavedMsg, mcpFailedMsg, and so on) has nowhere else to go:
	// a section's Update is the only thing that knows its own message
	// types, so anything unrecognized here is routed to the nav-selected
	// section - NOT gated on s.focus == render.Right, because the user
	// may have already pressed esc back to the nav pane while a save
	// from that same section was still in flight, and a dropped Failed
	// result would silently hide a rejected write. Without this
	// forwarding at all, a section's awaitSave Cmd would fire its
	// result Msg into the void: the save itself would still land in the
	// store, but the section would never learn it finished.
	next, cmd := s.sections[s.nav].Update(msg)
	s.sections[s.nav] = next
	return s, cmd
}

// handleKey dispatches within ContextSettings only - this screen does
// not cascade to ContextGlobal, the same self-contained shape the pager
// uses (keymap.go's ContextSettings doc comment). ctrl+c is the one
// exception: it is a ContextGlobal binding everywhere else, and this
// screen only reaches it at all because app.OwnsQuit routes it here
// instead of the router quitting immediately (see OwnsQuit's doc
// comment), so it is matched explicitly rather than through
// ContextSettings.
func (s Screen) handleKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd) {
	// The overlay's "any key dismisses it" contract is absolute - it
	// must run before ctrl+c is special-cased below, or ctrl+c becomes
	// the one key that arms/triggers quit instead of closing the help
	// screen, contradicting the overlay's own promise and diverging from
	// conversation.Screen's handleKey, where the identical overlay check
	// also precedes any ContextGlobal key match including IDQuit.
	if s.overlay != "" {
		s.overlay = ""
		s.quitArmed = false
		return s, tea.ClearScreen
	}
	if msg.String() == "ctrl+c" {
		return s.quit()
	}
	// Any key other than ctrl+c clears the armed guard, so a stray
	// keystroke after the first press cannot leave the screen one press
	// from quitting - the same rule conversation.Screen's handleKey
	// applies to its own quitArmed.
	s.quitArmed = false

	id, ok := s.keys.Match(keymap.ContextSettings, msg.String())
	if !ok {
		return s, nil
	}
	s.notice = ""
	switch id {
	case keymap.IDSettingsBack:
		if s.focus == render.Right {
			s.focus = render.Left
			return s, nil
		}
		return s, func() tea.Msg { return app.PopScreenMsg{} }
	case keymap.IDSettingsPaneLeft:
		s.focus = render.Left
		return s, nil
	case keymap.IDSettingsPaneRight, keymap.IDSettingsSelect:
		if s.focus == render.Left {
			s.focus = render.Right
			return s, nil
		}
	case keymap.IDSettingsUp:
		if s.focus == render.Left {
			return s.moveNav(-1), nil
		}
	case keymap.IDSettingsDown:
		if s.focus == render.Left {
			return s.moveNav(1), nil
		}
	case keymap.IDSettingsHelp:
		s.overlay = render.Help(s.Theme, s.Tier, s.keys.Help())
		return s, tea.ClearScreen
	}
	if s.focus == render.Right {
		next, cmd := s.sections[s.nav].Update(msg)
		s.sections[s.nav] = next
		return s, cmd
	}
	return s, nil
}

// quit is the ctrl+c double-press guard (UX Rule 1.3): the first press
// arms it and warns in the status row, the second press inside the
// armed state quits the whole app. Unlike conversation.Screen.quit,
// there is no in-flight turn to cancel on the first press - the
// settings modal's async saves are section-local and complete on their
// own; there is nothing this screen owns that the first press should
// interrupt.
func (s Screen) quit() (app.Screen, tea.Cmd) {
	if !s.quitArmed {
		s.quitArmed = true
		return s, nil
	}
	return s, tea.Quit
}

// moveNav shifts the highlighted nav row by delta, clamped (not
// wrapped) to [0,sectionCount) - a wrap would let repeated presses
// silently land back where they started, which reads as the key doing
// nothing. It also updates the breadcrumb, so the top bar always names
// the section actually shown.
func (s Screen) moveNav(delta int) Screen {
	next := s.nav + delta
	if next < 0 {
		next = 0
	}
	if next >= len(s.sections) {
		next = len(s.sections) - 1
	}
	s.nav = next
	s.top.SetBreadcrumb([]string{"settings", s.sections[s.nav].Title()})
	return s
}

func (s Screen) title() string {
	base := "settings"
	if s.nav >= 0 && s.nav < len(s.sections) {
		return base + " > " + strings.ToLower(s.sections[s.nav].Title())
	}
	return base
}

// View draws the settings modal dialog: a centered bordered box with
// the tabs sidebar on the left and the active section's options on the right.
func (s Screen) View() string {
	bw := render.DialogBodyWidth(s.width)
	bh := render.DialogBodyRows(s.height)
	nw := navWidth(bw)
	if s.overlay != "" {
		return render.Dialog(s.Theme, s.Tier, s.width, s.height, s.title(), s.overlay, s.statusRow())
	}
	body := render.SplitAt(s.Theme, s.Tier, bw, bh, nw, s.focus,
		s.navView(), s.sections[s.nav].View())
	return render.Dialog(s.Theme, s.Tier, s.width, s.height, s.title(), body, s.statusRow())
}

// navView draws the tab sidebar list: active tab marked with '>' and
// highlighted background when the sidebar holds focus, or '•' when detail has focus.
func (s Screen) navView() string {
	var b strings.Builder
	for i, sec := range s.sections {
		style := render.Role(s.Theme, s.Tier, theme.RoleFG)
		prefix := "  "
		if i == s.nav {
			if s.focus == render.Left {
				style = render.WithBg(style, s.Theme, s.Tier, theme.RoleBGSelection)
				prefix = "> "
			} else {
				style = render.Role(s.Theme, s.Tier, theme.RoleAccent)
				prefix = "• "
			}
		}
		b.WriteString(style.Render(prefix + sec.Title()))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// statusRow is the hint line, or a transient notice in its place. The
// quit-armed warning takes priority over a section's own notice: it
// names an action one more keystroke away, which outranks a save
// result the user can still read after dismissing the warning.
func (s Screen) statusRow() string {
	if s.quitArmed {
		return render.Role(s.Theme, s.Tier, theme.RoleWarning).Render("ctrl+c:press again to quit")
	}
	if s.notice != "" {
		return render.Role(s.Theme, s.Tier, theme.RoleWarning).Render(s.notice)
	}
	hint := s.keys.Hint(
		keymap.IDSettingsSelect, keymap.IDSettingsNew, keymap.IDSettingsDelete,
		keymap.IDSettingsFilter, keymap.IDSettingsBack, keymap.IDSettingsHelp,
	)
	return render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render(hint)
}

// gutter is conversation.Screen's own helper, duplicated rather than
// exported across packages: both screens need "pad to width, frame
// with one blank column each side, paint the theme surface under
// every cell" and neither depends on the other.
func (s Screen) gutter(lines []string) string {
	if s.width < 3 {
		return strings.Join(lines, "\n")
	}
	inner := contentWidth(s.width)
	out := make([]string, len(lines))
	for i, ln := range lines {
		pad := inner - ansi.StringWidth(ln)
		if pad < 0 {
			ln = ansi.Truncate(ln, inner, uikitconfig.ClipMarker)
			pad = 0
		}
		out[i] = " " + ln + strings.Repeat(" ", pad) + " "
	}
	return render.FillBG(s.Theme, s.Tier, theme.RoleBG, strings.Join(out, "\n"))
}
