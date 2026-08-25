package settings

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

const (
	// rowGap is the default column spacing across aligned settings list views.
	rowGap = 2
)

// section is one nav entry's detail pane: General, Models, MCP, Agents,
// or Automations. One small interface, one file per real
// implementation, so five sections do not each re-derive the
// list/detail/keys plumbing the Screen already owns (frame, nav,
// focus, notice, save state - see docs/design/settings-screen.md §8).
//
// Sections are held as pointers in Screen.sections: SetSize/SetTheme
// mutate in place rather than returning a copy, so Update's pointer
// receiver can return itself unchanged when a key does not concern it.
type section interface {
	Title() string
	SetSize(w, h int)
	SetTheme(t theme.Theme, tier theme.Tier)
	Update(msg tea.Msg) (section, tea.Cmd)
	View() string // detail body only, never the frame
	Hints() []keymap.ID
}

// inputCapturer is an optional interface a section can implement when it has
// an active text editor (such as MCP add/edit form) that needs raw keypresses.
type inputCapturer interface {
	CapturingInput() bool
}

// placeholderSection renders "unavailable" for a nav slot whose adapter
// is nil - the same nil-safe default the nil ports.CommandRunner uses
// (settings-screen.md §4), and what every slot is until its own slice
// (S4-S7) replaces it with a real section.
type placeholderSection struct {
	title         string
	theme         theme.Theme
	tier          theme.Tier
	width, height int
}

func newPlaceholderSection(title string) *placeholderSection {
	return &placeholderSection{title: title}
}

func (s *placeholderSection) Title() string { return s.title }

func (s *placeholderSection) SetSize(w, h int) { s.width, s.height = w, h }

func (s *placeholderSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
}

func (s *placeholderSection) Update(tea.Msg) (section, tea.Cmd) { return s, nil }

func (s *placeholderSection) View() string {
	return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(s.title + " is unavailable.")
}

func (s *placeholderSection) Hints() []keymap.ID { return nil }
