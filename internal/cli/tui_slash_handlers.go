package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// handleSlashImpl is the TUI slash dispatcher. The var form is a deliberate
// test seam (budget_integration_test and others override it). Command bodies
// live in per-concern sub-handlers; this switch only routes.
var handleSlashImpl = func(m *tuiModel, cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	if isLocalSlash(fields[0]) {
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiDimStyle.Render("  ⚙ " + strings.TrimSpace(cmd)), Rendered: tuiDimStyle.Render("  ⚙ " + strings.TrimSpace(cmd))})
	}
	switch strings.ToLower(fields[0]) {
	case "/help", "/h", "/?", "/status", "/tools", "/agents":
		return m.handleTuiInfoSlash(cmd, fields)
	case "/model":
		return m.handleTuiModelSlash(cmd, fields)
	case "/agent":
		return m.handleTuiAgentSlash(cmd, fields)
	case "/effort":
		return m.handleTuiEffortSlash(fields)
	case "/budget", "/steps":
		return m.handleTuiLimitsSlash(cmd, fields)
	case "/compact":
		if err := m.session.Compact(context.Background()); err != nil {
			m.appendInfo("context compaction failed: " + err.Error())
		} else {
			usage := m.session.ContextUsage()
			m.appendInfo(fmt.Sprintf("context compacted (%d%% used, %s/%s prompt)", usage.Percent, chat.FormatTokenK(usage.UsedTokens), chat.FormatTokenK(usage.BudgetTokens)))
		}
		return true
	case "/new", "/clear", "/sessions", "/worktrees":
		return m.handleTuiSessionLifecycleSlash(cmd, fields)
	case "/save", "/load", "/delete", "/list", "/session":
		return m.handleTuiSessionStoreSlash(cmd, fields)
	case "/title":
		return m.handleTuiTitleSlash(cmd)
	case "/select", "/plain":
		return m.handleTuiMiscSlash(cmd, fields)
	case "/resume":
		return m.handleTuiResumeSlash(cmd, fields)
	case "/workflows":
		return m.handleTuiWorkflowsSlash()
	case "/hooks":
		m.appendInfo(hooksSlashOutput(fields))
		return true
	default:
		return false
	}
}

func (m *tuiModel) handleTuiTitleSlash(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	title := strings.TrimSpace(strings.TrimPrefix(cmd, fieldsFirst(cmd)))
	sessionID := m.session.SessionID
	instance := contextstate.WorktreeInstance{}
	if m.activeSession != nil {
		if m.activeSession.SessionID == "" {
			m.appendInfo("title error: titles are not available for saved snapshots")
			return true
		}
		sessionID = m.activeSession.Reference()
		instance = m.activeSession.WorktreeInstance
	}
	var err error
	if m.activeSession != nil {
		err = m.session.SetContextSessionTitleInWorktree(sessionID, title, instance)
	} else {
		err = m.session.SetContextSessionTitle(sessionID, title)
	}
	if err != nil {
		m.appendInfo("title error: " + err.Error())
		return true
	}
	if err := m.refreshSessionList(); err != nil {
		m.appendInfo("sessions refresh failed: " + err.Error())
	} else {
		m.renderVP()
	}
	m.appendInfo("session title updated")
	return true
}

func fieldsFirst(cmd string) string { return strings.Fields(cmd)[0] }

// handleTuiInfoSlash handles /help, /status, and /tools (reference overlays).
func (m *tuiModel) handleTuiInfoSlash(cmd string, fields []string) bool {
	_ = cmd
	switch strings.ToLower(fields[0]) {
	case "/help", "/h", "/?":
		// Reference material, not conversation: a closable dialog instead of
		// a permanent wall of text in the transcript.
		m.setOverlay(newHelpDialogFor(m.session.CurrentBinding().SkillRegistry, m.width))
		return true
	case "/agents":
		m.appendInfo(formatAgentCurrent(currentAgentName(m.agentState), registryForState(m.agentState)))
		return true
	case "/status":
		m.setOverlay(m.newStatusDialog())
		return true
	case "/tools":
		// This runs on the Bubble Tea update goroutine with no waiting gate,
		// so it can read the surface while a turn boundary publishes a widened
		// one. Session.Tools is mu-guarded; snapshot it.
		registry, _, _ := m.session.AgentSurfaceSnapshot()
		if registry == nil {
			m.appendInfo("tools disabled (--no-tools)")
			return true
		}
		var names []string
		for _, t := range registry.List() {
			names = append(names, t.Name())
		}
		m.setOverlay(m.newToolsDialog(names))
		return true
	default:
		return false
	}
}

