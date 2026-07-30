package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
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
		sess.SaveAfterTurn()
		sess.Clear()
		term.WriteString("\n(history cleared)")
		return true, false, nil
	case "/new":
		// Persist the outgoing chat as a distinct exit snapshot, then reset
		// identity + rolling SaveManager and clear in place. The classic REPL
		// is blocked at the prompt while a turn runs, so no busy-guard needed.
		_ = sess.SaveLast()
		sess.SessionID = runtime.NewSessionID()
		setActiveSessionCaller(runtime.Caller{SessionID: sess.SessionID})
		if store, ok := sess.Store().(*chat.FileSessionStore); ok && store != nil {
			mgr := chat.NewSaveManager(store, sess.Model, sess.Completer.Name())
			sess.SetSessionStore(store, mgr)
		}
		sess.Clear()
		term.WriteString("\n(new session; previous conversation saved)")
		return true, false, nil
	case "/status", "/model", "/provider", "/tools", "/workspace":
		return handleSlashInfo(cmd, fields, sess, res, toolsOn, term)
	case "/budget", "/steps":
		return handleSlashLimits(cmd, fields, sess, term)
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
		fmt.Fprint(os.Stderr, slashHelp)
	}
	return true, false, nil
}
