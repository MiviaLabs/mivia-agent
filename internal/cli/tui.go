// Package cli — Bubble Tea TUI for mivia chat (agent mode).
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

var (
	tuiHeaderStyle   = lipgloss.NewStyle().Faint(true)
	tuiUserStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	tuiDimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	tuiInfoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	tuiBarStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Background(lipgloss.Color("236"))
	tuiAccentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	tuiThinkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Italic(true) // magenta italic
)

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type tuiTickMsg struct{}

// ---------------------------------------------------------------------------
// streamBridge — agent goroutine → UI (coalesced, no goroutine storms)
// ---------------------------------------------------------------------------

type bridgeToolEvt struct {
	Start  bool
	Name   string
	Detail string
	At     time.Time
}

type streamBridge struct {
	mu      sync.Mutex
	pending strings.Builder
	tools   []bridgeToolEvt
	done    bool
	doneErr error
	notify  chan struct{}
	closed  bool
	// Thinking buffer: model reasoning text between tool calls.
	thinking    strings.Builder
	activeTools int // tracks outstanding tool calls for thinking dedup
}

func newStreamBridge() *streamBridge {
	return &streamBridge{notify: make(chan struct{}, 1)}
}

func (b *streamBridge) signal() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

func (b *streamBridge) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return len(p), nil
	}
	const maxPending = 512 * 1024
	if b.pending.Len()+len(p) > maxPending {
		cur := b.pending.String()
		keep := maxPending / 2
		if len(cur) > keep {
			b.pending.Reset()
			b.pending.WriteString(cur[len(cur)-keep:])
		}
	}
	b.pending.Write(p)
	b.mu.Unlock()
	b.signal()
	return len(p), nil
}

// PushThinking appends model reasoning text (EventAssistant content).
// Only stores thinking when there are active tool calls, to avoid
// duplicating text that also flows through the stream buffer.
func (b *streamBridge) PushThinking(text string) {
	if text == "" {
		return
	}
	b.mu.Lock()
	if b.closed || b.activeTools == 0 {
		b.mu.Unlock()
		return
	}
	const maxThinking = 64 * 1024
	if b.thinking.Len()+len(text) > maxThinking {
		// Keep the tail end of thinking.
		cur := b.thinking.String()
		keep := maxThinking / 2
		if len(cur) > keep {
			b.thinking.Reset()
			b.thinking.WriteString(cur[len(cur)-keep:])
		}
	}
	b.thinking.WriteString(text)
	b.thinking.WriteByte('\n')
	b.mu.Unlock()
	b.signal()
}

func (b *streamBridge) PushTool(start bool, name, detail string) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if start {
		b.activeTools++
	} else if b.activeTools > 0 {
		b.activeTools--
	}
	if len(b.tools) < 500 {
		b.tools = append(b.tools, bridgeToolEvt{
			Start: start, Name: name, Detail: detail, At: time.Now(),
		})
	}
	b.mu.Unlock()
	b.signal()
}

func (b *streamBridge) Finish(err error) {
	b.mu.Lock()
	b.done = true
	b.doneErr = err
	b.mu.Unlock()
	b.signal()
}

func (b *streamBridge) Drain() (stream string, tools []bridgeToolEvt, done bool, doneErr error, thinking string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stream = b.pending.String()
	b.pending.Reset()
	tools = b.tools
	b.tools = nil
	done = b.done
	doneErr = b.doneErr
	if done {
		b.done = false
		b.doneErr = nil
	}
	thinking = b.thinking.String()
	b.thinking.Reset()
	return
}

func (b *streamBridge) Close() {
	b.mu.Lock()
	b.closed = true
	b.activeTools = 0
	b.mu.Unlock()
}

// ---------------------------------------------------------------------------
// tuiModel
// ---------------------------------------------------------------------------

type tuiModel struct {
	session   *chat.Session
	config    *config.Resolved
	toolsOn   bool
	modelName string

	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model

	messages []string

	bridge      *streamBridge
	streamBuf   strings.Builder
	waiting     bool
	turnStart   time.Time
	toolRows    []toolRow
	thinkingBuf strings.Builder // accumulated model reasoning text (shown on demand)
	cancel      context.CancelFunc
	mu          sync.Mutex

	// UI state
	selectedTool  int      // index into toolRows, -1 = none
	showThinking  bool     // toggle thinking panel
	thinkingLines int      // cached line count for thinking panel
	pendingQueue  []string // messages queued while agent is busy
	msgOffset     int      // index into session.Messages for oldest loaded message

	// Welcome screen (no auto-load on launch).
	mode          screenMode
	logoFrame     int
	sessions      []chat.SessionInfo
	sessionSel    int
	sessionScroll int
	sessionHits   []sessionRowHit
	lastClickIdx  int
	lastClickAt   time.Time

	width  int
	height int
	ready  bool
}

func newTUIModel(sess *chat.Session, res *config.Resolved, toolsOn bool) *tuiModel {
	ti := textarea.New()
	ti.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
	ti.Focus()
	ti.Prompt = "❯ "
	ti.CharLimit = 0
	ti.SetWidth(80)
	ti.SetHeight(3)
	ti.ShowLineNumbers = false
	ti.KeyMap.InsertNewline.SetEnabled(true)

	s := spinner.New()
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	s.Spinner = spinner.Dot

	m := &tuiModel{
		session:       sess,
		config:        res,
		toolsOn:       toolsOn,
		modelName:     shortenModel(sess.Model),
		viewport:      viewport.New(80, 20),
		textarea:      ti,
		spinner:       s,
		bridge:        newStreamBridge(),
		messages:      []string{},
		selectedTool:  -1,
		showThinking:  false,
		pendingQueue:  []string{},
		msgOffset:     0,
		mode:          modeWelcome,
		lastClickIdx:  -1,
		sessionSel:    0,
		sessionScroll: 0,
	}
	m.refreshSessionList()
	ti.Placeholder = "Type to start a new chat…  or select a session ↑↓"
	return m
}

