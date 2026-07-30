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
- **/resume** — list interrupted runs; ` + "`/resume <run-id>`" + ` resumes an interrupted run
- **/plain** — how to use classic UI
### Keys
- **Enter** send · **Alt+Enter** newline
- **Ctrl+C** cancel in-flight; at idle it copies a selected message, else quits
- **Ctrl+Q** — quit
- **Tab** / **Shift+Tab** — cycle between composer and scrollback
- **Ctrl+T** — toggle live thinking visibility
- **Ctrl+S** — select mode: hands the mouse back to the terminal so you can
  drag-select and copy anything, including the composer
- **Ctrl+M** — toggle mouse (auto-on when terminal supports it)
- **Ctrl+R** — toggle the run dashboard
- **Ctrl+O** (welcome) — continue last session
### Copying
- **y** or **Ctrl+Y** — copy the selected message to the system clipboard
- **Right click** a message — copy it
- **Ctrl+S** — select mode for arbitrary text (the terminal does the selecting)
- **Esc** — return to composer
### Scrolling history (no mouse needed)
- **PgUp** / **PgDn** — page the transcript; PgUp also hands it focus
- **Home** / **End** — jump to the oldest message / back to the latest
- **↑** / **↓** — line by line, once the transcript has focus
- **Esc** or any letter returns focus to the composer. While it is blurred the
  composer header reads ` + "`you · esc to type`" + ` and drops keystrokes.
Letter keys are never scroll keys: they belong to the composer.
### Queueing
While agent is busy, type + **Enter** queues a message.
**Enter** on empty input force-sends queued message (cancels current turn).
Queued messages auto-send when current turn finishes.
### Mouse
Enabled automatically when stdin is a TTY and TERM is not dumb
(override with MIVIA_MOUSE=0/1). Scroll wheel moves chat history;
click a message block to select; click composer to return. Ctrl+M toggles.
`
