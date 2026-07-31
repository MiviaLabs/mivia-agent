package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func handleSlashAgent(fields []string, sess *chat.Session, res *config.Resolved, term *Terminal, state *agentSessionState) (bool, bool, error) {
	sink := terminalSlashSink{t: term}
	if state == nil || state.Registry == nil || state.Registry.Len() == 0 {
		sink.Info("no agents loaded (add .mivia/agents/<name>.toml)")
		return true, false, nil
	}
	if len(fields) < 2 {
		sink.Info(formatAgentCurrent(currentAgentName(state), state.Registry))
		return true, false, nil
	}
	name := fields[1]
	if err := applySessionAgent(sess, res, state, name, false); err != nil {
		sink.Info(formatAgentUnavailable(err))
		return true, false, nil
	}
	sink.Info(formatAgentSet(name))
	return true, false, nil
}

func handleSlashInfo(cmd string, fields []string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) (bool, bool, error) {
	sink := terminalSlashSink{t: term}
	switch cmd {
	case "/status":
		binding := sess.CurrentBinding()
		messages := sess.MessagesCopy()
		tokens := provider.MessagesTokens(messages)
		term.WriteString(fmt.Sprintf("\nprovider=%s model=%s tools=%v turns=%d messages=%d context=%d tokens (est.)", binding.Completer.Name(), binding.Model, toolsOn && sess.UseTools, sess.UserTurns(), len(messages), tokens))
		if budget := sess.PromptBudget(); budget > 0 {
			term.WriteString(fmt.Sprintf("\ncontext budget=%d tokens (%d%% used)", budget, 100*tokens/budget))
		}
		if classicAgentState != nil && classicAgentState.Selected != nil {
			term.WriteString(fmt.Sprintf("\nagent=%s", classicAgentState.Selected.Name))
		}
	case "/model":
		defaultProvider := ""
		if res != nil {
			defaultProvider = res.ProviderName
		}
		providerName, modelName, hasArg := parseModelArgs(fields, sess.CurrentSelection().ProviderName, defaultProvider)
		choices := modelSwitchChoices(res, providerName, defaultProvider)
		if !hasArg {
			sink.Info(formatModelCurrent(sess.CurrentModel(), choices))
			return true, false, nil
		}
		if err := switchModelCommand(sess, res, providerName, modelName); err != nil {
			sink.Info(formatModelUnavailable(providerName, choices))
			return true, false, nil
		}
		sink.Info(formatModelSet(sess.CurrentSelection().ProviderName, sess.CurrentModel()))
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
	sink := terminalSlashSink{t: term}
	n, hasArg, ok := parseNonNegInt(fields)
	if hasArg {
		if !ok {
			arg := ""
			if len(fields) >= 2 {
				arg = fields[1]
			}
			sink.Info(formatStepsInvalid(arg))
			return true, false, nil
		}
		if err := sess.SetMaxSteps(n); err != nil {
			sink.Info("invalid step limit: " + err.Error())
			return true, false, nil
		}
		sink.Info(formatStepsSet(n))
		return true, false, nil
	}
	sink.Info(formatStepsSummary(sess.MaxStepsValue()))
	return true, false, nil
}

func handleBudget(fields []string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	sink := terminalSlashSink{t: term}
	n, hasArg, ok := parseNonNegInt(fields)
	if hasArg {
		if !ok {
			arg := ""
			if len(fields) >= 2 {
				arg = fields[1]
			}
			sink.Info(formatBudgetInvalid(arg))
			return true, false, nil
		}
		if err := sess.SetPromptBudget(n); err != nil {
			sink.Info("invalid budget: " + err.Error())
			return true, false, nil
		}
		sink.Info(formatBudgetSet(sess.PromptBudget()))
		return true, false, nil
	}
	sink.Info(formatBudgetSummary(sess.PromptBudget()))
	return true, false, nil
}

func handleSlashSessions(cmd, line string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, cmd)), cmd))
	sink := terminalSlashSink{t: term}
	switch cmd {
	case "/save":
		if name == "" {
			sink.Info("usage: /save <name>")
			return true, false, nil
		}
		if err := sess.Save(name); err != nil {
			sink.Info(fmt.Sprintf("save error: %v", err))
			return true, false, nil
		}
		sink.Info(saveSessionResult(name, len(sess.Messages), sess.UserTurns()))
	case "/load":
		if name == "" {
			// Preserve classic REPL typo (missing '>'); see plan non-goals.
			sink.Info("usage: /load <name")
			return true, false, nil
		}
		if err := sess.Load(name); err != nil {
			sink.Info(fmt.Sprintf("load error: %v", err))
			return true, false, nil
		}
		sink.Info(loadSessionResult(name, len(sess.Messages), sess.UserTurns()) + "\n")
		writeModelRestoreNotice(term, sess)
		NewChatRenderer(term, sess.CurrentModel()).RenderHistory(sess.Messages)
	case "/list":
		return listSessions(sess, term)
	case "/delete":
		if name == "" {
			sink.Info("usage: /delete <name>")
			return true, false, nil
		}
		if err := sess.DeleteSession(name); err != nil {
			sink.Info(fmt.Sprintf("delete error: %v", err))
			return true, false, nil
		}
		sink.Info(deleteSessionResult(name))
	case "/session":
		return showSession(sess, term)
	}
	return true, false, nil
}

func writeModelRestoreNotice(term *Terminal, sess *chat.Session) {
	if saved, current, ok := sess.ModelRestoreNotice(); ok {
		terminalSlashSink{t: term}.Info("(" + modelRestoreNoticeText(saved, current) + ")")
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
