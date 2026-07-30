package cli

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// composer chrome: bordered card around textarea.View(), matching
// formatUserMessageCard (msgcard.go) so input feels like a chat bubble.
// Keyboard hints stay outside the card (View chrome line).

// composerOuterWidth floors the outer card width for layout.
func composerOuterWidth(width int) int {
	if width < 20 {
		return 20
	}
	return width
}

func composerInnerWidth(width int) int {
	// "│ " + content + " │" — must match renderComposer inner.
	outer := composerOuterWidth(width)
	inner := outer - 4
	if inner < 8 {
		return 8
	}
	return inner
}

// composerMaxHeight returns textarea line capacity for the terminal height.
func composerMaxHeight(termH int) int {
	h := min(8, max(3, termH/6))
	if termH < 12 {
		h = 3
	}
	return h
}

// renderComposer wraps textarea.View() in a lipgloss card.
// States: idle focused, waiting (queue mode), empty draft.
// stepDetail and stalledWarning are heartbeat info for long-running tasks.
// Outer width is always composerOuterWidth; inner matches composerInnerWidth.
func renderComposer(taView string, width int, waiting bool, queueLen int, focused bool, stepDetail string, stalledWarning bool) string {
	width = composerOuterWidth(width)
	innerW := composerInnerWidth(width)
	_ = queueLen

	borderStyle := tuiUserStyle
	switch {
	case waiting && focused:
		borderStyle = tuiWaitingStyle
	case waiting:
		// Blurred mid-turn: the user paged into scrollback, so the textarea is
		// ignoring every keystroke. Testing `waiting` first made this identical to
		// the focused state, which is the one moment the difference matters.
		borderStyle = tuiDimStyle
	case focused:
		borderStyle = tuiInfoStyle
	}

	headerLabel := "you"
	if waiting {
		headerLabel = "you · queue"
	}
	if !focused {
		// Blurred means the textarea drops every keystroke. Say it in text, not
		// only in the border colour: a terminal with no colour profile renders
		// every border style identically, which is how this state stayed invisible.
		headerLabel += " · esc to type"
	}
	top := composerTopBorder(width, headerLabel, borderStyle)
	bot := composerBottomBorder(width, waiting, borderStyle, stepDetail, stalledWarning)

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

func composerTopBorder(width int, label string, border lipgloss.Style) string {
	labW := lipgloss.Width(label)
	dashN := width - 3 - labW - 1 - 1
	if dashN < 1 {
		dashN = 1
	}
	return border.Render("╭─ ") +
		tuiUserLabel.Render(label) +
		border.Render(" "+strings.Repeat("─", dashN)+"╮")
}

func composerBottomBorder(width int, waiting bool, border lipgloss.Style, stepDetail string, stalledWarning bool) string {
	if !waiting {
		return border.Render("╰" + strings.Repeat("─", width-2) + "╯")
	}
	note := ""
	if stalledWarning {
		note = " ⚠ stalled "
	} else if stepDetail != "" {
		note = " " + stepDetail + " "
	} else {
		note = " queued "
	}
	noteW := lipgloss.Width(note)
	fdash := width - 2 - 1 - noteW
	if fdash < 1 {
		return border.Render("╰" + strings.Repeat("─", width-2) + "╯")
	}
	if stalledWarning {
		return border.Render("╰─") +
			tuiErrorStyle.Render(note) +
			border.Render(strings.Repeat("─", fdash-1)+"╯")
	}
	return border.Render("╰─") +
		tuiWaitingStyle.Render(note) +
		border.Render(strings.Repeat("─", fdash-1)+"╯")
}
