// Package cli — Bubble Tea TUI for mivia chat (agent mode).
package cli

import (
	"context"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------
var (
	tuiHeaderStyle   = lipgloss.NewStyle().Faint(true)
	tuiUserStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	tuiUserCardBg    = lipgloss.NewStyle().Background(lipgloss.Color("235"))
	tuiUserLabel     = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
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
type tuiTickMsg struct{ bridge *streamBridge }

// ---------------------------------------------------------------------------
// tuiModel
// ---------------------------------------------------------------------------
type tuiModel struct {
	session     *chat.Session
	config      *config.Resolved
	toolsOn     bool
	modelName   string
	viewport    viewport.Model
	textarea    textarea.Model
	spinner     spinner.Model
	messages    []string
	blocks      []ChatBlock
	bridge      *streamBridge
	streamBuf   strings.Builder
	waiting     bool
	turnStart   time.Time
	toolRows    []toolRow
	thinkingBuf strings.Builder // accumulated model reasoning text (shown on demand)
	cancel      context.CancelFunc
	mu          sync.Mutex
	workerWG    sync.WaitGroup
	// UI state
	toolPanel          toolPanelState // windowed tool strip (scroll/select/focus/hit)
	focus              tuiFocus
	liveThinkingScroll int      // scroll offset for live streaming thinking block
	pendingQueue       []string // messages queued while agent is busy
	msgOffset          int      // index into session.Messages for oldest loaded message
	// Welcome screen (no auto-load on launch).
	mode            screenMode
	logoFrame       int
	mouseEnabled    bool
	sessions        []chat.SessionInfo
	sessionSel      int
	sessionScroll   int
	sessionHits     []sessionRowHit
	lastClickIdx    int
	lastClickAt     time.Time
	hitMap          tuiHitMap
	chatBlockRanges map[string][2]int
	selectedBlockID string
	// Heartbeat tracking for long-running task visibility.
	stepDetail     string
	stepDetailAt   time.Time
	stalledWarning bool
	// thinkingExpandDefault is the global default visibility for thinking blocks.
	// When true, thinking blocks show expanded content by default.
	// Individual blocks can still be overridden via the Collapsed field.
	thinkingExpandDefault bool
	width                 int
	height                int
	ready                 bool
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
		session:            sess,
		config:             res,
		toolsOn:            toolsOn,
		modelName:          shortenModel(sess.Model),
		viewport:           viewport.New(80, 20),
		textarea:           ti,
		spinner:            s,
		bridge:             newStreamBridge(),
		messages:           []string{},
		blocks:             []ChatBlock{},
		toolPanel:          toolPanelState{Selected: -1},
		focus:              focusComposer,
		liveThinkingScroll: 0,
		pendingQueue:       []string{},
		msgOffset:          0,
		mode:               modeWelcome,
		lastClickIdx:       -1,
		sessionSel:         0,
		sessionScroll:      0,
		hitMap:             tuiHitMap{version: 1},
	}
	m.setFocus(focusComposer)
	m.refreshSessionList()
	ti.Placeholder = "Type to start a new chat…  or select a session ↑↓"
	return m
}
func (m *tuiModel) refreshSessionList() {
	list, err := m.session.ListSessions()
	if err != nil {
		m.sessions = nil
		m.sessionSel = 0
		m.sessionScroll = 0
		return
	}
	// ListSessions is newest-first; keep selection on index 0 (latest) by default
	// when the list is freshly loaded or the previous index is out of range.
	m.sessions = list
	if m.sessionSel < 0 || m.sessionSel >= len(m.sessions) {
		m.sessionSel = 0
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
			return tuiTickMsg{bridge: bridge}
		case <-time.After(80 * time.Millisecond):
			return tuiTickMsg{bridge: bridge}
		}
	}
}

// clearToolSelection clears selection and focus on the tool strip.
func (m *tuiModel) clearToolSelection() {
	m.toolPanel.Selected = -1
	m.toolPanel.Focused = false
}
func (m *tuiModel) toggleSelectedBlock() bool {
	if m.selectedBlockID == "" {
		return false
	}
	for i := range m.blocks {
		if m.blocks[i].ID != m.selectedBlockID {
			continue
		}
		if m.blocks[i].Rendered != "" && m.blocks[i].Kind != ChatBlockTool && m.blocks[i].Kind != ChatBlockThinking {
			return true
		}
		m.blocks[i].Collapsed = !m.blocks[i].Collapsed
		m.renderVP()
		return true
	}
	m.selectedBlockID = ""
	return false
}

// blockByID returns the ChatBlock with the given ID, or nil.
func (m *tuiModel) blockByID(id string) *ChatBlock {
	for i := range m.blocks {
		if m.blocks[i].ID == id {
			return &m.blocks[i]
		}
	}
	return nil
}

