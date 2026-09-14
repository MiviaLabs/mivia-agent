package chat

import "charm.land/lipgloss/v2"

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
)

// themeColorError and themeColorUser back the styles below.
const (
	themeColorError = "9"
	themeColorUser  = "12"
)

// BrandColorThinking is the vivid cyan #00d7d7 thinking-ramp color.
const BrandColorThinking = "44"

// Semantic styles. Relocated from internal/legacytui/theme.go and
// internal/legacytui/toolui.go: both are reconstructed here from the raw
// color indices above (rather than aliasing an unexported legacytui var,
// which cli cannot reach), and internal/legacytui aliases these vars back so
// its own call sites are unchanged.
var (
	// TUIDimStyle is the dim/structural text style.
	TUIDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(ThemeColorDim))
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
)