// handleTuiModelSlash handles /model (dialog open or direct switch).
func (m *tuiModel) handleTuiModelSlash(cmd string, fields []string) bool {
	_ = cmd
	defaultProvider := ""
	if m.config != nil {
		defaultProvider = m.config.ProviderName
	}
	providerName, modelName, hasArg := parseModelArgs(fields, m.session.CurrentSelection().ProviderName, defaultProvider)
	if !hasArg {
		m.openModelDialog()
		return true
	}
	choices := modelSwitchChoices(m.config, providerName, defaultProvider)
	discarded, err := m.switchModel(providerName, modelName)
	if err != nil {
		m.appendInfo(formatModelUnavailable(providerName, choices))
		return true
	}
	m.modelName = shortenModel(m.session.CurrentModel())
	m.appendInfo(formatModelSet(m.session.CurrentSelection().ProviderName, m.session.CurrentModel(), discarded))
	return true
}

// handleTuiAgentSlash handles /agent (dialog open or direct switch).
func (m *tuiModel) handleTuiAgentSlash(cmd string, fields []string) bool {
	_ = cmd
	if len(fields) < 2 {
		m.openAgentDialog()
		return true
	}
	name := fields[1]
	if err := m.switchAgent(name); err != nil {
		m.appendInfo(formatAgentUnavailable(err))
		return true
	}
	m.appendInfo(formatAgentSet(name))
	return true
}

// handleTuiLimitsSlash handles /budget and /steps.
func (m *tuiModel) handleTuiLimitsSlash(cmd string, fields []string) bool {
	_ = cmd
	switch strings.ToLower(fields[0]) {
	case "/budget":
		if m.waiting {
			m.appendInfo("(finish the current turn before /budget)")
			return true
		}
		n, hasArg, ok := parseNonNegInt(fields)
		if hasArg {
			if !ok {
				arg := ""
				if len(fields) >= 2 {
					arg = fields[1]
				}
				m.appendInfo(formatBudgetInvalid(arg))
				return true
			}
			if err := m.session.SetPromptBudget(n); err != nil {
				m.appendInfo("invalid budget: " + err.Error())
				return true
			}
			m.appendInfo(formatBudgetSet(m.session.PromptBudget()))
			return true
		}
		m.appendInfo(formatBudgetSummary(m.session.PromptBudget()))
		return true
	case "/steps":
		n, hasArg, ok := parseNonNegInt(fields)
		if hasArg {
			if !ok {
				arg := ""
				if len(fields) >= 2 {
					arg = fields[1]
				}
				m.appendInfo(formatStepsInvalid(arg))
				return true
			}
			if err := m.session.SetMaxSteps(n); err != nil {
				m.appendInfo("invalid steps: " + err.Error())
				return true
			}
			m.appendInfo(formatStepsSet(n))
			return true
		}
		m.appendInfo(formatStepsSummary(m.session.MaxStepsValue()))
		return true
	default:
		return false
	}
}

