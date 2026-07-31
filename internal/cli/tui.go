// Package cli — Bubble Tea TUI for mivia chat (agent mode).
package cli

import (
	"context"
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
	tuiHeaderStyle  = lipgloss.NewStyle().Faint(true)
	tuiUserStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	tuiUserCardBg   = lipgloss.NewStyle().Background(lipgloss.Color("236")) // dark gray user bubble
	tuiUserLabel    = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	tuiDimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	tuiInfoStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	tuiWaitingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("243")) // visible mid-gray for waiting state composer
	tuiBarStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Background(lipgloss.Color("236"))
	tuiAccentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	// Thinking chrome uses the brand's thinking phase colour (cyan) so the
	// same state reads the same everywhere — it used to be magenta here and
	// cyan in the status bar for the identical moment.
	tuiThinkingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorThinking)).Italic(true)
	thinkingLiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorThinking)).Italic(true)
	thinkingDimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Italic(true).Faint(true)
)

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------
type tuiTickMsg struct{ bridge *streamBridge }

// ---------------------------------------------------------------------------
// tuiModel
// ---------------------------------------------------------------------------
type tuiModel struct {
	session      *chat.Session
	config       *config.Resolved
	toolsOn      bool
	modelName    string
	workspaceDir string // cwd with ~ for home; shown on the welcome hero
	viewport     viewport.Model
	textarea     textarea.Model
	spinner      spinner.Model
	messages     []string
	blocks       []ChatBlock
	bridge       *streamBridge
	streamBuf    strings.Builder
	waiting      bool
	turnStart    time.Time
	toolRows     []toolRow
	thinkingBuf  strings.Builder // accumulated model reasoning text (shown on demand)
	cancel       context.CancelFunc
	mu           sync.Mutex
	workerWG     sync.WaitGroup
	// UI state
	toolPanel          toolPanelState // windowed tool strip (scroll/select/focus/hit)
	focus              tuiFocus
	liveThinkingScroll int      // scroll offset for live streaming thinking block
	pendingQueue       []string // messages queued while agent is busy
	msgOffset          int      // index into session.Messages for oldest loaded message
	// subagents aggregates attributed subagent activity for the current turn
	// (data spine for the fleet box / per-agent ledger).
	subagents *subagentTracker
	// overlay is the full-screen block detail pager (nil = closed).
	overlay *blockOverlay
	// sessionsDlg is the /sessions manager (nil = closed).
	sessionsDlg *sessionsDialog
	// modelDlg is the provider-qualified /model picker (nil = closed).
	modelDlg *modelDialog
	// trimmedBlocks counts history blocks dropped by the transcript cap, so
	// the top of the view can say what it is no longer showing.
	trimmedBlocks int
	// Welcome screen (no auto-load on launch).
	mode             screenMode
	logoFrame        int
	mouseEnabled     bool
	sessions         []chat.SessionInfo
	sessionSel       int
	sessionScroll    int
	sessionHits      []sessionRowHit
	lastClickIdx     int
	lastClickAt      time.Time
	lastClickBlockID string // transcript double-click activate (work groups / bubbles)
	hitMap           tuiHitMap
	chatBlockRanges  map[string][2]int
	selectedBlockID  string
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
	// workGroupScroll is the first visible member of each expanded group's
	// bounded window (key = work:<id>), scrolled with j/k while selected.
	workGroupScroll map[string]int
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
	// cancelling tracks that a cancel has been requested but the agent
	// goroutine may still be unwinding (context cancelled, tools aborting).
	// Set on first Ctrl+C during a turn; cleared on next startAI or when
	// the goroutine fully finishes. Prevents the second Ctrl+C from
	// sending tea.Quit while the goroutine is still running.
	cancelling bool
	// quitRequested is set on the second Ctrl+C while cancelling is true.
	// When the agent goroutine finally finishes (bridge signals Done), the
	// poll loop sends tea.Quit so SaveLast runs before the process exits.
	quitRequested bool
	// agentDone is true once the current turn's agent worker has signaled
	// Finish (bridge Done drained). Stage-1 cancel also Finishes the bridge
	// and drains Done once; without this flag, stage-2 quitRequested waited
	// forever for a Done that would never reappear if it was already drained
	// before quitRequested was set.
	agentDone bool
	// toolWaveTotal / toolWaveDone track the current multi-tool wave for live
	// k/n status. Completed tools leave toolRows immediately, so counts cannot
	// be derived from open rows alone.
	toolWaveTotal int
	toolWaveDone  int
	// prevAutoSaveWarn is set from the auto-save status file on startup.
	// If non-empty, the welcome screen displays a warning that the previous
	// session's conversation history was not persisted.
	prevAutoSaveWarn string
	// runDashboard tracks active orchestration runs (via SubscribeLifecycle).
	runDash *runDashboard
	// pendingResume holds run ID awaiting confirmation for resume.
	// Set by /resume <run-id>; cleared by 'y' (executes) or 'n' (cancels).
	pendingResume string
	// notice / noticeAt is the transient acknowledgement line (copy, paste
	// failure, armed quit). It is deliberately NOT stepDetail: that field is
	// the live tool heartbeat, and sharing it meant a copy mid-turn replaced
	// the only progress indicator with a string that never expired.
	notice   string
	noticeAt time.Time
	// quitArmedAt is when an idle ctrl+c armed the quit. Zero means not
	// armed; the arm expires (quitArmWindow) and any other input clears it.
	quitArmedAt time.Time
	// pendingSelectCmd holds the mouse-capture command /select produced. The
	// toggle itself happens immediately in the handler; only the terminal
	// sequence needs a tea.Cmd, and every caller of handleSlash drains it.
	pendingSelectCmd tea.Cmd
	// queuedSlashCmds collects terminal commands from slash commands that ran
	// from the queue, where the caller has no tea.Cmd return of its own.
	queuedSlashCmds []tea.Cmd
	width           int
	height          int
	ready           bool
}

