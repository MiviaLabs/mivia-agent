// Package cli — Bubble Tea TUI for mivia chat (agent mode).
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

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

	return &tuiModel{
		session:      sess,
		config:       res,
		toolsOn:      toolsOn,
		modelName:    shortenModel(sess.Model),
		viewport:     viewport.New(80, 20),
		textarea:     ti,
		spinner:      s,
		bridge:       newStreamBridge(),
		messages:     []string{},
		selectedTool: -1,
		showThinking: false,
		pendingQueue: []string{},
	}
}

func (m *tuiModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tea.EnterAltScreen)
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

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		m.renderVP()

	case tea.KeyMsg:
		// Always allow Ctrl+C — cancels in-flight or quits when idle.
		if msg.String() == "ctrl+c" {
			if m.waiting {
				m.mu.Lock()
				if m.cancel != nil {
					m.cancel()
				}
				m.mu.Unlock()
			} else {
				return m, tea.Quit
			}
			break
		}

		switch msg.String() {
		case "ctrl+d":
			return m, tea.Quit
		case "enter":
			if msg.Alt {
				m.textarea.InsertString("\n")
				break
			}
			userText := strings.TrimSpace(m.textarea.Value())
			if userText == "" {
				// Empty Enter while waiting — if queue has items, force-send.
				if m.waiting && len(m.pendingQueue) > 0 {
					m.forceSendQueued()
					return m, tea.Batch(m.pollCmd(), m.spinner.Tick)
				}
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
					break
				}
				userText = "search the web for: " + query
			}

			// Handle slash commands (only when not waiting — no AI needed).
			if !m.waiting && strings.HasPrefix(userText, "/") {
				if m.handleSlash(userText) {
					m.renderVP()
					m.textarea.Reset()
					break
				}
			}

			if m.waiting {
				// Queue message for later.
				m.pendingQueue = append(m.pendingQueue, userText)
				m.textarea.Reset()
				m.appendInfo(fmt.Sprintf("(queued: %s — %d pending, Send empty to force)", truncateStr(userText, 40), len(m.pendingQueue)))
				m.renderVP()
			} else {
				// Send immediately.
				m.startAI(userText)
				return m, tea.Batch(m.pollCmd(), m.spinner.Tick)
			}

		case "ctrl+l":
			m.messages = nil
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

		// --- Toggle expand on selected tool ---
		case " ":
			if m.selectedTool >= 0 && m.selectedTool < len(m.toolRows) {
				m.toolRows[m.selectedTool].Expanded = !m.toolRows[m.selectedTool].Expanded
				m.layout()
			}

		// --- Expand all / collapse all ---
		case "e":
			for i := range m.toolRows {
				m.toolRows[i].Expanded = true
			}
			m.layout()
		case "E":
			for i := range m.toolRows {
				m.toolRows[i].Expanded = false
			}
			m.layout()
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
			return m, tea.Batch(append(cmds, m.pollCmd(), m.spinner.Tick)...)
		}
		return m, m.pollCmd()

	case spinner.TickMsg:
		if m.waiting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	if !m.waiting {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)
	return m, tea.Batch(cmds...)
}

