package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

var handleSlashImpl = func(m *tuiModel, cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	if isLocalSlash(fields[0]) {
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiDimStyle.Render("  ⚙ " + strings.TrimSpace(cmd)), Rendered: tuiDimStyle.Render("  ⚙ " + strings.TrimSpace(cmd))})
	}
	switch strings.ToLower(fields[0]) {
	case "/help", "/h", "/?":
		// Reference material, not conversation: a closable dialog instead of
		// a permanent wall of text in the transcript.
		m.setOverlay(newHelpDialog(m.width))
		return true
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
	case "/status":
		m.setOverlay(m.newStatusDialog())
		return true
	case "/sessions":
		// One place to switch, delete and purge — the same actions that used
		// to need /list, /load <name> and /delete <name> plus a name you had
		// to already know.
		m.openSessionsDialog()
		return true
	case "/model":
		choices := ""
		providerName := "current provider"
		if m.config != nil {
			choices = m.config.ModelChoices()
			providerName = m.config.ProviderName
		}
		if len(fields) >= 2 {
			if !m.session.SelectModel(fields[1]) {
				if choices != "" {
					m.appendInfo("model is not available for provider " + providerName + "; available: " + choices)
				} else {
					m.appendInfo("model name is invalid")
				}
				return true
			}
			m.modelName = shortenModel(m.session.CurrentModel())
			m.appendInfo("model set to " + m.session.CurrentModel())
		} else {
			if choices != "" {
				m.appendInfo("current model: " + m.session.CurrentModel() + "; available: " + choices)
			} else {
				m.appendInfo("current model: " + m.session.CurrentModel() + "; usage: /model <name>")
			}
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
				m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("save error: " + err.Error()), Rendered: tuiErrorStyle.Render("save error: " + err.Error())})
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
				m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiErrorStyle.Render("load error: " + err.Error()), Rendered: tuiErrorStyle.Render("load error: " + err.Error())})
			} else {
				m.modelName = shortenModel(m.session.CurrentModel())
				m.messages = nil
				m.blocks = nil
				m.appendInfo(fmt.Sprintf("session %q loaded", fields[1]))
				m.msgOffset = 0 // all messages loaded
				msgs := m.session.MessagesCopy()
				for _, block := range HydrateChatBlocksForView(msgs) {
					m.appendBlock(block)
				}
				m.appendModelRestoreNotice()
			}
		} else {
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
				m.appendInfo(fmt.Sprintf("session %q deleted", fields[1]))
			}
		} else {
			m.appendInfo("usage: /delete <name>")
		}
		return true
	case "/session":
		m.appendInfo(fmt.Sprintf("messages: %d, turns: %d", m.session.MessagesCount(), m.session.UserTurns()))
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
	case "/resume":
		return m.handleResumeSlash(fields)
	default:
		return false
	}
}

func (m *tuiModel) appendModelRestoreNotice() {
	if saved, current, ok := m.session.ModelRestoreNotice(); ok {
		m.appendInfo(fmt.Sprintf("session was saved with model %q, which is not available; using %s", saved, current))
	}
}
