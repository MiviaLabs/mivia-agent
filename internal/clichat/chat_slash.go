package clichat

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func handleSlash(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) (bool, bool, error) {
	if !strings.HasPrefix(line, "/") {
		return false, false, nil
	}
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/exit", "/quit", "/q":
		return true, true, nil
	case "/help", "/h", "/?":
		return showSlashHelp(term)
	case "/clear":
		if err := sess.Clear(); err != nil {
			term.WriteString("\n(clear failed: " + err.Error() + ")")
			return true, false, nil
		}
		term.WriteString("\n(history cleared)")
		return true, false, nil
	case "/status", "/model", "/provider", "/tools", "/workspace", "/agents":
		return handleSlashInfo(cmd, fields, sess, res, toolsOn, term)
	case "/compact":
		return handleSlashCompact(line, sess, res, term)
	case "/agent":
		return handleSlashAgent(fields, sess, res, term, cliagents.ClassicAgentState)
	case "/hooks":
		return HandleSlashHooksFunc(fields, term)
	case "/budget", "/steps":
		return handleSlashLimits(cmd, fields, sess, term)
	case "/effort":
		return HandleSlashEffort(fields, sess, term)
	case "/save", "/load", "/list", "/delete", "/session":
		return handleSlashSessions(cmd, line, sess, term)
	case "/resume":
		return handleSlashResume(cmd, fields, term)
	default:
		term.WriteString(fmt.Sprintf("\nunknown command %q (try /help)", cmd))
		return true, false, nil
	}
}

func showSlashHelp(term *Terminal) (bool, bool, error) {
	if term != nil {
		ShowHelpDialog(term)
	} else {
		fmt.Fprint(os.Stderr, renderReplHelpInline())
	}
	return true, false, nil
}
