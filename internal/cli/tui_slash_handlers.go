package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
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
	case "/budget", "/steps":
		return m.handleTuiLimitsSlash(cmd, fields)
	case "/new", "/clear", "/sessions":
		return m.handleTuiSessionLifecycleSlash(cmd, fields)
	case "/save", "/load", "/delete", "/list", "/session":
		return m.handleTuiSessionStoreSlash(cmd, fields)
	case "/select", "/plain":
		return m.handleTuiMiscSlash(cmd, fields)
	case "/resume":
		return m.handleTuiResumeSlash(cmd, fields)
	case "/hooks":
		m.appendInfo(hooksSlashOutput(fields))
		return true
	default:
		return false
	}
}

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
		if m.session.Tools == nil {
			m.appendInfo("tools disabled (--no-tools)")
			return true
		}
		var names []string
		for _, t := range m.session.Tools.List() {
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
	if err := m.switchModel(providerName, modelName); err != nil {
		m.appendInfo(formatModelUnavailable(providerName, choices))
		return true
	}
	m.modelName = shortenModel(m.session.CurrentModel())
	m.appendInfo(formatModelSet(m.session.CurrentSelection().ProviderName, m.session.CurrentModel()))
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
		// Save the conversation before clearing so it's recoverable.
		m.session.SaveAfterTurn()
		m.messages = nil
		m.blocks = nil
		m.session.Clear()
		m.msgOffset = 0
		m.appendInfo("history cleared")
		return true
	case "/new":
		// A turn in flight reads saveManager without a lock during its
		// writeback, so the SaveManager swap below would race it. /new is a
		// session-switch, not a queue action: block it while busy.
		if m.waiting {
			m.appendInfo("(finish the current turn before /new)")
			return true
		}
		m.resetForNewSession()
		m.appendInfo("new session started (previous conversation saved)")
		return true
	case "/sessions":
		// One place to switch, delete and purge - the same actions that used
		// to need /list, /load <name> and /delete <name> plus a name you had
		// to already know.
		m.openSessionsDialog()
		return true
	default:
		return false
	}
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
				m.appendInfo(saveSessionResult(fields[1], m.session.MessagesCount(), m.session.UserTurns()))
			}
		} else {
			m.appendInfo("usage: /save <name>")
		}
		return true
	case "/load":
		if len(fields) >= 2 {
			if err := m.session.Load(fields[1]); err != nil {
				m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("load error: " + err.Error()), Rendered: tuiErrorStyle.Render("load error: " + err.Error())})
			} else {
				m.modelName = shortenModel(m.session.CurrentModel())
				m.messages = nil
				m.blocks = nil
				m.appendInfo(loadSessionResult(fields[1], m.session.MessagesCount(), m.session.UserTurns()))
				m.msgOffset = 0 // all messages loaded
				msgs := m.session.MessagesCopy()
				for _, block := range HydrateChatBlocksForView(msgs) {
					m.appendBlock(block)
				}
				m.appendModelRestoreNotice()
			}
		} else {
			// Correct usage string on TUI (classic preserves a historical typo).
			m.appendInfo("usage: /load <name>")
		}
		return true
	case "/list":
		sessions, err := m.session.ListSessions()
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
				m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiDimStyle.Render(fmt.Sprintf("  %-20s %3d msgs%s", si.Name, si.MessageCount, marker)), Rendered: tuiDimStyle.Render(fmt.Sprintf("  %-20s %3d msgs%s", si.Name, si.MessageCount, marker))})
			}
		}
		return true
	case "/delete":
		if len(fields) >= 2 {
			if err := m.session.DeleteSession(fields[1]); err != nil {
				m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("delete error: " + err.Error()), Rendered: tuiErrorStyle.Render("delete error: " + err.Error())})
			} else {
				m.appendInfo(deleteSessionResult(fields[1]))
			}
		} else {
			m.appendInfo("usage: /delete <name>")
		}
		return true
	case "/session":
		m.appendInfo(fmt.Sprintf("messages: %d, turns: %d", m.session.MessagesCount(), m.session.UserTurns()))
		return true
	default:
		return false
	}
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
}