// handleTuiSessionLifecycleSlash handles /new, /clear, and /sessions.
func (m *tuiModel) handleTuiSessionLifecycleSlash(cmd string, fields []string) bool {
	_ = cmd
	switch strings.ToLower(fields[0]) {
	case "/clear":
		// /clear saves then wipes the transcript, so it would silently discard
		// a turn still in flight while the transcript keeps rendering it as
		// completed. Same guard as /new: block it while busy.
		if m.waiting {
			m.appendInfo("(finish the current turn before /clear)")
			return true
		}
		// Save the conversation before clearing so it's recoverable.
		m.session.SaveAfterTurn()
		m.messages = nil
		m.blocks = nil
		if err := m.session.Clear(); err != nil {
			m.appendInfo("clear failed: " + err.Error())
			return true
		}
		m.msgOffset = 0
		m.appendInfo("history cleared")
		return true
	case "/new":
		m.startNewSession()
		return true
	case "/sessions":
		if m.sessionsSidebar != nil {
			m.sessionsSidebar = nil
			m.setFocus(focusComposer)
			m.layout()
			m.renderVP()
			return true
		}
		list, err := m.listSessions()
		if err == nil {
			m.sessions = list
		} else {
			m.appendInfo("sessions refresh failed: " + err.Error())
		}
		if !newChatPaneLayout(m.width, true, m.workflowsSidebar != nil).sidebarVisible {
			m.appendInfo("sessions sidebar needs a wider terminal")
			return true
		}
		m.sessionsSidebar = newSessionsSidebar()
		m.setFocus(focusSidebar)
		m.layout()
		m.renderVP()
		return true
	case "/worktrees":
		m.openWorktreeDialog()
		return true
	default:
		return false
	}
}

// handleTuiWorkflowsSlash toggles the workflow-run sidebar. The first use
// opens it, the second closes it; esc closes it too. It refuses when the
// right sidebar does not fit next to the chat pane.
func (m *tuiModel) handleTuiWorkflowsSlash() bool {
	if m.workflowsSidebar != nil {
		m.workflowsSidebar = nil
		m.setFocus(focusComposer)
		m.layout()
		m.renderVP()
		return true
	}
	if !newChatPaneLayout(m.width, m.sessionsSidebar != nil, true).rightSidebarVisible {
		m.appendInfo("workflows sidebar needs a wider terminal")
		return true
	}
	m.workflowsSidebar = newWorkflowsSidebar()
	// The first population is async: the ledger read runs off the update
	// goroutine and is triggered by the next uiTickMsg heartbeat (or a
	// workflow event), so opening the sidebar never blocks on the ledger.
	m.setFocus(focusWorkflowsSidebar)
	m.layout()
	m.renderVP()
	return true
}

// handleTuiSessionStoreSlash handles /save, /load, /list, /delete, /session.
func (m *tuiModel) handleTuiSessionStoreSlash(cmd string, fields []string) bool {
	_ = cmd
	switch strings.ToLower(fields[0]) {
	case "/save":
		if len(fields) >= 2 {
			if err := m.session.Save(fields[1]); err != nil {
				m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("save error: " + err.Error()), Rendered: tuiErrorStyle.Render("save error: " + err.Error())})
			} else {
				if err := m.refreshSessionList(); err != nil {
					m.appendInfo("sessions refresh failed: " + err.Error())
				}
				m.appendInfo(saveSessionResult(fields[1], m.session.MessagesCount(), m.session.UserTurns()))
			}
		} else {
			m.appendInfo("usage: /save <name>")
		}
		return true
	case "/load":
		m.runLoadSlash(fields)
		return true
	case "/list":
		sessions, err := m.listSessions()
		if err != nil {
			m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("list error: " + err.Error()), Rendered: tuiErrorStyle.Render("list error: " + err.Error())})
		} else if len(sessions) == 0 {
			m.appendInfo("no saved sessions")
		} else {
			m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiHeaderStyle.Render("── saved sessions ──"), Rendered: tuiHeaderStyle.Render("── saved sessions ──")})
			for _, si := range sessions {
				marker := ""
				if chat.IsAutoSaveName(si.Name) {
					marker = " [auto]"
				}
				name := displaySessionName(si, latestAutoSaveName(sessions))
				line := fmt.Sprintf("  %-20s %3d msgs%s", name, si.MessageCount, marker)
				if si.SessionID != "" {
					line += " · " + si.Reference()
				}
				m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiDimStyle.Render(line), Rendered: tuiDimStyle.Render(line)})
			}
		}
		return true
	case "/delete":
		if len(fields) >= 2 {
			if err := m.session.DeleteSession(fields[1]); err != nil {
				m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("delete error: " + err.Error()), Rendered: tuiErrorStyle.Render("delete error: " + err.Error())})
			} else {
				if err := m.refreshSessionList(); err != nil {
					m.appendInfo("sessions refresh failed: " + err.Error())
				}
				m.appendInfo(deleteSessionResult(fields[1]))
			}
		} else {
			m.appendInfo("usage: /delete <session-id>")
		}
		return true
	case "/session":
		m.appendInfo(fmt.Sprintf("messages: %d, turns: %d", m.session.MessagesCount(), m.session.UserTurns()))
		return true
	default:
		return false
	}
}

