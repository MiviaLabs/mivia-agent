package legacytui

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

// HandleSlashImpl is the TUI slash dispatcher. The var form is a deliberate
// test seam (budget_integration_test and others override it). Command bodies
// live in per-concern sub-handlers; this switch only routes.
var HandleSlashImpl = func(m *TUIModel, cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	if cli.IsLocalSlash(fields[0]) {
		m.appendBlock(cli.ChatBlock{Kind: cli.ChatBlockSystem, Text: TUIDimStyle.Render("  ⚙ " + strings.TrimSpace(cmd)), Rendered: TUIDimStyle.Render("  ⚙ " + strings.TrimSpace(cmd))})
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
		return m.handleTuiCompactSlash(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(cmd, "/compact")), "/compact")))
	case "/new", "/clear", "/sessions", "/worktrees":
		return m.handleTuiSessionLifecycleSlash(cmd, fields)
	case "/queue":
		return m.handleTuiQueueSlash()
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
		m.appendInfo(cli.HooksSlashOutput(fields))
		return true
	default:
		return false
	}
}

// FieldsFirst implements fields first.
func FieldsFirst(cmd string) string { return strings.Fields(cmd)[0] }
