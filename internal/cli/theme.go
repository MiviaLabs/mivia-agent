// Package cli - theme is the single semantic color/style source for terminal UI.
//
// Render files compose on these tokens; brand.go still owns the phase-color ramp
// (brandColorThinking, brandColorError, …). Inline content error (themeColorError
// "9") is deliberately distinct from brand/status error (brandColorError "160").
package cli

import "github.com/charmbracelet/lipgloss"

// Theme - the single semantic color/style source.
//
// 256-color indices (raw-string source of truth for P2.2 / P3 consumers):
const (
	themeColorDim          = "8"   // dim/structural text
	themeColorError        = "9"   // inline error red (tool ✗, slash errors) - not brandColorError
	themeColorInfo         = "14"  // cyan accent / info
	themeColorUser         = "12"  // user label blue
	themeColorWaitGray     = "243" // waiting-state mid-gray
	themeColorCardBg       = "236" // user-card / bar dark bg
	themeColorSelBg        = "237" // tool selection bg
	themeColorDiffAdd      = "10"
	themeColorDiffDel      = "9" // same index as themeColorError; distinct role name
	themeColorDiffAddBg    = "22"
	themeColorDiffDelBg    = "88"
	themeColorDiffHunk     = "5"  // magenta @@ hunk headers (SGR 35 / AnsiMagenta)
	themeColorTime         = "11" // tool-time yellow
	themeColorOk           = "2"  // run-dashboard "running" / tool ok green
	themeColorStatusFailed = "9"
	themeColorStatusDone   = "8"
	themeThinkingDim       = "6"  // thinkingDimStyle
	themeColorBright       = "15" // bright white (name, selection fg)
)

// ANSI SGR codes - one vocabulary for markdown + highlight (hl* block
// deleted). The exported codes below (AnsiBold .. AnsiReset) are shared with
// the classic-mode markdown/highlight renderers in internal/clichat.
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
	AnsiBgDark  = "\033[48;5;236m"
	ansiBgReset = "\033[49m"
	// AnsiReset clears all SGR attributes.
	AnsiReset = "\033[0m"
)

// ansiBgDiffDel / ansiBgDiffAdd (diff-highlight SGR) relocated to
// diff_style.go: that file was their sole caller. ansiUnderline relocated
// to markdown.go for the same reason.

// Consolidated semantic styles (duplicates collapse onto these).
var (
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorDim))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorError))
	infoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorInfo)).Bold(true)
	waitStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorWaitGray))
	userLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser)).Bold(true)
	userStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(themeColorUser))
)

// Backward-compat aliases - same value as consolidated styles.
// Collapse when call sites migrate off the legacy names (P1.1 follow-up).
var (
	// TUIDimStyle is the dim/structural text style. Shared with the
	// classic-mode block renderer in internal/clichat.
	TUIDimStyle = dimStyle // alias of dimStyle
	// ToolDimStyle is the dim/structural text style for tool rows. Shared
	// with the classic-mode renderer in internal/clichat.
	ToolDimStyle  = dimStyle // alias of dimStyle
	tuiErrorStyle = errStyle // alias of errStyle
	// ToolErrStyle is the inline error style for tool status icons. Shared
	// with the classic-mode renderer in internal/clichat.
	ToolErrStyle    = errStyle    // alias of errStyle
	tuiInfoStyle    = infoStyle   // alias of infoStyle
	tuiAccentStyle  = accentStyle // alias of accentStyle
	tuiWaitingStyle = waitStyle   // alias of waitStyle
	tuiUserLabel    = userLabel   // alias of userLabel
	tuiUserStyle    = userStyle   // alias of userStyle
	// UserLabelStyle renders the "you" user-turn label. Shared with the
	// classic-mode message-card renderer in internal/clichat.
	UserLabelStyle = userLabel // alias of userLabel
	// UserRailStyle renders the user-turn left rail glyph. Shared with the
	// classic-mode message-card renderer in internal/clichat.
	UserRailStyle = userLabel // alias of userLabel
)
