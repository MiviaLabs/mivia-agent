package cli

import "github.com/charmbracelet/lipgloss"

// ANSI SGR codes - one vocabulary for markdown + highlight rendering.
// Relocated from internal/legacytui/theme.go: internal/legacytui aliases
// these same values so both packages share one source of truth.
const (
	// AnsiBold starts bold text.
	AnsiBold = "\033[1m"
	// AnsiBoldEnd ends bold text.
	AnsiBoldEnd = "\033[22m"
	// AnsiItalic starts italic text.
	AnsiItalic = "\033[3m"
	// AnsiDim starts dim text.
	AnsiDim = "\033[2m"
	// AnsiDimEnd ends dim text.
	AnsiDimEnd = "\033[22m"
	// AnsiYellow sets yellow foreground.
	AnsiYellow = "\033[33m"
	// AnsiCyan sets cyan foreground.
	AnsiCyan = "\033[36m"
	// AnsiBlue sets blue foreground.
	AnsiBlue = "\033[34m"
	// AnsiGreen sets green foreground.
	AnsiGreen = "\033[32m"
	// AnsiRed sets red foreground.
	AnsiRed = "\033[31m"
	// AnsiMagenta sets magenta foreground.
	AnsiMagenta = "\033[35m"
	// AnsiBgDark sets a dark background (user-card / bar fill).
	AnsiBgDark = "\033[48;5;236m"
	// AnsiReset clears all SGR attributes.
	AnsiReset = "\033[0m"
)

// Theme color indices (256-color). Relocated from internal/legacytui/theme.go
// for the same reason as the ANSI codes above.
const (
	// ThemeColorDim is the dim/structural text color index.
	ThemeColorDim = "8"
	// ThemeColorDiffAdd is the added-line diff color index.
	ThemeColorDiffAdd = "10"
	// ThemeColorDiffDel is the removed-line diff color index.
	ThemeColorDiffDel = "9"
)

// themeColorError, themeColorUser, themeColorTime mirror the private indices
// in internal/legacytui/theme.go and internal/legacytui/toolui.go that back
// the styles below (kept private: only this file's own styles need them).
const (
	themeColorError  = "9"
	themeColorUser   = "12"
	themeColorTime   = "11"
	themeColorCardBg = "236"
)

// MinCardWidth is the floor width for a rendered chat card. Relocated from
// internal/legacytui/composer.go: internal/legacytui aliases this value so
// both packages share one source of truth.
const MinCardWidth = 20

// BrandColorThinking is the vivid cyan #00d7d7 thinking-ramp color.
// Relocated from internal/legacytui/brand.go: internal/legacytui aliases this
// value so both packages share one source of truth.
const BrandColorThinking = "44"

// brandColorMulti is the vivid magenta #d75fd7 multi-ramp color, mirroring
// internal/legacytui/brand.go's private constant of the same value (backs
// AgentBadgeStyle below).
const brandColorMulti = "170"

// BrandWorkFrames is an 8-frame single-rune braille diamond pulse. Relocated
// from internal/legacytui/brand.go: internal/legacytui aliases this slice so
// both packages share one source of truth.
var BrandWorkFrames = []string{
	"⠶", // U+2836 dots 2,3,5,6     - inner diamond
	"⠛", // U+281B dots 1,2,4,5     - upper weight
	"⠿", // U+283F dots 1–6         - mid expand
	"⣿", // U+28FF all 8            - full pulse
	"⣶", // U+28F6 dots 2,3,5,6,7,8 - lower weight
	"⠿", // mid
	"⠛", // upper
	"⠶", // inner
}

// Semantic styles. Relocated from internal/legacytui/theme.go and
// internal/legacytui/toolui.go: both are reconstructed here from the raw
// color indices above (rather than aliasing an unexported legacytui var,
// which cli cannot reach), and internal/legacytui aliases these vars back so
// its own call sites are unchanged.
var (
	// TUIDimStyle is the dim/structural text style.
	TUIDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDim))
	// ToolDimStyle is the dim/structural text style for tool rows.
	ToolDimStyle = TUIDimStyle
	// TUIErrorStyle is the error text style.
	TUIErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorError))
	// ToolErrStyle is the inline error style for tool status icons.
	ToolErrStyle = TUIErrorStyle
	// UserLabelStyle renders the "you" user-turn label.
	UserLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser)).Bold(true)
	// UserRailStyle renders the user-turn left rail glyph.
	UserRailStyle = UserLabelStyle
	// TUIThinkingStyle renders live thinking-phase text.
	TUIThinkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(BrandColorThinking)).Italic(true)
	// ToolOkStyle renders a completed, non-failed tool status icon.
	ToolOkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDiffAdd))
	// ToolNameStyle renders a tool's name.
	ToolNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser)).Bold(true)
	// ToolTimeStyle renders a tool's elapsed-time text.
	ToolTimeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorTime))
	// ToolPathStyle renders the workspace-path chip on a tool row.
	ToolPathStyle = lipgloss.NewStyle().Reverse(true).Faint(true)
	// AgentBadgeStyle marks nested tool rows with their producing subagent.
	AgentBadgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorMulti))
)