func (m *tuiModel) refreshSessionList() {
	list, err := m.session.ListSessions()
	if err != nil {
		m.sessions = nil
		return
	}
	m.sessions = list
	if m.sessionSel >= len(m.sessions) {
		m.sessionSel = max(0, len(m.sessions)-1)
	}
}

func (m *tuiModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tea.EnterAltScreen, logoTickCmd())
}

func (m *tuiModel) pollCmd() tea.Cmd {
	return func() tea.Msg {
		m.mu.Lock()
		bridge := m.bridge
		m.mu.Unlock()
		if bridge == nil {
			return nil
		}
		select {
		case <-bridge.notify:
			return tuiTickMsg{}
		case <-time.After(80 * time.Millisecond):
			return tuiTickMsg{}
		}
	}
}

// consumeToolNavKey reports whether a single-letter tool/viewport bind should
// run instead of being typed into the composer.
// space/e/E only when a tool row is selected; G (scroll-to-bottom) only when
// the composer is empty so it never steals typing.
func consumeToolNavKey(selectedTool int, key string, textareaEmpty bool) bool {
	switch key {
	case " ", "e", "E":
		return selectedTool >= 0
	case "G":
		return textareaEmpty
	default:
		return false
	}
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	// When true, Enter/tool-nav already handled the key — do not also feed it
	// to the textarea (would insert a newline after Reset, or steal e/space).
	skipTextarea := false

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		if m.mode == modeChat {
			m.renderVP()
		}

	case logoTickMsg:
		// Animate brand mark on welcome and whenever the agent is working.
		if m.mode == modeWelcome || m.waiting {
			m.logoFrame++
			return m, logoTickCmd()
		}

	case tea.KeyMsg:
		// Always allow Ctrl+C — cancels in-flight or quits when idle.
		if msg.String() == "ctrl+c" {
			if m.waiting {
				m.mu.Lock()
				if m.cancel != nil {
					m.cancel()
				}
				// Close bridge so stale goroutine output is discarded.
				m.bridge.Close()
				m.mu.Unlock()
				// Immediately reset UI state so user can type freely.
				m.waiting = false
				m.toolRows = nil
				m.selectedTool = -1
				m.streamBuf.Reset()
				m.layout()
				m.renderVP()
				m.textarea.Reset()
				m.appendInfo("(cancelled — type a new message)")
			} else {
				return m, tea.Quit
			}
			break
		}

		// Escape: deselect tool, collapse all expanded, exit modes.
		if msg.String() == "esc" {
			if m.mode == modeWelcome {
				// No-op on welcome (do not quit — use ctrl+c).
				skipTextarea = true
				break
			}
			m.selectedTool = -1
			for i := range m.toolRows {
				m.toolRows[i].Expanded = false
			}
			m.layout()
			break
		}

		// Welcome: session list navigation (mouse also supported).
		// j/k only when composer is empty so they never steal typing.
		if m.mode == modeWelcome {
			composerEmpty := strings.TrimSpace(m.textarea.Value()) == ""
			key := msg.String()
			nav := false
			switch key {
			case "up":
				nav = true
			case "down":
				nav = true
			case "k", "j":
				nav = composerEmpty
			case "pgup", "pgdown", "home", "end":
				nav = true
			}
			if nav {
				switch key {
				case "up", "k":
					if m.sessionSel > 0 {
						m.sessionSel--
					}
				case "down", "j":
					if m.sessionSel < len(m.sessions)-1 {
						m.sessionSel++
					}
				case "pgup":
					m.sessionSel = max(0, m.sessionSel-10)
				case "pgdown":
					m.sessionSel = min(len(m.sessions)-1, m.sessionSel+10)
					if m.sessionSel < 0 {
						m.sessionSel = 0
					}
				case "home":
					m.sessionSel = 0
				case "end":
					if len(m.sessions) > 0 {
						m.sessionSel = len(m.sessions) - 1
					}
				}
				skipTextarea = true
			}
		}

		switch msg.String() {
		case "ctrl+d":
			return m, tea.Quit
		case "enter":
			if msg.Alt {
				m.textarea.InsertString("\n")
				skipTextarea = true
				break
			}
			userText := strings.TrimSpace(m.textarea.Value())

			// Welcome screen: text → new session + send; empty → open selected.
			if m.mode == modeWelcome {
				skipTextarea = true
				if userText == "exit" || userText == "quit" {
					return m, tea.Quit
				}
				if userText != "" {
					m.beginNewSession()
					m.enterChatMode()
					m.textarea.Reset()
					m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
					if strings.HasPrefix(userText, "/search") {
						query := strings.TrimSpace(userText[7:])
						if query == "" {
							m.appendInfo("usage: /search <query>")
							m.renderVP()
							return m, nil
						}
						userText = "search the web for: " + query
					}
					if strings.HasPrefix(userText, "/") {
						if m.handleSlash(userText) {
							m.renderVP()
							return m, nil
						}
					}
					m.startAI(userText)
					return m, tea.Batch(m.pollCmd(), logoTickCmd())
				}
				// Empty enter: open selected session if any.
				if len(m.sessions) == 0 {
					break
				}
				if err := m.openSelectedSession(); err != nil {
					break
				}
				m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
				return m, nil
			}

			if userText == "" {
				// Empty Enter while waiting — if queue has items, force-send.
				if m.waiting && len(m.pendingQueue) > 0 {
					m.forceSendQueued()
					return m, tea.Batch(m.pollCmd(), logoTickCmd())
				}
				// Empty Enter: do not insert a newline into the composer.
				skipTextarea = true
				break
			}
			if userText == "exit" || userText == "quit" {
				return m, tea.Quit
			}

			// Handle /search transform.
			if strings.HasPrefix(userText, "/search") {
				query := strings.TrimSpace(userText[7:])
				if query == "" {
					m.appendInfo("usage: /search <query>")
					m.renderVP()
					m.textarea.Reset()
					skipTextarea = true
					break
				}
				userText = "search the web for: " + query
			}

			// Handle slash commands (only when not waiting — no AI needed).
			if !m.waiting && strings.HasPrefix(userText, "/") {
				if m.handleSlash(userText) {
					m.renderVP()
					m.textarea.Reset()
					skipTextarea = true
					break
				}
			}

			if m.waiting {
				// Queue message for later. Early return so Enter is not also
				// applied to the textarea after Reset (would insert a newline).
				m.pendingQueue = append(m.pendingQueue, userText)
				m.textarea.Reset()
				m.appendInfo(fmt.Sprintf("(queued: %s — %d pending, empty enter=force)", truncateStr(userText, 40), len(m.pendingQueue)))
				m.renderVP()
				return m, tea.Batch(cmds...)
			}
			// Send immediately.
			m.startAI(userText)
			return m, tea.Batch(m.pollCmd(), logoTickCmd())

		case "ctrl+l":
			m.messages = nil
			m.msgOffset = 0
			m.viewport.SetContent("")

		// --- Tool navigation ---
		case "tab":
			if len(m.toolRows) > 0 {
				m.selectedTool++
				if m.selectedTool >= len(m.toolRows) {
					m.selectedTool = -1 // wrap to none-selected
				}
			}
		case "shift+tab":
			if len(m.toolRows) > 0 {
				m.selectedTool--
				if m.selectedTool < -1 {
					m.selectedTool = len(m.toolRows) - 1
				}
			}

		// --- Toggle thinking display ---
		case "ctrl+t":
			m.showThinking = !m.showThinking
			m.layout()

		// --- Toggle expand on selected tool (only when a row is selected) ---
		case " ":
			if consumeToolNavKey(m.selectedTool, " ", strings.TrimSpace(m.textarea.Value()) == "") &&
				m.selectedTool < len(m.toolRows) {
				m.toolRows[m.selectedTool].Expanded = !m.toolRows[m.selectedTool].Expanded
				m.layout()
				skipTextarea = true
			}

		// --- Expand all / collapse all (only when a tool row is selected) ---
		case "e":
			if consumeToolNavKey(m.selectedTool, "e", strings.TrimSpace(m.textarea.Value()) == "") {
				for i := range m.toolRows {
					m.toolRows[i].Expanded = true
				}
				m.layout()
				skipTextarea = true
			}
		case "E":
			if consumeToolNavKey(m.selectedTool, "E", strings.TrimSpace(m.textarea.Value()) == "") {
				for i := range m.toolRows {
					m.toolRows[i].Expanded = false
				}
				m.layout()
				skipTextarea = true
			}
		// --- Scroll to bottom (only when composer is empty) ---
		case "G":
			if consumeToolNavKey(m.selectedTool, "G", strings.TrimSpace(m.textarea.Value()) == "") {
				m.viewport.GotoBottom()
				skipTextarea = true
			}
		}

	case tea.MouseMsg:
		if m.mode == modeWelcome {
			// Wheel: scroll session list.
			if msg.Type == tea.MouseWheelUp {
				if m.sessionSel > 0 {
					m.sessionSel--
				}
			} else if msg.Type == tea.MouseWheelDown {
				if m.sessionSel < len(m.sessions)-1 {
					m.sessionSel++
				}
			} else if msg.Type == tea.MouseLeft {
				idx := m.sessionIndexAtY(msg.Y)
				if idx >= 0 {
					// Double-click opens; single-click selects.
					now := time.Now()
					if idx == m.lastClickIdx && now.Sub(m.lastClickAt) < 400*time.Millisecond {
						m.sessionSel = idx
						if err := m.openSelectedSession(); err == nil {
							m.textarea.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
						}
						m.lastClickIdx = -1
					} else {
						m.sessionSel = idx
						m.lastClickIdx = idx
						m.lastClickAt = now
					}
				}
			}
			break
		}
		// Chat: any mouse click scrolls to bottom (the ↓ indicator region matches).
		if msg.Type == tea.MouseLeft {
			// Only jump to bottom if actually scrolled up.
			if !m.viewport.AtBottom() {
				m.viewport.GotoBottom()
			}
		}

	case tuiTickMsg:
		if !m.waiting {
			return m, nil
		}
		stream, toolEvts, done, doneErr, thinking := m.bridge.Drain()
		m.applyToolEvents(toolEvts)
		needsLayout := len(toolEvts) > 0
		if stream != "" {
			m.streamBuf.WriteString(stream)
		}
		if thinking != "" {
			m.thinkingBuf.WriteString(thinking)
			m.thinkingLines += strings.Count(thinking, "\n")
		}
		if needsLayout {
			m.layout()
		}
		m.renderStreamVP()
		if done {
			cmdsFromFinish := m.finishStream(doneErr)
			cmds = append(cmds, cmdsFromFinish...)
			if !m.waiting {
				return m, tea.Batch(cmds...)
			}
			// finishStream started a new turn via queued message.
			return m, tea.Batch(append(cmds, m.pollCmd(), logoTickCmd())...)
		}
		return m, m.pollCmd()

	case spinner.TickMsg:
		if m.waiting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	// Always forward to textarea (idle or busy) unless Enter/tool-nav consumed the key.
	// Previously blocked while waiting, so follow-up typing was impossible.
	if !skipTextarea {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}
	// Always update viewport for resize, mouse, and scroll — but NOT for
	// regular typing keys (those go to textarea and would cause scroll jumping).
	// Only navigation keys (up/down/pgup/pgdown/home/end) trigger viewport scroll.
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(v)
		cmds = append(cmds, vpCmd)
	case tea.MouseMsg:
		if m.mode == modeWelcome {
			// Welcome mouse handled above; do not scroll chat viewport.
			break
		}
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(v)
		cmds = append(cmds, vpCmd)
		// Check if scrolled to top after mouse wheel — lazy-load more history.
		if !m.waiting && m.msgOffset > 0 && m.viewport.YOffset <= 0 && m.viewport.TotalLineCount() > m.viewport.Height {
			m.loadMoreMessages()
		}
	case tea.KeyMsg:
		// Welcome owns ↑↓ for the session picker; do not scroll empty chat viewport.
		if m.mode == modeWelcome {
			break
		}
		k := v.String()
		if k == "up" || k == "down" || k == "pgup" || k == "pgdown" || k == "home" || k == "end" {
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(v)
			cmds = append(cmds, vpCmd)
		}
		// Lazy-load older history when user scrolls toward the top.
		// Trigger near top (YOffset < 3), not only when TotalLineCount > Height —
		// short viewports / single-line messages otherwise never load.
		if k == "pgup" || k == "up" || k == "home" {
			if m.msgOffset > 0 && m.viewport.YOffset < 3 {
				m.loadMoreMessages()
			}
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *tuiModel) layout() {
	headerHeight := 1 // single-line status (idle + working)
	toolPanel := 0
	thinkingPanel := 0

	if m.waiting && len(m.toolRows) > 0 {
		// Calculate tool panel height based on expanded rows.
		toolPanel = min(18, m.calcToolPanelLines())
	}
	if m.showThinking && m.thinkingBuf.Len() > 0 {
		thinkingPanel = min(10, m.thinkingLines+1)
	}

	inputHeight := min(5, max(3, m.textarea.LineCount()+1))
	extraPanels := toolPanel + thinkingPanel
	vpHeight := max(5, m.height-headerHeight-inputHeight-extraPanels-2)
	if !m.ready {
		m.viewport = viewport.New(m.width, vpHeight)
		m.textarea.SetWidth(max(20, m.width-4))
		m.ready = true
	} else {
		m.viewport.Width = m.width
		m.viewport.Height = vpHeight
	}
}

// calcToolPanelLines estimates rendered lines for the tool panel.
func (m *tuiModel) calcToolPanelLines() int {
	if len(m.toolRows) == 0 {
		return 0
	}
	lines := 2 // header + 1 blank
	const maxShow = 20
	start := 0
	if len(m.toolRows) > maxShow {
		start = len(m.toolRows) - maxShow
	}
	for _, r := range m.toolRows[start:] {
		lines++ // tool row
		if r.Expanded {
			if r.Detail != "" {
				lines++ // input header
				lines += min(7, 1+strings.Count(r.Detail, "\n"))
			}
			if r.Result != "" {
				lines++ // output header
				lines += min(7, 1+strings.Count(r.Result, "\n"))
			}
		}
	}
	return lines
}

func (m *tuiModel) applyToolEvents(evts []bridgeToolEvt) {
	for _, e := range evts {
		if e.Start {
			m.toolRows = append(m.toolRows, toolRow{
				Name:   e.Name,
				Detail: e.Detail,
				Start:  e.At,
			})
			continue
		}
		for i := len(m.toolRows) - 1; i >= 0; i-- {
			if m.toolRows[i].Name == e.Name && !m.toolRows[i].Done {
				m.toolRows[i].Done = true
				m.toolRows[i].End = e.At
				m.toolRows[i].Result = e.Detail
				m.toolRows[i].Failed = strings.HasPrefix(strings.ToLower(e.Detail), "error") ||
					strings.Contains(e.Detail, "exit=1") ||
					strings.Contains(e.Detail, "exit=error") ||
					strings.Contains(e.Detail, "exit=timeout")
				break
			}
		}
	}
}

func (m *tuiModel) finishStream(err error) []tea.Cmd {
	m.waiting = false
	raw := m.streamBuf.String()
	m.streamBuf.Reset()

	if strings.TrimSpace(raw) != "" {
		md := RenderMarkdown(raw, max(40, m.width-2))
		if m.width > 20 {
			md = wrapANSIv2(md, m.width-4)
		}
		m.messages = append(m.messages, md)
	}

	if len(m.toolRows) > 0 {
		now := time.Now()
		var summary strings.Builder
		summary.WriteString(tuiDimStyle.Render("  ── tools ──"))
		summary.WriteByte('\n')
		for _, r := range m.toolRows {
			icon := "✓"
			style := toolOkStyle
			if r.Failed {
				icon = "✗"
				style = toolErrStyle
			} else if !r.Done {
				icon = "·"
			}
			summary.WriteString(fmt.Sprintf("  %s %s %s %s\n",
				style.Render(icon),
				toolNameStyle.Render(r.Name),
				tuiDimStyle.Render(truncateStr(firstLine(r.Result, r.Detail), 60)),
				toolTimeStyle.Render(formatDuration(r.elapsed(now))),
			))
		}
		m.messages = append(m.messages, strings.TrimRight(summary.String(), "\n"))
	}

	total := time.Since(m.turnStart)
	if err != nil && err != context.Canceled {
		m.messages = append(m.messages, tuiErrorStyle.Render("error: "+err.Error()))
	} else if err == context.Canceled {
		m.messages = append(m.messages, tuiDimStyle.Render(fmt.Sprintf("(cancelled · %s)", formatDuration(total))))
	} else {
		m.messages = append(m.messages, tuiDimStyle.Render(fmt.Sprintf("  ─ done · %s ─", formatDuration(total))))
	}

	m.toolRows = nil
	m.selectedTool = -1
	m.thinkingBuf.Reset()
	m.thinkingLines = 0
	m.layout()
	m.renderVP()
	// Do not textarea.Reset() here: user may have typed a draft while waiting.
	// startAI / sendNextQueued still Reset after capturing the sent text.
	m.mu.Lock()
	m.cancel = nil
	m.mu.Unlock()

	// Auto-send next queued message if any.
	if len(m.pendingQueue) > 0 {
		m.sendNextQueued()
		if m.waiting {
			return []tea.Cmd{m.pollCmd(), logoTickCmd()}
		}
	}
	return nil
}

func firstLine(a, b string) string {
	s := a
	if s == "" {
		s = b
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (m *tuiModel) View() string {
	if !m.ready {
		return tuiAccentStyle.Render("  mivia") + tuiDimStyle.Render(" starting…")
	}

	// Recalculate layout every frame to ensure viewport fills available space.
	// This is safe because layout() only sets Width/Height and calls SetContent.
	m.layout()

	if m.mode == modeWelcome {
		return m.viewWelcome()
	}

	open, done, total := countTools(m.toolRows)
	phase := deriveBrandPhase(m.waiting, open, m.streamBuf.Len(), len(m.pendingQueue), false)
	color := brandColor(phase)

	var chrome string
	if m.waiting {
		// Full work chrome: animated mini diamond replaces the old spinner.
		chrome = renderWorkChrome(
			m.logoFrame,
			phase,
			m.modelName,
			time.Since(m.turnStart),
			open, done, total,
			len(m.pendingQueue),
			m.width,
		)
	} else {
		// Idle: static brand glyph + name + message count.
		left := renderIdleStatusLeft(m.modelName)
		hint := "/help"
		if m.showThinking {
			hint = "thinking on"
		}
		right := tuiDimStyle.Render(fmt.Sprintf(" %d msgs · %s ", len(m.session.Messages), hint))
		if len(m.pendingQueue) > 0 {
			right = lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorQueue)).Render(
				fmt.Sprintf(" ▣ %d queued ", len(m.pendingQueue)),
			) + right
		}
		lw, rw := lipgloss.Width(left), lipgloss.Width(right)
		spacerN := m.width - lw - rw
		if spacerN < 1 {
			spacerN = 1
		}
		chrome = left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN)) + right
	}

	// Body: viewport + thinking panel + tool panel
	var bodyParts []string
	bodyParts = append(bodyParts, m.viewport.View())

	// Thinking panel (when toggled) — phase-colored header with mini mark.
	if m.showThinking && m.thinkingBuf.Len() > 0 {
		thinkingContent := m.thinkingBuf.String()
		lines := strings.Split(thinkingContent, "\n")
		if len(lines) > 10 {
			lines = lines[len(lines)-10:]
			thinkingContent = strings.Join(lines, "\n")
		}
		hdr := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Italic(true).Render(
			"  " + nanoFirstLine(m.logoFrame, color) + " thinking",
		)
		panel := fmt.Sprintf("%s\n%s%s",
			hdr,
			tuiDimStyle.Render(thinkingContent),
			ansiReset,
		)
		bodyParts = append(bodyParts, "", panel)
	}

	// Tool panel — brand-colored running indicators.
	if m.waiting && len(m.toolRows) > 0 {
		panel, _ := renderToolPanel(m.toolRows, m.width, time.Now(), m.selectedTool, m.logoFrame, phase)
		bodyParts = append(bodyParts, "", panel)
	}

	// Scroll-to-bottom indicator
	if !m.viewport.AtBottom() && m.width > 12 {
		arrow := lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("15")).
			Render(" ↓ ")
		padding := max(0, m.width-lipgloss.Width(arrow))/2 - 1
		btnLine := strings.Repeat(" ", padding) + arrow
		bodyParts = append(bodyParts, "", tuiDimStyle.Render(btnLine))
	}

	body := strings.Join(bodyParts, "\n")

	// Input area
	h := min(6, max(3, m.textarea.LineCount()+1))
	m.textarea.SetHeight(h)
	input := m.textarea.View()

	// Hint bar
	var hintParts []string
	if m.waiting {
		hintParts = append(hintParts, " type to queue · enter queue · ctrl+c cancel · esc collapse ")
	} else {
		hintParts = append(hintParts, " enter send · alt+enter newline · ctrl+c quit ")
	}
	if len(m.toolRows) > 0 {
		hintParts = append(hintParts, "· tab select · space expand ")
	}
	if m.showThinking {
		hintParts = append(hintParts, "· thinking:on ")
	}
	if len(m.pendingQueue) > 0 {
		hintParts = append(hintParts, fmt.Sprintf("· %d queued (empty enter=force) ", len(m.pendingQueue)))
	}
	hint := tuiDimStyle.Render(strings.Join(hintParts, ""))

	return lipgloss.JoinVertical(lipgloss.Left, chrome, body, input, hint)
}

