// Package cli implements mivia command handlers.
package cli

import (
	"strings"

	"github.com/rivo/uniseg"
)

// replHelpContent is the classic REPL's help: catalog-driven Commands plus
// hand-written key sections. The TUI documents its keys in tuiHelpContent
// (tui_help_content.go); the two surfaces bind different keys and must never
// share a help source.
func replHelpContent() []helpSection {
	return append(replHelpCommands(), replHelpKeySections()...)
}

// replHelpCommands lists plain-surface slash commands from the catalog so
// classic help cannot drift from what the REPL handles.
func replHelpCommands() []helpSection {
	commands := slashCommands(slashSurfacePlain, nil)
	items := make([]helpItem, 0, len(commands))
	for _, command := range commands {
		key := command.Name
		if len(command.Aliases) > 0 {
			key += " " + command.Aliases[0]
			for _, alias := range command.Aliases[1:] {
				key += " " + alias
			}
		}
		if command.ArgsHint != "" {
			key += " " + command.ArgsHint
		}
		items = append(items, helpItem{key: key, desc: command.Description})
	}
	return []helpSection{{title: "Commands", items: items}}
}

// replHelpKeySections documents classic REPL line-editor bindings only
// (not slash commands). Arrows are written once, correctly.
func replHelpKeySections() []helpSection {
	return []helpSection{
		{
			title: "Session Keys",
			items: []helpItem{
				{key: "Ctrl-C at prompt", desc: "Exit session"},
				{key: "Ctrl-C while busy", desc: "Cancel current turn"},
				{key: "Ctrl-D", desc: "Exit session"},
			},
		},
		{
			title: "Editing Keys",
			items: []helpItem{
				{key: "↑ ↓", desc: "History navigation"},
				{key: "← →", desc: "Move cursor"},
				{key: "Home / End", desc: "Move to start/end of line"},
				{key: "Backspace / Delete", desc: "Delete character"},
				{key: "Ctrl+U", desc: "Kill entire line"},
				{key: "Ctrl+W", desc: "Kill word before cursor"},
				{key: "Esc / q", desc: "Close this dialog"},
				{key: "Tab", desc: "Command completion"},
			},
		},
	}
}

type helpSection struct {
	title string
	items []helpItem
}

type helpItem struct {
	key  string
	desc string
}

// ShowHelpDialog draws a bordered help dialog and waits for Esc or 'q'.
func ShowHelpDialog(t *Terminal) error {
	w, h := t.Size()
	if w < 50 || h < 10 {
		return displayInlineHelp(t)
	}
	lines, boxW, contentH, topRow, leftCol := helpDialogLayout(w, h)

	// Draw dialog.
	t.SaveScreen()
	t.HideCursor()
	defer t.ShowCursor()
	defer t.RestoreScreen()

	drawHelpDialog(t, lines, boxW, contentH, topRow, leftCol)
	return waitHelpDialog(t)
}

func helpDialogLayout(w, h int) ([]string, int, int, int, int) {
	maxH := h - 4
	boxW := min(72, max(40, w-4))
	lines := renderHelpLines(boxW - 2)
	contentH := min(len(lines), maxH)
	return lines[:contentH], boxW, contentH, max(1, (h-contentH-2)/2), max(1, (w-boxW)/2)
}

func drawHelpDialog(t *Terminal, lines []string, boxW, contentH, topRow, leftCol int) {
	t.MoveTo(topRow, leftCol)
	t.WriteString("┌" + strings.Repeat("─", boxW-2) + "┐")
	for i, line := range lines {
		t.MoveTo(topRow+1+i, leftCol)
		padding := max(0, boxW-2-runeWidth(line))
		t.WriteString("│" + line + strings.Repeat(" ", padding) + "│")
	}
	t.MoveTo(topRow+1+contentH, leftCol)
	t.WriteString("└" + strings.Repeat("─", boxW-2) + "┘")
	footerLine := dim("Esc / q - close")
	t.MoveTo(topRow+contentH+2, leftCol)
	pad := max(0, boxW-2-runeWidth(footerLine))
	t.WriteString(" " + footerLine + strings.Repeat(" ", pad) + " ")
}

func waitHelpDialog(t *Terminal) error {
	for {
		key, err := t.ReadKey()
		if err != nil {
			return err
		}
		if key == "\033" || key == "q" || key == "Q" {
			break
		}
	}
	return nil
}

// renderHelpLines produces the formatted lines of help content,
// each without leading/trailing border characters, fitting within maxW columns.
func renderHelpLines(maxW int) []string {
	var out []string
	for _, section := range replHelpContent() {
		out = append(out, bold(section.title))
		for _, item := range section.items {
			keyW := runeWidth(item.key)
			padding := 26 - keyW
			if padding < 1 {
				padding = 1
			}
			descW := runeWidth(item.desc)
			maxDesc := maxW - keyW - padding
			if maxDesc < 5 {
				maxDesc = 5
			}
			desc := item.desc
			if descW > maxDesc {
				desc = truncateToWidth(desc, maxDesc-3) + "..."
			}
			line := "  " + item.key + strings.Repeat(" ", padding) + dim(desc)
			if runeWidth(line) > maxW {
				line = truncateToWidth(line, maxW-3) + "..."
			}
			out = append(out, line)
		}
		// Blank line between sections.
		out = append(out, "")
	}
	return out
}

// renderReplHelpInline flattens the same help sections the dialog uses into a
// plain-text block for too-small terminals and no-term stderr fallbacks.
func renderReplHelpInline() string {
	var b strings.Builder
	for _, section := range replHelpContent() {
		b.WriteString(section.title)
		b.WriteByte('\n')
		for _, item := range section.items {
			keyW := runeWidth(item.key)
			padding := 26 - keyW
			if padding < 1 {
				padding = 1
			}
			b.WriteString("  ")
			b.WriteString(item.key)
			b.WriteString(strings.Repeat(" ", padding))
			b.WriteString(item.desc)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func displayInlineHelp(t *Terminal) error {
	t.ClearLine()
	t.WriteString("\n  (terminal too small for dialog - inline help below)\n\n")
	t.WriteString(renderReplHelpInline())
	t.WriteString("\n  " + dim("Press Enter to continue"))
	// Wait for any key.
	for {
		key, err := t.ReadKey()
		if err != nil {
			return err
		}
		if key == "\r" || key == "\n" || key == "\033" {
			break
		}
	}
	return nil
}

func bold(s string) string {
	return "\033[1m" + s + "\033[22m"
}

func dim(s string) string {
	return "\033[2m" + s + "\033[22m"
}

func truncateToWidth(s string, maxW int) string {
	var out strings.Builder
	w := 0
	for g := uniseg.NewGraphemes(s); g.Next(); {
		cluster := g.Str()
		width := uniseg.StringWidth(cluster)
		if w+width > maxW {
			break
		}
		out.WriteString(cluster)
		w += width
	}
	return out.String()
}
