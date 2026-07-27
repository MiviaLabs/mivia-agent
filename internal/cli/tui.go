// Package cli — Bubble Tea TUI for mivia chat (agent mode).
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
	"github.com/MiviaLabs/mivia-agent/internal/events"
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
	// awaitingFirstActivity: after send, before first interim/tool/stream/status.
	awaitingFirstActivity bool
	// followOutput: auto-scroll transcript to bottom when user is following.
	// Cleared when the user scrolls up; restored on jump-to-latest / at bottom.
	followOutput bool
	// workGroupCollapsed is view-only collapse state for history work groups
	// (key = work:<id>). Absent keys use auto policy (collapse when tools ≥ 4).
	workGroupCollapsed map[string]bool
	// EventBus for extensible event delivery.
	eventBus  *events.Bus
	uiAdapter *UIAdapter
	// activeTurnID fences bus TurnEnd events against force-send races.
	// Incremented on each startAI; TurnEnd must match to finish the stream.
	activeTurnID string
	turnSeq      uint64
	// thinkingExpandDefault is the global default visibility for thinking blocks.
	// When true, thinking blocks show expanded content by default.
	// Individual blocks can still be overridden via the Collapsed field.
	thinkingExpandDefault bool
	// prevAutoSaveWarn is set from the auto-save status file on startup.
	// If non-empty, the welcome screen displays a warning that the previous
	// session's conversation history was not persisted.
	prevAutoSaveWarn string
	width            int
	height           int
	ready            bool
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
		session:               sess,
		config:                res,
		toolsOn:               toolsOn,
		modelName:             shortenModel(sess.Model),
		viewport:              viewport.New(80, 20),
		textarea:              ti,
		spinner:               s,
		bridge:                newStreamBridge(),
		messages:              []string{},
		blocks:                []ChatBlock{},
		toolPanel:             toolPanelState{Selected: -1},
		focus:                 focusComposer,
		liveThinkingScroll:    0,
		pendingQueue:          []string{},
		msgOffset:             0,
		mode:                  modeWelcome,
		lastClickIdx:          -1,
		sessionSel:            0,
		sessionScroll:         0,
		hitMap:                tuiHitMap{version: 1},
		thinkingExpandDefault: true, // chat-like: show thinking body when committed
		followOutput:          true,
		workGroupCollapsed:    map[string]bool{},
	}
	m.setFocus(focusComposer)
	m.refreshSessionList()
	m.prevAutoSaveWarn = readAutosaveStatus(m.session.SessionDir)
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
	return tea.Batch(m.spinner.Tick, tea.EnterAltScreen, logoTickCmd(), m.pollCmd())
}
func (m *tuiModel) pollCmd() tea.Cmd {
	// Bridge is the TUI content source of truth (FinalWriter + OnEvent tools).
	// EventBus remains for extensibility / future Program.Send wiring, but must
	// not replace bridge drain — that half-migration dropped all live content.
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
	// Work-group header selection (view-layer only).
	if strings.HasPrefix(m.selectedBlockID, "work:") {
		if m.workGroupCollapsed == nil {
			m.workGroupCollapsed = map[string]bool{}
		}
		// Default when unset: auto-collapsed if group has ≥4 tools.
		cur := false
		if v, ok := m.workGroupCollapsed[m.selectedBlockID]; ok {
			cur = v
		} else {
			for _, g := range findWorkGroups(m.blocks) {
				if g.Key == m.selectedBlockID {
					cur = workGroupCollapsedDefault(g, nil)
					break
				}
			}
		}
		m.workGroupCollapsed[m.selectedBlockID] = !cur
		m.renderVP()
		return true
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
		hydrated := HydrateChatBlocksForView([]provider.Message{msg})
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
	addedVisual := visualLineCount(RenderChatBlocksWithWorkGroups(newBlocks, m.modelName, max(20, m.width-2), m.thinkingExpandDefault, m.workGroupCollapsed).Lines)
	oldYOffset := m.viewport.YOffset
	// Prepend to messages.
	m.blocks = append(newBlocks, m.blocks...)
	m.messages = m.renderBlocksForView().Lines
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
		m.messages = m.renderBlocksForView().Lines
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
	m.stepDetail = ""
	m.stepDetailAt = time.Time{}
	m.stalledWarning = false
	m.liveThinkingScroll = 0
	m.awaitingFirstActivity = true
	m.followOutput = true
	// Fence bus lifecycle events to this generation so a cancelled turn's
	// TurnEnd cannot finish a newer force-sent turn.
	m.turnSeq++
	turnID := fmt.Sprintf("%d", m.turnSeq)
	m.activeTurnID = turnID
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
		SentAt: time.Now(),
	})
	m.layout()
	m.renderVP()
	m.textarea.Reset()
	m.workerWG.Add(1)
	// Nested multi_step heartbeats/tools → same bridge as parent tools.
	// When UIAdapter is primary, bus also receives subagent events via globalBus;
	// bridge remains for legacy drain fallback.
	SetSubagentProgress(agentEventBridgeCallback(bridge))
	if m.eventBus != nil {
		m.eventBus.Publish(events.Event{
			Kind:      events.KindTurnStart,
			Timestamp: time.Now(),
			TurnID:    turnID,
			Detail:    userText,
		})
	}
	go func() {
		defer m.workerWG.Done()
		defer SetSubagentProgress(nil)
		// Bridge is FinalWriter + OnEvent sink. TUI drains it via tuiTickMsg.
		// EventBus still receives agent emits (session.EventBus) for non-UI
		// subscribers; UI does not exclusive-consume the bus for content.
		_, err := m.session.SendUserWithEvent(ctx, userText, bridge, agentEventBridgeCallback(bridge))
		if ctx.Err() != nil {
			err = context.Canceled
		}
		bridge.Finish(err)
		m.publishTurnEnd(turnID, err)
	}()
}

func runTUI(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	defer func() {
		err := sess.SaveLast()
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ auto-save failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "✓ session auto-saved\n")
		}
		writeAutosaveStatus(sess.SessionDir, err)
	}()
	model := newTUIModel(sess, res, toolsOn)
	// EventBus: agent loop dual-publishes for extensibility (hooks, future
	// Program.Send). TUI live content is bridge drain (FinalWriter + OnEvent).
	bus := events.New()
	model.eventBus = bus
	sess.EventBus = bus
	model.uiAdapter = NewUIAdapter(bus, model.bridge)
	SetGlobalBus(bus)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	model.mu.Lock()
	if model.cancel != nil {
		model.cancel()
	}
	model.mu.Unlock()
	model.workerWG.Wait()
	model.bridge.Close()
	bus.Close()
	return err
}
