package cli

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// composer chrome: bordered card around textarea.View(), matching
// formatUserMessageCard (msgcard.go) so input feels like a chat bubble.
// Keyboard hints stay outside the card (View chrome line).

// Terminal and card floor dimensions shared across the TUI surface.
const (
	defaultTermWidth    = 80
	defaultTermHeight   = 24
	minCardWidth        = 20
	minPaneContentWidth = 8 // the return 8 pane-content floor
)

// composerOuterWidth floors the outer card width for layout.
func composerOuterWidth(width int) int {
	if width < minCardWidth {
		return minCardWidth
	}
	return width
}

func composerInnerWidth(width int) int {
	// "│ " + content + " │" - must match renderComposer inner.
	outer := composerOuterWidth(width)
	inner := outer - 4
	if inner < minPaneContentWidth {
		return minPaneContentWidth
	}
	return inner
}

// composerMaxHeight returns textarea line capacity for the terminal height.
// The composer starts at one line and grows with the draft, capped at 5.
func composerMaxHeight(termH int) int {
	return min(5, max(1, termH/6))
}

// renderComposer wraps textarea.View() in a lipgloss card.
// The card is a square-cornered border in one fixed color (no phase glow, no
// focus flip); the only text on the border is the provider/model label at the
// bottom-right. Outer width is always composerOuterWidth; inner matches
// composerInnerWidth.
func renderComposer(taView string, width int, modelLabel string) string {
	width = composerOuterWidth(width)
	innerW := composerInnerWidth(width)

	borderStyle := tuiUserStyle
	top := composerTopBorder(width, borderStyle)
	bot := composerBottomBorder(width, borderStyle, modelLabel)

	body := strings.TrimRight(taView, "\n")
	if body == "" {
		body = " "
	}
	var b strings.Builder
	b.WriteString(top)
	for _, line := range strings.Split(body, "\n") {
		b.WriteByte('\n')
		if lipgloss.Width(line) > innerW {
			line = lipgloss.NewStyle().MaxWidth(innerW).Render(line)
		}
		pad := innerW - lipgloss.Width(line)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(borderStyle.Render("│ "))
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(borderStyle.Render(" │"))
	}
	b.WriteByte('\n')
	b.WriteString(bot)
	return b.String()
}

// composerTopBorder renders the square top border line: ┌────┐
func composerTopBorder(width int, border lipgloss.Style) string {
	return border.Render("┌" + strings.Repeat("─", max(1, width-2)) + "┐")
}

// composerBottomBorder renders the square bottom border line with the
// provider/model label right-aligned on it: └──── model ┘
// The label is dropped when the terminal is too narrow to fit it.
func composerBottomBorder(width int, border lipgloss.Style, modelLabel string) string {
	labW := lipgloss.Width(modelLabel)
	// "└" + dashes + " " + label + " ┘" must total width cells.
	if labW+4 > width {
		return border.Render("└" + strings.Repeat("─", max(1, width-2)) + "┘")
	}
	dashN := width - labW - 4
	return border.Render("└"+strings.Repeat("─", dashN)) +
		border.Render(" "+modelLabel+" ") +
		border.Render("┘")
}