func (m *tuiModel) appendMsg(s string) {
	m.messages = append(m.messages, s)
	const maxLines = 2000
	if len(m.messages) > maxLines {
		m.messages = m.messages[len(m.messages)-maxLines:]
	}
}

func (m *tuiModel) appendInfo(s string) {
	m.appendMsg(tuiInfoStyle.Render("  " + s))
}

func (m *tuiModel) renderVP() {
	content := m.buildViewportContent()
	wasAtBottom := m.viewport.AtBottom()
	savedOffset := m.viewport.YOffset
	m.viewport.SetContent(content)
	if wasAtBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.YOffset = min(savedOffset, m.viewport.TotalLineCount()-m.viewport.Height)
		if m.viewport.YOffset < 0 {
			m.viewport.YOffset = 0
		}
	}
	// Check if scrolled to top and there's more history to load.
	if !m.waiting && m.msgOffset > 0 && m.viewport.YOffset <= 0 && m.viewport.TotalLineCount() > m.viewport.Height {
		m.loadMoreMessages()
	}
}

func (m *tuiModel) renderStreamVP() {
	content := m.buildViewportContent()
	if m.streamBuf.Len() > 0 {
		if content != "" {
			content += "\n"
		}
		content += tuiDimStyle.Render("▌ ") + m.streamBuf.String()
	}
	wasAtBottom := m.viewport.AtBottom()
	savedOffset := m.viewport.YOffset
	m.viewport.SetContent(content)
	if wasAtBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.YOffset = min(savedOffset, m.viewport.TotalLineCount()-m.viewport.Height)
		if m.viewport.YOffset < 0 {
			m.viewport.YOffset = 0
		}
	}
}