func newTUIModel(sess *chat.Session, res *config.Resolved, toolsOn bool) *tuiModel {
	ti := newComposerTextarea()
	ti.Placeholder = "Message mivia…  Enter send · Alt+Enter newline · /help"
	ti.Focus()
	ti.SetWidth(80)
	ti.SetHeight(1)
	s := spinner.New()
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	s.Spinner = spinner.Dot
	m := &tuiModel{
		session:               sess,
		config:                res,
		toolsOn:               toolsOn,
		modelName:             shortenModel(sess.CurrentModel()),
		workspaceDir:          shortenWorkspacePath(),
		viewport:              newTranscriptViewport(80, 20),
		textarea:              ti,
		spinner:               s,
		bridge:                newStreamBridge(),
		messages:              []string{},
		blocks:                []ChatBlock{},
		toolPanel:             toolPanelState{Selected: -1},
		subagents:             newSubagentTracker(),
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
		// Auto-enable mouse when the host terminal looks capable (TTY + TERM).
		// ctrl+e (select mode) toggles at runtime. Do not EnableMouse in Init —
		// use tea.WithMouseCellMotion on the Program (bubbletea requirement).
		mouseEnabled:  mouseAvailable(),
		runDash:       newRunDashboard(),
		pendingResume: "",
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
	cmds := []tea.Cmd{m.spinner.Tick, tea.EnterAltScreen, logoTickCmd(), m.pollCmd()}
	// The adapter's poll chain is self-perpetuating from uiEventMsg/uiTickMsg,
	// so nothing re-issues the FIRST PollCmd. Without this the bus side
	// channel is dead for the whole session and every consumer fed by it
	// (subagent tracker → fleet box) silently never appears.
	if m.uiAdapter != nil {
		cmds = append(cmds, m.uiAdapter.PollCmd())
	}
	return tea.Batch(cmds...)
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
		// Conversation is never hidden: user and assistant messages are the
		// point of the transcript, and a collapsed message reads as lost
		// work. Only machinery (tools, thinking, status, work groups) folds.
		switch m.blocks[i].Kind {
		case ChatBlockDivider, ChatBlockUser, ChatBlockAssistant:
			return true
		}
		m.blocks[i].Collapsed = !m.blocks[i].Collapsed
		// Expanding a live multi-tool status also opens the tool strip so the
		// user can enter/space into per-tool I/O (status alone used to no-op).
		if isWorkStatusBlock(m.blocks[i]) && !m.blocks[i].Collapsed {
			m.focusLiveToolPanelFromStatus()
		}
		m.renderVP()
		return true
	}
	m.selectedBlockID = ""
	return false
}

// focusLiveToolPanelFromStatus selects and expands a live tool row when the
// wave is still in the tool strip (not yet committed to history).
func (m *tuiModel) focusLiveToolPanelFromStatus() {
	if len(m.toolRows) == 0 {
		return
	}
	m.toolPanel.Focused = true
	if len(m.toolPanel.ordered) == 0 {
		m.toolPanel.ordered = orderToolIndices(m.toolRows)
	}
	if m.toolPanel.Selected < 0 || m.toolPanel.Selected >= len(m.toolRows) {
		if len(m.toolPanel.ordered) > 0 {
			m.toolPanel.Selected = m.toolPanel.ordered[0]
		} else {
			m.toolPanel.Selected = 0
		}
	}
	sel := m.toolPanel.Selected
	if sel >= 0 && sel < len(m.toolRows) {
		m.toolRows[sel].Expanded = true
	}
	m.toolPanel.Scroll = clampToolScroll(
		m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
	)
	m.layout()
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
