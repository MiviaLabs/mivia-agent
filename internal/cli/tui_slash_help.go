package cli

const slashHelpMD = `
### Commands
- **/help** — this help
- **/exit** / **/quit** — leave chat
- **/clear** — clear history
- **/status** — provider, model, tokens
- **/model** ` + "`name`" + ` — e.g. deepseek-v4-pro
- **/tools** — list tools
- **/save** / **/load** / **/list** / **/delete** — sessions
- **/plain** — how to use classic UI
### Keys
- **Enter** send · **Alt+Enter** newline
- **Ctrl+C** cancel in-flight or quit at idle
- **Ctrl+D** quit
- **Tab** / **Shift+Tab** — cycle between composer and scrollback
- **Ctrl+T** — toggle live thinking visibility
- **Ctrl+M** — toggle mouse (auto-on when terminal supports it)
- **Ctrl+O** (welcome) — continue last session
- **Esc** — return to composer
### Queueing
While agent is busy, type + **Enter** queues a message.
**Enter** on empty input force-sends queued message (cancels current turn).
Queued messages auto-send when current turn finishes.
### Mouse
Enabled automatically when stdin is a TTY and TERM is not dumb
(override with MIVIA_MOUSE=0/1). Scroll wheel moves chat history;
click a message block to select; click composer to return. Ctrl+M toggles.
`