// buildViewportContent joins all messages into a single string for the viewport.
func (m *tuiModel) buildViewportContent() string {
	if len(m.messages) == 0 {
		return ""
	}
	return strings.Join(m.messages, "\n")
}

// loadMoreMessages loads older messages from session history into the viewport.
// It prepends them to m.messages and adjusts the scroll offset so the user's
// current viewport position remains stable (showing the same content).
// Batch size: 50 messages at a time.
func (m *tuiModel) loadMoreMessages() {
	if m.msgOffset <= 0 || m.waiting {
		return
	}
	const batchSize = 50
	newOffset := m.msgOffset - batchSize
	if newOffset < 0 {
		newOffset = 0
	}
	var newLines []string
	maxIdx := len(m.session.Messages) - 1
	for i := m.msgOffset - 1; i >= newOffset && i <= maxIdx; i-- {
		if i < 0 {
			break
		}
		msg := m.session.Messages[i]
		if msg.Role == provider.RoleSystem {
			continue
		}
		// Prepend: we're iterating backwards, so build in reverse order.
		header := tuiHeaderStyle.Render(fmt.Sprintf("── %s ──", msg.Role))
		var content string
		if msg.Role == provider.RoleAssistant {
			content = RenderMarkdown(msg.Content, max(40, m.width-2))
			if m.width > 4 {
				content = wrapANSIv2(content, m.width-4)
			}
		} else {
			content = msg.Content
			if m.width > 4 {
				content = wrapANSIv2(content, m.width-4)
			}
		}
		// Prepend header and content.
		newLines = append([]string{header, content}, newLines...)
	}

	if len(newLines) == 0 {
		m.msgOffset = 0 // nothing left to load
		return
	}

	// Visual lines (not slot count): multi-line content shifts YOffset by more than 1.
	addedVisual := visualLineCount(newLines)
	oldYOffset := m.viewport.YOffset

	// Prepend to messages.
	m.messages = append(newLines, m.messages...)
	m.msgOffset = newOffset

	// Update viewport content and adjust scroll to keep position stable.
	wasAtBottom := m.viewport.AtBottom()
	content := m.buildViewportContent()
	m.viewport.SetContent(content)
	if wasAtBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.YOffset = addedVisual + oldYOffset
	}

	// Remove the "showing last N" notice if we've loaded everything.
	if m.msgOffset <= 0 && len(m.messages) > 0 && strings.Contains(m.messages[0], "showing last") {
		noticeVisual := visualLineCount(m.messages[:1])
		m.messages = m.messages[1:]
		m.viewport.SetContent(m.buildViewportContent())
		m.viewport.YOffset = max(0, m.viewport.YOffset-noticeVisual)
	}
}

