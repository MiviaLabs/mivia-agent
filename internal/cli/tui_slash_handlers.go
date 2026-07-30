package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
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
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiHeaderStyle.Render("── help ──"), Rendered: tuiHeaderStyle.Render("── help ──")})
		help := RenderMarkdown(slashHelpMD, max(40, m.width-2))
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: help, Rendered: help})
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
	case "/status":
		msgs := m.session.MessagesCopy()
		tokens := provider.MessagesTokens(msgs)
		m.appendInfo(fmt.Sprintf("provider=%s model=%s tools=%v turns=%d msgs=%d tokens=%d",
			m.session.Completer.Name(), m.session.Model, m.toolsOn && m.session.UseTools,
			m.session.UserTurns(), len(msgs), tokens))
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
				m.messages = nil
				m.blocks = nil
				m.appendInfo(fmt.Sprintf("session %q loaded", fields[1]))
				m.msgOffset = 0 // all messages loaded
				msgs := m.session.MessagesCopy()
				for _, block := range HydrateChatBlocksForView(msgs) {
					m.appendBlock(block)
				}
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
		m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiHeaderStyle.Render("── tools ──"), Rendered: tuiHeaderStyle.Render("── tools ──")})
		for _, t := range m.session.Tools.List() {
			m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: tuiDimStyle.Render(fmt.Sprintf("  %s %s — %s", toolIconForName(t.Name()), t.Name(), t.Description())), Rendered: tuiDimStyle.Render(fmt.Sprintf("  %s %s — %s", toolIconForName(t.Name()), t.Name(), t.Description()))})
		}
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