func (m *tuiModel) layout() {
	headerHeight := 1
	toolPanel := 0
	thinkingPanel := 0

	if m.waiting && len(m.toolRows) > 0 {
		// Calculate tool panel height based on expanded rows.
		toolPanel = min(15, m.calcToolPanelLines())
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
	m.textarea.Reset()
	m.mu.Lock()
	m.cancel = nil
	m.mu.Unlock()

	// Auto-send next queued message if any.
	if len(m.pendingQueue) > 0 {
		m.sendNextQueued()
		if m.waiting {
			return []tea.Cmd{m.pollCmd(), m.spinner.Tick}
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

	// Status bar — fixed height, no layout needed.

	// Status bar
	left := tuiAccentStyle.Render(" mivia ") + tuiDimStyle.Render(m.modelName)
	var right string
	if m.waiting {
		elapsed := formatDuration(time.Since(m.turnStart))
		nOpen := 0
		for _, r := range m.toolRows {
			if !r.Done {
				nOpen++
			}
		}
		spin := m.spinner.View()
		if nOpen > 0 {
			right = fmt.Sprintf(" %s %s · %d active tools ", spin, elapsed, nOpen)
		} else {
			right = fmt.Sprintf(" %s thinking · %s ", spin, elapsed)
		}
		right = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render(right)
	} else {
		hint := "/help"
		if m.showThinking {
			hint = "thinking on"
		}
		right = tuiDimStyle.Render(fmt.Sprintf(" %d msgs · %s ", len(m.session.Messages), hint))
	}
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	spacerN := m.width - lw - rw
	if spacerN < 1 {
		spacerN = 1
	}
	status := left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN)) + right

	// Body: viewport + thinking panel + tool panel
	var bodyParts []string
	bodyParts = append(bodyParts, m.viewport.View())

	// Thinking panel (when toggled)
	if m.showThinking && m.thinkingBuf.Len() > 0 {
		thinkingContent := m.thinkingBuf.String()
		lines := strings.Split(thinkingContent, "\n")
		if len(lines) > 10 {
			lines = lines[len(lines)-10:]
			thinkingContent = strings.Join(lines, "\n")
		}
		panel := fmt.Sprintf("%s\n%s%s",
			tuiThinkingStyle.Render("  ── thinking ──"),
			tuiDimStyle.Render(thinkingContent),
			ansiReset,
		)
		bodyParts = append(bodyParts, "", panel)
	}

	// Tool panel.
	if m.waiting && len(m.toolRows) > 0 {
		panel, _ := renderToolPanel(m.toolRows, m.width, time.Now(), m.selectedTool)
		bodyParts = append(bodyParts, "", panel)
	}

	body := strings.Join(bodyParts, "\n")

	// Input area
	h := min(6, max(3, m.textarea.LineCount()+1))
	m.textarea.SetHeight(h)
	input := m.textarea.View()

	// Hint bar
	hintParts := []string{" enter send · alt+enter newline · ctrl+c quit "}
	if len(m.toolRows) > 0 {
		hintParts = append(hintParts, "· tab select · space expand · e/E all ")
	}
	if m.showThinking {
		hintParts = append(hintParts, "· thinking:on ")
	}
	if len(m.pendingQueue) > 0 {
		hintParts = append(hintParts, fmt.Sprintf("· %d queued (empty enter=force) ", len(m.pendingQueue)))
	}
	hint := tuiDimStyle.Render(strings.Join(hintParts, ""))

	return lipgloss.JoinVertical(lipgloss.Left, status, body, input, hint)
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
	m.viewport.SetContent(strings.Join(m.messages, "\n"))
	if m.viewport.AtBottom() {
		m.viewport.GotoBottom()
	}
}

func (m *tuiModel) renderStreamVP() {
	content := strings.Join(m.messages, "\n")
	if m.streamBuf.Len() > 0 {
		if content != "" {
			content += "\n"
		}
		content += tuiDimStyle.Render("▌ ") + m.streamBuf.String()
	}
	m.viewport.SetContent(content)
	// Only auto-scroll if user hasn't scrolled up.
	if m.viewport.AtBottom() {
		m.viewport.GotoBottom()
	}
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

// runTUI starts the Bubble Tea TUI program.
func runTUI(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	defer func() {
		if err := sess.SaveLast(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-save: %v\n", err)
		}
	}()

	if sess.HasAutoSave() {
		_ = sess.Load(chat.AutoSaveName)
	}

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

	if sess.UserTurns() > 0 {
		for _, msg := range sess.Messages {
			if msg.Role == provider.RoleSystem {
				continue
			}
			model.appendMsg(tuiHeaderStyle.Render(fmt.Sprintf("── %s ──", msg.Role)))
			if msg.Role == provider.RoleAssistant {
				model.appendMsg(RenderMarkdown(msg.Content, 78))
			} else {
				model.appendMsg(msg.Content)
			}
		}
		model.renderVP()
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
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

### Queueing
While agent is busy, type + **Enter** queues a message.
**Enter** on empty input force-sends queued message (cancels current turn).
Queued messages auto-send when current turn finishes.
`