// visualLineCount returns how many viewport lines the given content slots occupy.
// Each string may itself contain newlines after markdown/wrap.
func visualLineCount(lines []string) int {
	n := 0
	for _, line := range lines {
		n += strings.Count(line, "\n") + 1
	}
	return n
}

func (m *tuiModel) forceSendQueued() {
	if len(m.pendingQueue) == 0 {
		return
	}
	// Cancel current turn.
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.sendNextQueued()
}

// sendNextQueued pops and sends the next queued message, handling /commands locally.
func (m *tuiModel) sendNextQueued() {
	if len(m.pendingQueue) == 0 {
		return
	}
	next := m.pendingQueue[0]
	m.pendingQueue = m.pendingQueue[1:]

	// Handle slash commands locally before sending to AI.
	if strings.HasPrefix(next, "/") {
		if m.handleSlash(next) {
			m.renderVP()
			m.textarea.Reset()
			// Check if more queued messages after slash command.
			if len(m.pendingQueue) > 0 {
				m.sendNextQueued() // recurse to keep draining
			}
			return
		}
	}
	m.startAI(next)
}

func (m *tuiModel) startAI(userText string) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	oldBridge := m.bridge
	oldBridge.Close()
	m.bridge = newStreamBridge()
	bridge := m.bridge
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	m.waiting = true
	m.turnStart = time.Now()
	m.toolRows = nil
	m.streamBuf.Reset()
	m.thinkingBuf.Reset()
	m.thinkingLines = 0
	m.selectedTool = -1

	m.appendMsg("")
	m.appendMsg(tuiHeaderStyle.Render("── you ──"))
	m.appendMsg(tuiUserStyle.Render(userText))
	m.appendMsg(tuiHeaderStyle.Render(fmt.Sprintf("── %s ──", m.modelName)))
	m.layout()
	m.renderVP()
	m.textarea.Reset()

	go func() {
		_, err := m.session.SendUser(ctx, userText, bridge)
		if ctx.Err() != nil {
			err = context.Canceled
		}
		bridge.Finish(err)
	}()
}

