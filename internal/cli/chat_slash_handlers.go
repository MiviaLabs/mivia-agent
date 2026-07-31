package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func handleSlashInfo(cmd string, fields []string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) (bool, bool, error) {
	switch cmd {
	case "/status":
		binding := sess.CurrentBinding()
		messages := sess.MessagesCopy()
		tokens := provider.MessagesTokens(messages)
		term.WriteString(fmt.Sprintf("\nprovider=%s model=%s tools=%v turns=%d messages=%d context=%d tokens (est.)", binding.Completer.Name(), binding.Model, toolsOn && sess.UseTools, sess.UserTurns(), len(messages), tokens))
		if budget := sess.PromptBudget(); budget > 0 {
			term.WriteString(fmt.Sprintf("\ncontext budget=%d tokens (%d%% used)", budget, 100*tokens/budget))
		}
	case "/model":
		providerName := sess.CurrentSelection().ProviderName
		if providerName == "" {
			providerName = res.ProviderName
		}
		choices := res.ModelChoicesFor(providerName)
		if len(fields) < 2 {
			if choices != "" {
				term.WriteString(fmt.Sprintf("\ncurrent model=%s\navailable: %s", sess.CurrentModel(), choices))
			} else {
				term.WriteString(fmt.Sprintf("\ncurrent model=%s\nusage: /model <name>", sess.CurrentModel()))
			}
			return true, false, nil
		}
		modelName := fields[1]
		if len(fields) >= 3 {
			providerName = fields[1]
			modelName = strings.Join(fields[2:], " ")
		}
		choices = res.ModelChoicesFor(providerName)
		if err := switchModelCommand(sess, res, providerName, modelName); err != nil {
			if choices != "" {
				term.WriteString(fmt.Sprintf("\nmodel is not available for provider %s\navailable: %s", providerName, choices))
			} else {
				term.WriteString("\nmodel name is invalid")
			}
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(model set to %s/%s)", sess.CurrentSelection().ProviderName, sess.CurrentModel()))
	case "/provider":
		term.WriteString(fmt.Sprintf("\nprovider=%s (restart with --provider to switch)", res.ProviderName))
	case "/tools":
		if sess.Tools == nil {
			term.WriteString("\ntools disabled (--no-tools)")
			return true, false, nil
		}
		for _, t := range sess.Tools.List() {
			term.WriteString(fmt.Sprintf("\n  %s — %s", t.Name(), t.Description()))
		}
	case "/workspace":
		if sess.Tools == nil {
			term.WriteString("\ntools disabled")
			return true, false, nil
		}
		cwd, _ := os.Getwd()
		term.WriteString(fmt.Sprintf("\nworkspace defaults to process cwd unless --workspace set: %s", cwd))
	}
	return true, false, nil
}

func handleSlashLimits(cmd string, fields []string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	if cmd == "/budget" {
		return handleBudget(fields, sess, term)
	}
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
}

func handleBudget(fields []string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	if len(fields) >= 2 {
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 0 {
			term.WriteString(fmt.Sprintf("\ninvalid budget %q; use a positive number", fields[1]))
			return true, false, nil
		}
		if err := sess.SetPromptBudget(n); err != nil {
			term.WriteString("\ninvalid budget: " + err.Error())
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(context budget set to %d tokens)", sess.PromptBudget()))
		return true, false, nil
	}
	term.WriteString(fmt.Sprintf("\ncontext budget=%d tokens\nusage: /budget <tokens>\n  set to 0 for model default", sess.PromptBudget()))
	return true, false, nil
}

func handleSlashSessions(cmd, line string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, cmd)), cmd))
	switch cmd {
	case "/save":
		if name == "" {
			term.WriteString("\nusage: /save <name>")
			return true, false, nil
		}
		if err := sess.Save(name); err != nil {
			term.WriteString(fmt.Sprintf("\nsave error: %v", err))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(session %q saved — %d messages, %d turns)", name, len(sess.Messages), sess.UserTurns()))
	case "/load":
		if name == "" {
			term.WriteString("\nusage: /load <name")
			return true, false, nil
		}
		if err := sess.Load(name); err != nil {
			term.WriteString(fmt.Sprintf("\nload error: %v", err))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(session %q loaded — %d messages, %d turns)\n", name, len(sess.Messages), sess.UserTurns()))
		writeModelRestoreNotice(term, sess)
		NewChatRenderer(term, sess.CurrentModel()).RenderHistory(sess.Messages)
	case "/list":
		return listSessions(sess, term)
	case "/delete":
		if name == "" {
			term.WriteString("\nusage: /delete <name>")
			return true, false, nil
		}
		if err := sess.DeleteSession(name); err != nil {
			term.WriteString(fmt.Sprintf("\ndelete error: %v", err))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(session %q deleted)", name))
	case "/session":
		return showSession(sess, term)
	}
	return true, false, nil
}

func writeModelRestoreNotice(term *Terminal, sess *chat.Session) {
	if saved, current, ok := sess.ModelRestoreNotice(); ok {
		term.WriteString(fmt.Sprintf("\n(session was saved with model %q, which is not available; using %s)", saved, current))
	}
}

func listSessions(sess *chat.Session, term *Terminal) (bool, bool, error) {
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
		term.WriteString(fmt.Sprintf("\n  %-20s  %3d msgs  %3d turns  ~%6d tok  %s ago%s  (%s)", si.Name, si.MessageCount, si.TurnCount, si.TokenCount, ago, marker, si.Model))
	}
	return true, false, nil
}

func showSession(sess *chat.Session, term *Terminal) (bool, bool, error) {
	term.WriteString(fmt.Sprintf("\ncurrent: %d messages, %d turns, ~%d tokens", len(sess.Messages), sess.UserTurns(), provider.MessagesTokens(sess.Messages)))
	term.WriteString(fmt.Sprintf("\nsessions dir: %s", sess.SessionDir))
	sessions, err := sess.ListSessions()
	if err != nil {
		term.WriteString(fmt.Sprintf("\nsaved: (list error: %v)", err))
	} else if len(sessions) > 0 {
		term.WriteString(fmt.Sprintf("\nsaved: %d session(s)", len(sessions)))
	} else {
		term.WriteString("\nno saved sessions yet")
	}
	return true, false, nil
}

func handleSlashResume(cmd string, fields []string, term *Terminal) (bool, bool, error) {
	if term == nil {
		return true, false, nil
	}
	if len(fields) < 2 {
		// No argument: list interrupted runs.
		c := findCoordinator()
		if c == nil {
			term.WriteString("\nno active orchestration runs")
			return true, false, nil
		}
		runs, err := listInterruptedRuns(context.Background(), c)
		if err != nil {
			term.WriteString(fmt.Sprintf("\nerror: %v", err))
			return true, false, nil
		}
		term.WriteString("\n" + formatListedRuns(runs))
		return true, false, nil
	}
	// With a run ID: show confirmation and resume.
	runID := fields[1]
	c := findCoordinator()
	if c == nil {
		term.WriteString("\nno active orchestration runs")
		return true, false, nil
	}
	d := findDispatcher()
	_, err := resumeRun(context.Background(), c, d, runID, nil)
	if err != nil {
		term.WriteString(fmt.Sprintf("\n%v", formatResumeError(err, runID)))
		return true, false, nil
	}
	term.WriteString(fmt.Sprintf("\nrun %s resumed", runID))
	return true, false, nil
}
