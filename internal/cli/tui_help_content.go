package cli

// tuiHelpContent is the /help dialog content: slash commands, the key
// sections generated from keyRegistry (keymap.go), and the notes that are
// neither. Keys are never listed by hand here — hand-maintained key docs are
// exactly how /help came to advertise the classic REPL's bindings in a UI
// that implements none of them. The REPL keeps its own replHelpContent.
func tuiHelpContent() []helpSection {
	sections := append(tuiHelpCommands(), keyHelpSections(keyRegistry)...)
	return append(sections, tuiHelpNotes()...)
}

// tuiHelpCommands lists the slash commands, which are not key bindings.
func tuiHelpCommands() []helpSection {
	return []helpSection{
		{
			title: "Commands",
			items: []helpItem{
				{key: "/help /h /?", desc: "This help"},
				{key: "/sessions", desc: "Manage sessions: switch, delete, purge"},
				{key: "/save /load /list /delete", desc: "Sessions by name"},
				{key: "/resume [run-id]", desc: "List or resume interrupted runs"},
				{key: "/model /budget /steps", desc: "Model, context budget, max steps"},
				{key: "/status /tools", desc: "Session info, agent tools"},
				{key: "/search <query>", desc: "Web search"},
				{key: "/clear", desc: "Clear history (saved first)"},
				{key: "/new", desc: "Start a fresh session (current one saved)"},
				{key: "/select", desc: "Select mode (same as F2)"},
				{key: "/plain", desc: "How to use the classic UI"},
			},
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
				{key: "Copy delivery", desc: "wl-copy/xclip/xsel/pbcopy, else OSC 52 (tmux: set-clipboard on)"},
				{key: "Ctrl+O (welcome)", desc: "Continue the last session"},
			},
		},
	}
}