// adjustThinkingScroll adjusts the scroll offset of a thinking block identified by blockID.
// Returns true if the offset changed.
func (m *tuiModel) adjustThinkingScroll(blockID string, dir int) bool {
	if blockID == "thinking-live" {
		// Live streaming block.
		text := m.thinkingBuf.String()
		if text == "" {
			return false
		}
		n := len(strings.Split(text, "\n"))
		if n <= maxThinkingLines {
			return false
		}
		maxOffset := n - maxThinkingLines
		old := m.liveThinkingScroll
		m.liveThinkingScroll += dir
		if m.liveThinkingScroll < 0 {
			m.liveThinkingScroll = 0
		}
		if m.liveThinkingScroll > maxOffset {
			m.liveThinkingScroll = maxOffset
		}
		return m.liveThinkingScroll != old
	}
	// History block.
	block := m.blockByID(blockID)
	if block == nil || block.Kind != ChatBlockThinking {
		return false
	}
	n := len(strings.Split(block.Text, "\n"))
	if n <= maxThinkingLines {
		return false
	}
	maxOffset := n - maxThinkingLines
	old := block.ScrollOffset
	block.ScrollOffset += dir
	if block.ScrollOffset < 0 {
		block.ScrollOffset = 0
	}
	if block.ScrollOffset > maxOffset {
		block.ScrollOffset = maxOffset
	}
	return block.ScrollOffset != old
}
func (m *tuiModel) loadMoreMessages() {
	m.hitMap.invalidate()
	// Allow while waiting — user can still browse older history mid-turn.
	if m.msgOffset <= 0 {
		return
	}
	const batchSize = 50
	newOffset := m.msgOffset - batchSize
	if newOffset < 0 {
		newOffset = 0
	}
	msgs := m.session.MessagesCopy()
	var newBlocks []ChatBlock
	maxIdx := len(msgs) - 1
	for i := m.msgOffset - 1; i >= newOffset && i <= maxIdx; i-- {
		if i < 0 {
			break
		}
		msg := msgs[i]
		hydrated := HydrateChatBlocks([]provider.Message{msg})
		if len(hydrated) == 0 {
			continue
		}
		newBlocks = append(hydrated, newBlocks...)
	}
	if len(newBlocks) == 0 {
		m.msgOffset = 0 // nothing left to load
		return
	}
	// Visual lines (not slot count): multi-line content shifts YOffset by more than 1.
	addedVisual := visualLineCount(RenderChatBlocks(newBlocks, m.modelName, max(20, m.width-2), m.thinkingExpandDefault).Lines)
	oldYOffset := m.viewport.YOffset
	// Prepend to messages.
	m.blocks = append(newBlocks, m.blocks...)
	m.messages = RenderChatBlocks(m.blocks, m.modelName, max(20, m.width-2), m.thinkingExpandDefault).Lines
	m.msgOffset = newOffset
	// Always preserve visual position on prepend. Do NOT use AtBottom()/GotoBottom:
	// when content fits the viewport, AtBottom∧AtTop are both true and GotoBottom
	// would jump the user away from the top (history load looks broken).
	content := m.buildViewportContent()
	m.viewport.SetContent(content)
	maxOff := m.viewport.TotalLineCount() - m.viewport.Height
	if maxOff < 0 {
		maxOff = 0
	}
	newOff := addedVisual + oldYOffset
	if newOff > maxOff {
		newOff = maxOff
	}
	if newOff < 0 {
		newOff = 0
	}
	m.viewport.YOffset = newOff
	// Remove the "showing last N" notice if we've loaded everything.
	if m.msgOffset <= 0 && len(m.blocks) > 0 && strings.Contains(m.messages[0], "showing last") {
		noticeVisual := visualLineCount(m.messages[:1])
		m.blocks = m.blocks[1:]
		m.messages = RenderChatBlocks(m.blocks, m.modelName, max(20, m.width-2), m.thinkingExpandDefault).Lines
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
	for len(m.pendingQueue) > 0 {
		next := m.pendingQueue[0]
		m.pendingQueue = m.pendingQueue[1:]
		if strings.HasPrefix(next, "/") && m.handleSlash(next) {
			m.renderVP()
			m.textarea.Reset()
			continue
		}
		m.startAI(next)
		return
	}
}
func (m *tuiModel) startAI(userText string) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	oldBridge := m.bridge
	oldBridge.Close() // Close isolates prior events; a new bridge starts clean.
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
	m.toolPanel = toolPanelState{Selected: -1}
	// Insert turn separator if this is a subsequent turn in a live session.
	if len(m.blocks) > 0 {
		m.appendBlock(ChatBlock{
			TurnID: uint64(m.session.UserTurns() + 1),
			Kind:   ChatBlockDivider,
		})
	}
	m.appendBlock(ChatBlock{
		TurnID: uint64(m.session.UserTurns() + 1),
		Kind:   ChatBlockUser,
		Text:   userText,
	})
	m.layout()
	m.renderVP()
	m.textarea.Reset()
	m.workerWG.Add(1)
	// Nested multi_step heartbeats/tools → same bridge as parent tools.
	SetSubagentProgress(agentEventBridgeCallback(bridge))
	go func() {
		defer m.workerWG.Done()
		defer SetSubagentProgress(nil)
		_, err := m.session.SendUserWithEvent(ctx, userText, bridge, agentEventBridgeCallback(bridge))
		if ctx.Err() != nil {
			err = context.Canceled
		}
		bridge.Finish(err)
	}()
}
func runTUI(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	defer func() {
		if err := sess.SaveLast(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-save: %v\n", err)
		}
	}()
	model := newTUIModel(sess, res, toolsOn)
	// Note: Agent events are delivered via the bridge callback passed to
	// SendUserWithEvent in startAI, NOT via sess.OnAgentEvent. The bridge
	// callback (agentEventBridgeCallback) is more complete than any
	// OnAgentEvent assignment here would be, and the latter would be
	// silently overridden by SendUserWithEvent's override parameter.
	// See sendAgent in internal/chat/session.go for the override logic.
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	model.mu.Lock()
	if model.cancel != nil {
		model.cancel()
	}
	model.mu.Unlock()
	model.workerWG.Wait()
	model.bridge.Close()
	return err
}

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
- **Ctrl+M** — toggle mouse on/off
- **Ctrl+O** (welcome) — continue last session
- **Esc** — return to composer
### Queueing
While agent is busy, type + **Enter** queues a message.
**Enter** on empty input force-sends queued message (cancels current turn).
Queued messages auto-send when current turn finishes.
### Mouse
Scroll wheel moves through chat history.
Click a message block to select it; click composer to return.
`