func (m *tuiModel) handleSlash(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "/help", "/h", "/?":
		m.appendMsg(tuiHeaderStyle.Render("── help ──"))
		help := RenderMarkdown(slashHelpMD, max(40, m.width-2))
		m.appendMsg(help)
		return true
	case "/clear":
		m.messages = nil
		m.session.Clear()
		m.msgOffset = 0
		m.appendInfo("history cleared")
		return true
	case "/status":
		tokens := provider.MessagesTokens(m.session.Messages)
		m.appendInfo(fmt.Sprintf("provider=%s model=%s tools=%v turns=%d msgs=%d tokens=%d",
			m.session.Completer.Name(), m.session.Model, m.toolsOn && m.session.UseTools,
			m.session.UserTurns(), len(m.session.Messages), tokens))
		return true
	case "/model":
		if len(fields) >= 2 {
			m.session.Model = fields[1]
			m.modelName = shortenModel(fields[1])
			m.appendInfo("model set to " + fields[1])
		} else {
			m.appendInfo("current model: " + m.session.Model)
		}
		return true
	case "/budget":
		if len(fields) >= 2 {
			var n int
			fmt.Sscanf(fields[1], "%d", &n)
			if n <= 0 {
				n = chat.DefaultMaxContextTokens
			}
			m.session.MaxContextTokens = n
			m.appendInfo(fmt.Sprintf("budget set to %d", n))
		} else {
			m.appendInfo(fmt.Sprintf("budget: %d", m.session.MaxContextTokens))
		}
		return true
	case "/steps":
		if len(fields) >= 2 {
			var n int
			fmt.Sscanf(fields[1], "%d", &n)
			m.session.MaxSteps = n
			if n <= 0 {
				m.appendInfo("steps: unlimited")
			} else {
				m.appendInfo(fmt.Sprintf("steps: %d", n))
			}
		} else if m.session.MaxSteps <= 0 {
			m.appendInfo("steps: unlimited")
		} else {
			m.appendInfo(fmt.Sprintf("steps: %d", m.session.MaxSteps))
		}
		return true
	case "/save":
		if len(fields) >= 2 {
			if err := m.session.Save(fields[1]); err != nil {
				m.appendMsg(tuiErrorStyle.Render("save error: " + err.Error()))
			} else {
				m.appendInfo(fmt.Sprintf("session %q saved", fields[1]))
			}
		} else {
			m.appendInfo("usage: /save <name>")
		}
		return true
	case "/load":
		if len(fields) >= 2 {
			if err := m.session.Load(fields[1]); err != nil {
				m.appendMsg(tuiErrorStyle.Render("load error: " + err.Error()))
			} else {
				m.messages = nil
				m.appendInfo(fmt.Sprintf("session %q loaded", fields[1]))
				m.msgOffset = 0 // all messages loaded
				for _, msg := range m.session.Messages {
					if msg.Role == provider.RoleSystem {
						continue
					}
					m.appendMsg(tuiHeaderStyle.Render(fmt.Sprintf("── %s ──", msg.Role)))
					if msg.Role == provider.RoleAssistant {
						m.appendMsg(RenderMarkdown(msg.Content, max(40, m.width-2)))
					} else {
						m.appendMsg(msg.Content)
					}
				}
			}
		} else {
			m.appendInfo("usage: /load <name>")
		}
		return true
	case "/list":
		sessions, err := m.session.ListSessions()
		if err != nil {
			m.appendMsg(tuiErrorStyle.Render("list error: " + err.Error()))
		} else if len(sessions) == 0 {
			m.appendInfo("no saved sessions")
		} else {
			m.appendMsg(tuiHeaderStyle.Render("── saved sessions ──"))
			for _, si := range sessions {
				marker := ""
				if si.Name == chat.AutoSaveName {
					marker = " [auto]"
				}
				m.appendMsg(tuiDimStyle.Render(fmt.Sprintf("  %-20s %3d msgs%s", si.Name, si.MessageCount, marker)))
			}
		}
		return true
	case "/delete":
		if len(fields) >= 2 {
			if err := m.session.DeleteSession(fields[1]); err != nil {
				m.appendMsg(tuiErrorStyle.Render("delete error: " + err.Error()))
			} else {
				m.appendInfo(fmt.Sprintf("session %q deleted", fields[1]))
			}
		} else {
			m.appendInfo("usage: /delete <name>")
		}
		return true
	case "/session":
		m.appendInfo(fmt.Sprintf("messages: %d, turns: %d", len(m.session.Messages), m.session.UserTurns()))
		return true
	case "/tools":
		if m.session.Tools == nil {
			m.appendInfo("tools disabled (--no-tools)")
			return true
		}
		m.appendMsg(tuiHeaderStyle.Render("── tools ──"))
		for _, t := range m.session.Tools.List() {
			m.appendMsg(tuiDimStyle.Render(fmt.Sprintf("  %s %s — %s", toolIconForName(t.Name()), t.Name(), t.Description())))
		}
		return true
	case "/plain":
		m.appendInfo("restart with: mivia chat --plain")
		return true
	default:
		return false
	}
}

