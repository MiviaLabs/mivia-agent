// Package cli implements mivia command handlers.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

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
	tuiHeaderStyle    = lipgloss.NewStyle().Faint(true)
	tuiUserStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	tuiAssistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	tuiDimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	tuiInfoStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	tuiBoldStyle      = lipgloss.NewStyle().Bold(true)
)

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type tuiStreamMsg string
type tuiDoneMsg struct{ err error }

// ---------------------------------------------------------------------------
// tuiWriter — io.Writer that sends writes through a channel
// ---------------------------------------------------------------------------

type tuiWriter struct {
	ch chan<- string
}

func (w *tuiWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		select {
		case w.ch <- string(p):
		default:
		}
	}
	return len(p), nil
}

// ---------------------------------------------------------------------------
// tuiModel — Bubble Tea model
// ---------------------------------------------------------------------------

type tuiModel struct {
	session   *chat.Session
	config    *config.Resolved
	toolsOn   bool
	modelName string

	// Components
	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model

	// Messages rendered as lines
	messages []string

	// Streaming state
	streamCh  chan string
	doneCh    chan error
	streamBuf strings.Builder
	waiting   bool
	cancel    context.CancelFunc
	mu        sync.Mutex

	// Terminal
	width  int
	height int
	ready  bool
}

func newTUIModel(sess *chat.Session, res *config.Resolved, toolsOn bool) *tuiModel {
	ti := textarea.New()
	ti.Placeholder = "Type a message... (/help for commands)"
	ti.Focus()
	ti.Prompt = "> "
	ti.CharLimit = 0
	ti.SetWidth(80)
	ti.SetHeight(3)
	ti.ShowLineNumbers = false
	ti.KeyMap.InsertNewline.SetEnabled(true)

	s := spinner.New()
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	s.Spinner = spinner.Dot

	vp := viewport.New(80, 20)

	return &tuiModel{
		session:   sess,
		config:    res,
		toolsOn:   toolsOn,
		modelName: shortenModel(sess.Model),
		viewport:  vp,
		textarea:  ti,
		spinner:   s,
		streamCh:  make(chan string, 512),
		doneCh:    make(chan error, 1),
		messages:  []string{},
	}
}

func (m *tuiModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		tea.EnterAltScreen,
	)
}

