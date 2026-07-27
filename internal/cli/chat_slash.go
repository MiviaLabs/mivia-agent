package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func handleSlash(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) (handled, exit bool, err error) {
	if !strings.HasPrefix(line, "/") {
		return false, false, nil
	}
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/exit", "/quit", "/q":
		return true, true, nil
	case "/help", "/h", "/?":
		if term != nil {
			ShowHelpDialog(term)
		} else {
			fmt.Fprint(os.Stderr, slashHelp)
		}
		return true, false, nil
	case "/clear":
		sess.Clear()
		term.WriteString("\n(history cleared)")
		return true, false, nil
	case "/status":
		tokens := provider.MessagesTokens(sess.Messages)
		term.WriteString(fmt.Sprintf(
			"\nprovider=%s model=%s tools=%v turns=%d messages=%d context=%d tokens (est.)",
			sess.Completer.Name(), sess.Model, toolsOn && sess.UseTools, sess.UserTurns(), len(sess.Messages), tokens))
		if sess.MaxContextTokens > 0 {
			pct := 100 * tokens / sess.MaxContextTokens
			term.WriteString(fmt.Sprintf("\ncontext budget=%d tokens (%d%% used)", sess.MaxContextTokens, pct))
		}
		return true, false, nil
	case "/model":
		if len(fields) < 2 {
			term.WriteString(fmt.Sprintf("\ncurrent model=%s\nusage: /model deepseek-v4-flash|deepseek-v4-pro|<name>", sess.Model))
			return true, false, nil
		}
		sess.Model = fields[1]
		term.WriteString(fmt.Sprintf("\n(model set to %s)", sess.Model))
		return true, false, nil
	case "/provider":
		term.WriteString(fmt.Sprintf("\nprovider=%s (restart with --provider to switch)", res.ProviderName))
		return true, false, nil
	case "/tools":
		if sess.Tools == nil {
			term.WriteString("\ntools disabled (--no-tools)")
			return true, false, nil
		}
		for _, t := range sess.Tools.List() {
			term.WriteString(fmt.Sprintf("\n  %s â€” %s", t.Name(), t.Description()))
		}
		return true, false, nil
	case "/workspace":
		if sess.Tools == nil {
			term.WriteString("\ntools disabled")
			return true, false, nil
		}
		cwd, _ := os.Getwd()
		term.WriteString(fmt.Sprintf("\nworkspace defaults to process cwd unless --workspace set: %s", cwd))
		return true, false, nil
	case "/budget":
		if len(fields) >= 2 {
			var n int
			if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil || n < 0 {
				term.WriteString(fmt.Sprintf("\ninvalid budget %q; use a positive number", fields[1]))
				return true, false, nil
			}
			if n == 0 {
				n = chat.DefaultMaxContextTokens
			}
			sess.MaxContextTokens = n
			term.WriteString(fmt.Sprintf("\n(context budget set to %d tokens)", n))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\ncontext budget=%d tokens\nusage: /budget <tokens>\n  set to 0 for default (%d)", sess.MaxContextTokens, chat.DefaultMaxContextTokens))
		return true, false, nil
	case "/steps":
		if len(fields) >= 2 {
			var n int
			if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil || n < 0 {
				term.WriteString(fmt.Sprintf("\ninvalid step limit %q; use a positive number (0 = unlimited)", fields[1]))
				return true, false, nil
			}
			sess.MaxSteps = n
			if n <= 0 {
				term.WriteString("\n(max steps set to unlimited)")
			} else {
				term.WriteString(fmt.Sprintf("\n(max steps set to %d)", n))
			}
			return true, false, nil
		}
		if sess.MaxSteps <= 0 {
			term.WriteString("\nmax steps: unlimited\nusage: /steps <n> (set to 0 for unlimited)")
		} else {
			term.WriteString(fmt.Sprintf("\nmax steps: %d\nusage: /steps <n> (set to 0 for unlimited)", sess.MaxSteps))
		}
		return true, false, nil
	case "/save":
		name := strings.TrimSpace(strings.TrimPrefix(line, cmd))
		name = strings.TrimSpace(strings.TrimPrefix(name, "/save"))
		if name == "" {
			term.WriteString("\nusage: /save <name>")
			return true, false, nil
		}
		if err := sess.Save(name); err != nil {
			term.WriteString(fmt.Sprintf("\nsave error: %v", err))
			return true, false, nil
		}
		turns := sess.UserTurns()
		term.WriteString(fmt.Sprintf("\n(session %q saved â€” %d messages, %d turns)", name, len(sess.Messages), turns))
		return true, false, nil
	case "/load":
		name := strings.TrimSpace(strings.TrimPrefix(line, cmd))
		name = strings.TrimSpace(strings.TrimPrefix(name, "/load"))
		if name == "" {
			term.WriteString("\nusage: /load <name>")
			return true, false, nil
		}
		if err := sess.Load(name); err != nil {
			term.WriteString(fmt.Sprintf("\nload error: %v", err))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(session %q loaded â€” %d messages, %d turns)\n", name, len(sess.Messages), sess.UserTurns()))
		// Render loaded conversation history using term as the writer.
		r := NewChatRenderer(term, sess.Model)
		r.RenderHistory(sess.Messages)
		return true, false, nil
	case "/list":
		sessions, err := sess.ListSessions()
		if err != nil {
			term.WriteString(fmt.Sprintf("\nlist error: %v", err))
			return true, false, nil
		}
		if len(sessions) == 0 {
			term.WriteString("\n(no saved sessions)")
			return true, false, nil
		}
		term.WriteString("\nsaved sessions:")
		for _, si := range sessions {
			ago := time.Since(si.UpdatedAt).Truncate(time.Second)
			marker := ""
			if chat.IsAutoSaveName(si.Name) {
				marker = " [auto]"
			}
			term.WriteString(fmt.Sprintf("\n  %-20s  %3d msgs  %3d turns  ~%6d tok  %s ago%s  (%s)",
				si.Name, si.MessageCount, si.TurnCount, si.TokenCount, ago, marker, si.Model))
		}
		return true, false, nil
	case "/delete":
		name := strings.TrimSpace(strings.TrimPrefix(line, cmd))
		name = strings.TrimSpace(strings.TrimPrefix(name, "/delete"))
		if name == "" {
			term.WriteString("\nusage: /delete <name>")
			return true, false, nil
		}
		if err := sess.DeleteSession(name); err != nil {
			term.WriteString(fmt.Sprintf("\ndelete error: %v", err))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(session %q deleted)", name))
		return true, false, nil
	case "/session":
		term.WriteString(fmt.Sprintf("\ncurrent: %d messages, %d turns, ~%d tokens",
			len(sess.Messages), sess.UserTurns(), provider.MessagesTokens(sess.Messages)))
		term.WriteString(fmt.Sprintf("\nsessions dir: %s", sess.SessionDir))
		sessions, listErr := sess.ListSessions()
		if listErr != nil {
			term.WriteString(fmt.Sprintf("\nsaved: (list error: %v)", listErr))
		} else if len(sessions) > 0 {
			term.WriteString(fmt.Sprintf("\nsaved: %d session(s)", len(sessions)))
		} else {
			term.WriteString("\nno saved sessions yet")
		}
		return true, false, nil
	default:
		term.WriteString(fmt.Sprintf("\nunknown command %q (try /help)", cmd))
		return true, false, nil
	}
}