// viewWelcome renders the launch screen: animated mark, wordmark, session picker.
func (m *tuiModel) viewWelcome() string {
	w := m.width
	if w < 20 {
		w = 20
	}
	h := m.height
	if h < 10 {
		h = 10
	}

	// Status
	left := tuiAccentStyle.Render(" mivia ") + tuiDimStyle.Render(m.modelName)
	right := tuiDimStyle.Render(" welcome ")
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	spacerN := w - lw - rw
	if spacerN < 1 {
		spacerN = 1
	}
	status := left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN)) + right

	// Logo (compact on short terminals) — welcome phase color (white identity).
	var logo string
	if h < 22 {
		logo = compactLogoFrameColor(m.logoFrame, w, brandColorWelcome)
	} else {
		logo = renderLogoFrameColor(m.logoFrame, w, brandColorWelcome)
	}
	word := renderWordmark(w)
	tag := tuiDimStyle.Render("type a message to start · select a session to resume")
	tag = lipgloss.PlaceHorizontal(w, lipgloss.Center, tag)

	// Input height
	inputH := min(6, max(3, m.textarea.LineCount()+1))
	m.textarea.SetWidth(max(20, w-4))
	m.textarea.SetHeight(inputH)
	input := m.textarea.View()
	hint := tuiDimStyle.Render(" ↑↓ sessions · enter open · type+enter new · ctrl+c quit ")

	// Vertical budget for session list.
	logoLines := strings.Count(logo, "\n") + 1
	// status + logo + word + tag + blanks + input + hint ≈ fixed
	fixed := 1 + logoLines + 1 + 1 + 2 + inputH + 1 + 4
	maxRows := h - fixed
	if maxRows < 3 {
		maxRows = 3
	}
	if maxRows > 12 {
		maxRows = 12
	}

	// Absolute Y of picker: after status, blank, logo, blank, word, blank, tag, blank
	yBase := 1 + 1 + logoLines + 1 + 1 + 1 + 1 + 1
	picker, hits, sc := renderSessionPicker(m.sessions, m.sessionSel, m.sessionScroll, w, maxRows, yBase)
	m.sessionHits = hits
	m.sessionScroll = sc

	// Center body content vertically a bit when there is spare height.
	body := strings.Join([]string{
		"",
		logo,
		"",
		word,
		"",
		tag,
		"",
		picker,
	}, "\n")

	return lipgloss.JoinVertical(lipgloss.Left, status, body, "", input, hint)
}