// streamCmd reads from the streaming channel and returns a message.
// It re-issues itself so streaming continues across updates.
func (m *tuiModel) streamCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case s, ok := <-m.streamCh:
			if !ok {
				// Channel closed - check for error.
				var err error
				select {
				case err = <-m.doneCh:
				default:
				}
				return tuiDoneMsg{err: err}
			}
			return tuiStreamMsg(s)
		case err := <-m.doneCh:
			return tuiDoneMsg{err: err}
		case <-time.After(50 * time.Millisecond):
			return nil
		}
	}
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		headerHeight := 1
		inputHeight := min(5, max(3, m.textarea.LineCount()+1))
		vpHeight := max(5, m.height-headerHeight-inputHeight-2)

		if !m.ready {
			m.viewport = viewport.New(m.width, vpHeight)
			m.textarea.SetWidth(m.width - 4)
			m.ready = true
		} else {
			m.viewport.Width = m.width
			m.viewport.Height = vpHeight
		}
		m.viewport.SetContent(strings.Join(m.messages, "\n"))
		m.viewport.GotoBottom()

	case tea.KeyMsg:
		if m.waiting {
			if msg.String() == "ctrl+c" {
				m.mu.Lock()
				if m.cancel != nil {
					m.cancel()
				}
				m.mu.Unlock()
			}
			break
		}

		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			return m, tea.Quit

		case "enter":
			if msg.Alt {
				m.textarea.InsertString("\n")
				break
			}
			userText := strings.TrimSpace(m.textarea.Value())
			if userText == "" {
				break
			}
			if userText == "exit" || userText == "quit" {
				return m, tea.Quit
			}

			// Transform /search into NL request.
			if strings.HasPrefix(userText, "/search") {
				query := strings.TrimSpace(userText[7:])
				if query == "" {
					m.appendMsg(tuiInfoStyle.Render("usage: /search <query>"))
					m.renderVP()
					m.textarea.Reset()
					break
				}
				userText = "search the web for: " + query
			}

			// Handle slash commands.
			if strings.HasPrefix(userText, "/") {
				if m.handleSlash(userText) {
					m.renderVP()
					m.textarea.Reset()
					break
				}
			}

			// Start AI request.
			m.startAI(userText)
			return m, m.streamCmd()

		case "ctrl+l":
			m.messages = nil
			m.viewport.SetContent("")
		}

	case tuiStreamMsg:
		chunk := string(msg)
		m.streamBuf.WriteString(chunk)
		content := strings.Join(m.messages, "\n")
		if m.streamBuf.Len() > 0 {
			content += "\n" + m.streamBuf.String()
		}
		m.viewport.SetContent(content)
		m.viewport.GotoBottom()
		return m, m.streamCmd()

	case tuiDoneMsg:
		m.waiting = false
		finalContent := m.streamBuf.String()
		m.streamBuf.Reset()

		if finalContent != "" {
			m.messages = append(m.messages, finalContent)
		}
		if msg.err != nil {
			m.messages = append(m.messages, tuiErrorStyle.Render("error: "+msg.err.Error()))
		}

		m.textarea.Reset()
		m.renderVP()
		return m, nil

	case spinner.TickMsg:
		if m.waiting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	// Update textarea when not waiting.
	if !m.waiting {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}

	// Always update viewport.
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m *tuiModel) View() string {
	if !m.ready {
		return "Starting mivia..."
	}

	// Status bar.
	left := fmt.Sprintf(" %s ", m.modelName)
	right := ""
	if m.waiting {
		right = fmt.Sprintf(" %s thinking... ", m.spinner.View())
	} else {
		right = fmt.Sprintf(" %d msgs ", len(m.session.Messages))
	}
	spacer := max(2, m.width-lipgloss.Width(left)-lipgloss.Width(right)-2)
	status := tuiHeaderStyle.Render(fmt.Sprintf(" %s%s%s ", left, strings.Repeat("─", spacer), right))

	// Viewport.
	vp := m.viewport.View()

	// Input.
	h := min(6, max(3, m.textarea.LineCount()+1))
	m.textarea.SetHeight(h)
	input := m.textarea.View()

	return lipgloss.JoinVertical(lipgloss.Left, status, vp, input)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (m *tuiModel) appendMsg(s string) {
	m.messages = append(m.messages, s)
}

func (m *tuiModel) renderVP() {
	m.viewport.SetContent(strings.Join(m.messages, "\n"))
	m.viewport.GotoBottom()
}

func (m *tuiModel) startAI(userText string) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	m.waiting = true
	m.streamBuf.Reset()

	// Display user + assistant headers.
	m.appendMsg(tuiHeaderStyle.Render("── you ──"))
	m.appendMsg(tuiUserStyle.Render(userText))
	m.appendMsg(tuiHeaderStyle.Render(fmt.Sprintf("── %s ──", m.modelName)))
	m.renderVP()
	m.textarea.Reset()

	// Channel writer.
	cw := &tuiWriter{ch: m.streamCh}
	ch := m.streamCh
	doneCh := m.doneCh

	// Run AI in goroutine.
	go func() {
		_, err := m.session.SendUser(ctx, userText, cw)
		doneCh <- err
		close(ch)
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
		for _, line := range strings.Split(strings.TrimSpace(slashHelp), "\n") {
			m.appendMsg(tuiDimStyle.Render(line))
		}
		return true
	case "/clear":
		m.messages = nil
		m.session.Clear()
		m.appendMsg(tuiInfoStyle.Render("(history cleared)"))
		return true
	case "/status":
		tokens := provider.MessagesTokens(m.session.Messages)
		s := fmt.Sprintf("provider=%s model=%s tools=%v turns=%d messages=%d tokens=%d",
			m.session.Completer.Name(), m.session.Model, m.toolsOn && m.session.UseTools,
			m.session.UserTurns(), len(m.session.Messages), tokens)
		m.appendMsg(tuiInfoStyle.Render(s))
		return true
	case "/model":
		if len(fields) >= 2 {
			m.session.Model = fields[1]
			m.modelName = shortenModel(fields[1])
			m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("(model set to %s)", fields[1])))
		} else {
			m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("current model: %s", m.session.Model)))
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
			m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("(budget set to %d)", n)))
		} else {
			m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("budget: %d", m.session.MaxContextTokens)))
		}
		return true
	case "/steps":
		if len(fields) >= 2 {
			var n int
			fmt.Sscanf(fields[1], "%d", &n)
			m.session.MaxSteps = n
			if n <= 0 {
				m.appendMsg(tuiInfoStyle.Render("(steps: unlimited)"))
			} else {
				m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("(steps: %d)", n)))
			}
		} else if m.session.MaxSteps <= 0 {
			m.appendMsg(tuiInfoStyle.Render("steps: unlimited"))
		} else {
			m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("steps: %d", m.session.MaxSteps)))
		}
		return true
	case "/save":
		if len(fields) >= 2 {
			if err := m.session.Save(fields[1]); err != nil {
				m.appendMsg(tuiErrorStyle.Render("save error: " + err.Error()))
			} else {
				m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("(session %q saved)", fields[1])))
			}
		} else {
			m.appendMsg(tuiInfoStyle.Render("usage: /save <name>"))
		}
		return true
	case "/load":
		if len(fields) >= 2 {
			if err := m.session.Load(fields[1]); err != nil {
				m.appendMsg(tuiErrorStyle.Render("load error: " + err.Error()))
			} else {
				m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("(session %q loaded)", fields[1])))
				for _, msg := range m.session.Messages {
					if msg.Role == provider.RoleSystem {
						continue
					}
					m.appendMsg(tuiHeaderStyle.Render(fmt.Sprintf("── %s ──", msg.Role)))
					m.appendMsg(msg.Content)
				}
			}
		} else {
			m.appendMsg(tuiInfoStyle.Render("usage: /load <name>"))
		}
		return true
	case "/list":
		sessions, err := m.session.ListSessions()
		if err != nil {
			m.appendMsg(tuiErrorStyle.Render("list error: " + err.Error()))
		} else if len(sessions) == 0 {
			m.appendMsg(tuiInfoStyle.Render("(no saved sessions)"))
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
				m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("(session %q deleted)", fields[1])))
			}
		} else {
			m.appendMsg(tuiInfoStyle.Render("usage: /delete <name>"))
		}
		return true
	case "/session":
		m.appendMsg(tuiInfoStyle.Render(fmt.Sprintf("messages: %d, turns: %d", len(m.session.Messages), m.session.UserTurns())))
		return true
	case "/tools":
		if m.session.Tools == nil {
			m.appendMsg(tuiInfoStyle.Render("tools disabled (--no-tools)"))
			return true
		}
		m.appendMsg(tuiHeaderStyle.Render("── tools ──"))
		for _, t := range m.session.Tools.List() {
			m.appendMsg(tuiDimStyle.Render(fmt.Sprintf("  %s — %s", t.Name(), t.Description())))
		}
		return true
	default:
		return false
	}
}

// runTUI starts the Bubble Tea TUI program, replacing the raw terminal REPL.
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

	if sess.UserTurns() > 0 {
		for _, msg := range sess.Messages {
			if msg.Role == provider.RoleSystem {
				continue
			}
			model.appendMsg(tuiHeaderStyle.Render(fmt.Sprintf("── %s ──", msg.Role)))
			model.appendMsg(msg.Content)
		}
		model.renderVP()
	}

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
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
