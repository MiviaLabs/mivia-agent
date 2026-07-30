package cli

// tuiHelpContent is the /help dialog content for the TUI, and the single
// place TUI key documentation lives. Every key named here must be really
// bound (INV-TUI-23, TestSlashHelpMatchesRealBindings asserts on the rendered
// dialog). The classic REPL documents its own keys in replHelpContent —
// the two surfaces bind different keys and must never share a help source.
var tuiHelpContent = []helpSection{
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
			{key: "/plain", desc: "How to use the classic UI"},
		},
	},
	{
		title: "Sending",
		items: []helpItem{
			{key: "Enter", desc: "Send message"},
			{key: "Alt+Enter", desc: "Insert newline"},
			{key: "Enter (empty, busy)", desc: "Force-send next queued message"},
			{key: "Type while busy", desc: "Queues the message"},
		},
	},
	{
		title: "Cancel & quit",
		items: []helpItem{
			{key: "Ctrl+C (busy)", desc: "Cancel the current turn"},
			{key: "Ctrl+C (idle)", desc: "Copy selected message, else quit"},
			{key: "Ctrl+Q", desc: "Quit"},
		},
	},
	{
		title: "Navigation",
		items: []helpItem{
			{key: "Tab / Shift+Tab", desc: "Cycle composer and history blocks"},
			{key: "Esc", desc: "Back to composer, clear selection"},
			{key: "PgUp / PgDn", desc: "Page the transcript"},
			{key: "Home / End", desc: "Oldest message / back to latest"},
			{key: "↑ / ↓", desc: "Line by line (transcript focused)"},
			{key: "Enter / Space", desc: "Expand or collapse selected block"},
			{key: "o", desc: "Open selected block detail"},
			{key: "j / k", desc: "Scroll selected work group"},
			{key: "Ctrl+G", desc: "Fleet detail (subagent activity)"},
		},
	},
	{
		title: "Editing",
		items: []helpItem{
			{key: "← / →", desc: "Move cursor"},
			{key: "Ctrl+A", desc: "Start of line"},
			{key: "Ctrl+← / Ctrl+→", desc: "Word back / word forward (also Alt+←/→)"},
			{key: "Ctrl+U / Ctrl+K", desc: "Delete to line start / to line end"},
			{key: "Ctrl+W / Alt+Backspace", desc: "Delete word before cursor"},
		},
	},
	{
		title: "Copying",
		items: []helpItem{
			{key: "y or Ctrl+Y", desc: "Copy selected message"},
			{key: "Right-click", desc: "Copy clicked message"},
			{key: "Ctrl+E", desc: "Select mode: terminal owns the mouse"},
			{key: "Shift+drag", desc: "Bypass mouse capture (Option on iTerm2)"},
			{key: "", desc: "Delivery: wl-copy/xclip/xsel/pbcopy, else OSC 52 (tmux: set-clipboard on)"},
		},
	},
	{
		title: "Panels & mouse",
		items: []helpItem{
			{key: "Ctrl+T", desc: "Toggle live thinking"},
			{key: "Ctrl+R", desc: "Toggle run dashboard"},
			{key: "Ctrl+L", desc: "Clear the screen"},
			{key: "Ctrl+O (welcome)", desc: "Continue last session"},
			{key: "Mouse", desc: "Wheel scrolls; click selects; MIVIA_MOUSE=0/1 overrides"},
		},
	},
}