// runLoadSlash performs the TUI /load. A load replaces the transcript, the
// binding and the tool surface, so it is a session switch like /new, not a
// queue action: it runs on the Bubble Tea update goroutine while a turn runs on
// a worker. The session refuses a load while work is active too, but that
// refusal names a busy session; gating here keeps the message in the same words
// as /new and /budget.
func (m *tuiModel) runLoadSlash(fields []string) {
	if m.waiting {
		m.appendInfo("(finish the current turn before /load)")
		return
	}
	if len(fields) < 2 {
		// Correct usage string on TUI (classic preserves a historical typo).
		m.appendInfo("usage: /load <session-id>")
		return
	}
	if err := m.session.Load(fields[1]); err != nil {
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("load error: " + err.Error()), Rendered: tuiErrorStyle.Render("load error: " + err.Error())})
		return
	}
	m.activeSession = nil
	if err := m.refreshSessionList(); err != nil {
		m.appendInfo("sessions refresh failed: " + err.Error())
	} else {
		for i := range m.sessions {
			si := m.sessions[i]
			if si.Reference() == fields[1] && !si.WorktreeRoute {
				m.activeSession = &si
				break
			}
		}
	}
	m.modelName = shortenModel(m.session.CurrentModel())
	m.messages = nil
	m.blocks = nil
	if m.session.LoadedContextSession() {
		m.appendInfo(loadContextSessionResult(fields[1], m.session.MessagesCount(), m.session.UserTurns()))
	} else {
		m.appendInfo(loadSessionResult(fields[1], m.session.MessagesCount(), m.session.UserTurns()))
	}
	m.msgOffset = 0 // all messages loaded
	msgs := m.session.MessagesCopy()
	for _, block := range HydrateChatBlocksForView(msgs) {
		m.appendBlock(block)
	}
	m.appendModelRestoreNotice()
}

// handleTuiMiscSlash handles /select and /plain toggles/hints.
func (m *tuiModel) handleTuiMiscSlash(cmd string, fields []string) bool {
	_ = cmd
	switch strings.ToLower(fields[0]) {
	case "/select":
		// Same toggle as F2, for terminals and multiplexers that swallow
		// function keys. The mode flips here, not in whichever caller happens
		// to drain a flag: staging the toggle itself made /select a no-op from
		// the queue and the welcome screen, and then fired it later on an
		// unrelated command.
		m.pendingSelectCmd = m.toggleSelectMode()
		return true
	case "/plain":
		m.appendInfo("restart with: mivia chat --plain")
		return true
	default:
		return false
	}
}

// handleTuiResumeSlash routes /resume to the existing resume helper.
func (m *tuiModel) handleTuiResumeSlash(cmd string, fields []string) bool {
	_ = cmd
	return m.handleResumeSlash(fields)
}

func (m *tuiModel) appendModelRestoreNotice() {
	if saved, current, ok := m.session.ModelRestoreNotice(); ok {
		m.appendInfo(modelRestoreNoticeText(saved, current))
	}
	m.appendAdmissionNotes()
}

// appendAdmissionNotes surfaces deferred-tool admission notices: a resume that
// dropped a stale admitted set, or a staged load that has not been able to
// publish yet. Silence there would leave the user with a tool surface that
// disagrees with what the transcript shows.
func (m *tuiModel) appendAdmissionNotes() {
	for _, note := range m.session.TakeAdmissionNotes() {
		m.appendInfo(note)
	}
}