// runTUI starts the Bubble Tea TUI program.
// Does not auto-load the last session — welcome screen lets the user choose.
func runTUI(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	defer func() {
		if err := sess.SaveLast(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-save: %v\n", err)
		}
	}()

	model := newTUIModel(sess, res, toolsOn)

	if toolsOn {
		sess.OnAgentEvent = func(e agent.Event) {
			switch e.Kind {
			case agent.EventToolStart:
				model.bridge.PushTool(true, e.Name, e.Detail)
			case agent.EventToolEnd:
				model.bridge.PushTool(false, e.Name, e.Detail)
			case agent.EventToolParallel:
				model.bridge.PushTool(true, "parallel", e.Detail)
			case agent.EventPrune:
				model.bridge.PushTool(false, "prune", e.Detail)
			case agent.EventAssistant:
				// Model reasoning text — store for optional display.
				if e.Content != "" {
					model.bridge.PushThinking(e.Content)
				}
			case agent.EventStep:
				// ignore noisy step spam
			}
		}
	}

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	model.bridge.Close()
	return err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateStr(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// visibleWidth returns the visible (display) width of a string, ignoring
// ANSI escape sequences (which are zero-width). Multi-byte CJK chars count
// as 2, everything else as 1.
func visibleWidth(s string) int {
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == '\033' {
			// Skip ANSI escape sequence — zero-width.
			i++
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
			if i < len(s) {
				i++ // skip terminator
			}
			continue
		}
		// Count visual width — CJK and wide chars = 2, ASCII = 1.
		r, size := utf8.DecodeRuneInString(s[i:])
		if isWideRune(r) {
			w += 2
		} else {
			w++
		}
		i += size
	}
	return w
}

// stripAnsiOut removes ANSI escape sequences from a string.
func stripAnsiOut(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			i++
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// wrapANSIv2 wraps a string containing ANSI escape sequences to a maximum
// visible width. ANSI sequences are zero-width and preserved in the output.
// It breaks lines at word boundaries (spaces). If no space is found within
// maxWidth, the line is output as-is (no hard break of words).
func wrapANSIv2(s string, maxWidth int) string {
	if maxWidth < 5 {
		maxWidth = 5
	}
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(wrapLineV2(line, maxWidth))
	}
	return out.String()
}

// isRenderedTableRow reports whether a line is a markdown-rendered table row
// (spaces + box-drawing │ borders). Those must not soft-wrap mid-row.
func isRenderedTableRow(line string) bool {
	plain := stripAnsiOut(line)
	if !strings.Contains(plain, "│") {
		return false
	}
	trimmed := strings.TrimLeft(plain, " \t")
	return strings.HasPrefix(trimmed, "│")
}

// hardTruncateANSI truncates a line to maxWidth visible columns, appends … if
// cut, and always ends with ansiReset so colors do not bleed.
func hardTruncateANSI(line string, maxWidth int) string {
	if maxWidth < 1 {
		return ansiReset
	}
	if visibleWidth(line) <= maxWidth {
		if strings.HasSuffix(line, ansiReset) {
			return line
		}
		return line + ansiReset
	}
	budget := maxWidth
	if budget > 1 {
		budget-- // reserve for …
	}
	var b strings.Builder
	w := 0
	i := 0
	for i < len(line) {
		if line[i] == '\033' {
			start := i
			i++
			for i < len(line) && !((line[i] >= 'A' && line[i] <= 'Z') || (line[i] >= 'a' && line[i] <= 'z')) {
				i++
			}
			if i < len(line) {
				i++
			}
			b.WriteString(line[start:i])
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		rw := 1
		if isWideRune(r) {
			rw = 2
		}
		if w+rw > budget {
			break
		}
		b.WriteString(line[i : i+size])
		w += rw
		i += size
	}
	if maxWidth > 1 {
		b.WriteString("…")
	}
	b.WriteString(ansiReset)
	return b.String()
}

// wrapLineV2 wraps a single line (no embedded newlines) to maxWidth visible
// columns. ANSI sequences are zero-width. CJK chars are properly counted.
// Rendered table rows (│ borders) are never soft-wrapped; they hard-truncate.
// Returns the wrapped line.
func wrapLineV2(line string, maxWidth int) string {
	if len(line) == 0 {
		return ""
	}
	// Quick check: if visible width is within limit, return as-is.
	if visibleWidth(line) <= maxWidth {
		return line
	}

	// Table rows: keep one physical line — hard truncate with … if needed.
	if isRenderedTableRow(line) {
		return hardTruncateANSI(line, maxWidth)
	}

	// We need to wrap. Walk byte by byte, using visibleWidth for width.
	var out strings.Builder
	var currentLine strings.Builder
	lastSpaceByte := -1 // byte position of last space in currentLine

	flushLine := func() {
		prefix := currentLine.String()[:lastSpaceByte]
		out.WriteString(prefix)
		out.WriteByte('\n')
		remainder := currentLine.String()[lastSpaceByte+1:] // skip the space
		currentLine.Reset()
		currentLine.WriteString(remainder)
		lastSpaceByte = -1
	}

	i := 0
	for i < len(line) {
		// ANSI escape sequence: copy verbatim, zero-width.
		if line[i] == '\033' {
			start := i
			i++
			for i < len(line) && !((line[i] >= 'A' && line[i] <= 'Z') || (line[i] >= 'a' && line[i] <= 'z')) {
				i++
			}
			if i < len(line) {
				i++
			}
			currentLine.WriteString(line[start:i])
			continue
		}

		// Regular character byte.
		currentLine.WriteByte(line[i])

		// Track space positions for word wrap.
		if line[i] == ' ' || line[i] == '\t' {
			lastSpaceByte = currentLine.Len() - 1
		}

		// Check if we've exceeded maxWidth by measuring the full currentLine
		// (which accumulates characters and ANSI codes).
		if visibleWidth(currentLine.String()) > maxWidth && lastSpaceByte >= 0 {
			flushLine()
			i++
			continue
		}

		i++
	}

	// Write remaining content.
	out.WriteString(currentLine.String())
	return out.String()
}

// wrapANSI is the public wrapper. It uses wrapANSIv2 internally.
func wrapANSI(s string, maxWidth int) string {
	return wrapANSIv2(s, maxWidth)
}

// Markdown help content for /help in TUI.
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
- **Tab** / **Shift+Tab** — select tool
- **Space** — toggle expand on selected tool
- **e** — expand all tools · **E** — collapse all
- **Ctrl+T** — toggle thinking panel
- **Esc** — deselect tool, collapse all
- **G** — scroll to bottom (when viewing history)

### Queueing
While agent is busy, type + **Enter** queues a message.
**Enter** on empty input force-sends queued message (cancels current turn).
Queued messages auto-send when current turn finishes.

### Mouse
Scroll wheel moves through chat history.
A **↓** button appears at the bottom when scrolled up.
`
