package cli

import "github.com/MiviaLabs/mivia-agent/internal/skills"

// tuiHelpContent is the /help dialog content: slash commands, the key
// sections generated from keyRegistry (keymap.go), and the notes that are
// neither. Keys are never listed by hand here - hand-maintained key docs are
// exactly how /help came to advertise the classic REPL's bindings in a UI
// that implements none of them. The REPL keeps its own replHelpContent.
func tuiHelpContent() []helpSection {
	return tuiHelpContentFor(nil)
}

func tuiHelpContentFor(registry *skills.Registry) []helpSection {
	sections := append(tuiHelpCommandsFor(registry), keyHelpSections(keyRegistry)...)
	return append(sections, tuiHelpNotes()...)
}

// tuiHelpCommands lists the slash commands, which are not key bindings.
func tuiHelpCommands() []helpSection {
	return tuiHelpCommandsFor(nil)
}

func tuiHelpCommandsFor(registry *skills.Registry) []helpSection {
	commands := slashCommands(slashSurfaceTUI, registry)
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
	return []helpSection{
		{
			title: "Commands",
			items: items,
		},
	}
}

// tuiHelpNotes closes /help with what is neither a command nor a single key:
// queueing behaviour, mouse, and how a copy actually reaches the clipboard.
func tuiHelpNotes() []helpSection {
	return []helpSection{
		{
			title: "Good to know",
			items: []helpItem{
				{key: "Type while busy", desc: "Queues the message; empty Enter force-sends it"},
				{key: "Shift+drag", desc: "Selects text without leaving mouse capture (Option on iTerm2)"},
				{key: "Mouse", desc: "Wheel scrolls, click selects, right-click copies (MIVIA_MOUSE=0/1)"},
				{key: "Copy delivery", desc: "wl-copy/xclip/xsel/pbcopy/clip, else OSC 52 (tmux: set-clipboard on)"},
				{key: "Ctrl+O (welcome)", desc: "Continue the last session"},
			},
		},
	}
}
