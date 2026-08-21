package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func handleSlashAgent(fields []string, sess *chat.Session, res *config.Resolved, term *Terminal, state *AgentSessionState) (bool, bool, error) {
	sink := terminalSlashSink{t: term}
	if state == nil || state.Registry == nil || state.Registry.Len() == 0 {
		sink.Info("no agents loaded (add .mivia/agents/<name>.toml)")
		return true, false, nil
	}
	if len(fields) < 2 {
		sink.Info(FormatAgentCurrent(CurrentAgentName(state), state.Registry))
		return true, false, nil
	}
	name := fields[1]
	if err := ApplySessionAgent(sess, res, state, name, false); err != nil {
		sink.Info(FormatAgentUnavailable(err))
		return true, false, nil
	}
	sink.Info(FormatAgentSet(name))
	return true, false, nil
}

// handleSlashCompact runs /compact. In --json line mode the result must reach
// stdout as typed NDJSON (a frontend driving this process over stdin has
// nowhere else to look), so handleSlashCompactJSON attaches the turn event
// callback for the duration of the compact: the session's own post-commit
// emission (emitContextCompaction -> agent.EmitCompaction) then writes the
// same "compaction" event a turn's automatic compaction emits - one field
// mapping, not a second one here. At slash time no turn is in flight, so
// OnAgentEvent is otherwise unset and nothing double-emits.
func handleSlashCompact(line string, sess *chat.Session, res *config.Resolved, term *Terminal) (bool, bool, error) {
	focus := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "/compact")), "/compact"))
	if term == nil && activeJSONSlashSink != nil {
		return handleSlashCompactJSON(sess, res, focus)
	}
	if _, err := sess.CompactWithResult(context.Background(), focus); err != nil {
		reportCompactFailure(term, err)
		return true, false, nil
	}
	usage := sess.ContextUsage()
	message := compactResultMessage(sess, res, usage)
	if term == nil {
		fmt.Fprintln(os.Stderr, message)
	} else {
		term.WriteString("\n" + message)
	}
	return true, false, nil
}

// handleSlashCompactJSON is the --json line-mode /compact leg: the typed
// "compaction" event flows through the temporarily-attached callback, then a
// context_usage refresh follows so a consumer updates its context indicator in
// the same read. On failure, slash_error is the sole authoritative signal -
// stderr prose is invisible to a frontend parsing stdout.
func handleSlashCompactJSON(sess *chat.Session, res *config.Resolved, focus string) (bool, bool, error) {
	w := activeJSONSlashSink.w
	previous := sess.SwapOnAgentEvent(jsonTurnEventCallback(w))
	_, err := sess.CompactWithResult(context.Background(), focus)
	sess.SwapOnAgentEvent(previous)
	if err != nil {
		activeJSONSlashSink.Error("context compaction failed: " + err.Error())
		return true, false, nil
	}
	if reason := SummaryDisabledReason(sess, res); reason != "" {
		activeJSONSlashSink.Info(CompactStructuralOnlyNotice(reason))
	}
	writeContextUsageLine(w, sess)
	return true, false, nil
}

// CompactStructuralOnlyNotice explains an instant, LLM-free compaction. A
// structural-only compact returns at once and makes no provider call, which is
// correct but reads as a broken summarizer; naming the unmet condition is the
// difference between "it did nothing" and "it is not configured to do that".
func CompactStructuralOnlyNotice(reason string) string {
	return "compaction was structural only (no LLM summary): " + reason
}

// compactResultMessage frames the human-readable /compact result, appending the
// structural-only explanation when summarization is not configured.
func compactResultMessage(sess *chat.Session, res *config.Resolved, usage chat.ContextUsage) string {
	message := fmt.Sprintf("context compacted (%d%% used, %s/%s prompt)", usage.Percent, chat.FormatTokenK(usage.UsedTokens), chat.FormatTokenK(usage.BudgetTokens))
	if reason := SummaryDisabledReason(sess, res); reason != "" {
		message += "\n  " + CompactStructuralOnlyNotice(reason)
	}
	return message
}

func reportCompactFailure(term *Terminal, err error) {
	message := "context compaction failed: " + err.Error()
	if term == nil {
		fmt.Fprintln(os.Stderr, message)
	} else {
		term.WriteString("\n" + message)
	}
}

func handleSlashInfo(cmd string, fields []string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) (bool, bool, error) {
	sink := SlashSinkFor(term)
	switch cmd {
	case "/agents":
		term.WriteString("\n" + FormatAgentCurrent(CurrentAgentName(classicAgentState), RegistryForState(classicAgentState)))
		return true, false, nil
	case "/status":
		binding := sess.CurrentBinding()
		messages := sess.MessagesCopy()
		usage := sess.ContextUsage()
		term.WriteString(fmt.Sprintf("\nprovider=%s model=%s tools=%v turns=%d messages=%d context=%d tokens (est.)", binding.Completer.Name(), binding.Model, toolsOn && sess.UseTools, sess.UserTurns(), len(messages), usage.UsedTokens))
		term.WriteString("\neffort=" + FormatEffortStatus(sess.ReasoningSetting(), len(sess.ReasoningChoices()) > 0))
		if usage.BudgetTokens > 0 {
			term.WriteString(fmt.Sprintf("\ncontext=%s (output=%s, prompt budget=%s, %d%% used)", chat.FormatTokenK(usage.ContextWindowTokens), chat.FormatTokenK(usage.OutputReserveTokens), chat.FormatTokenK(usage.BudgetTokens), usage.Percent))
		}
		if classicAgentState != nil {
			term.WriteString("\n" + strings.TrimSpace(formatSessionAgentStatus(classicAgentState, sess)))
		}
	case "/model":
		defaultProvider := ""
		if res != nil {
			defaultProvider = res.ProviderName
		}
		providerName, modelName, hasArg := ParseModelArgs(fields, sess.CurrentSelection().ProviderName, defaultProvider)
		choices := ModelSwitchChoices(res, providerName, defaultProvider)
		if !hasArg {
			sink.Info(formatModelCurrent(sess.CurrentModel(), choices))
			return true, false, nil
		}
		discarded, err := SwitchModelCommand(sess, res, providerName, modelName)
		if err != nil {
			sink.Error(FormatModelUnavailable(providerName, choices))
			return true, false, nil
		}
		sink.Info(FormatModelSet(sess.CurrentSelection().ProviderName, sess.CurrentModel(), discarded))
		if jsink, ok := sink.(*JSONSlashSink); ok {
			jsink.ModelChanged(sess.CurrentSelection().ProviderName, sess.CurrentModel(), discarded)
		}
	case "/provider":
		term.WriteString(fmt.Sprintf("\nprovider=%s (restart with --provider to switch)", res.ProviderName))
	case "/tools":
		// Session.Tools is mu-guarded and a turn boundary republishes it, so
		// the listing reads one snapshot rather than the live field.
		registry, _, _ := sess.AgentSurfaceSnapshot()
		if registry == nil {
			term.WriteString("\ntools disabled (--no-tools)")
			return true, false, nil
		}
		for _, t := range registry.List() {
			term.WriteString(fmt.Sprintf("\n  %s - %s", t.Name(), t.Description()))
		}
		term.WriteString("\n" + classicAgentState.SchemaMassSnapshot().String())
	case "/workspace":
		if registry, _, _ := sess.AgentSurfaceSnapshot(); registry == nil {
			term.WriteString("\ntools disabled")
			return true, false, nil
		}
		cwd, _ := os.Getwd()
		term.WriteString(fmt.Sprintf("\nworkspace defaults to process cwd unless --workspace set: %s", cwd))
	}
	return true, false, nil
}

// RegistryForState implements registry for state.
func RegistryForState(state *AgentSessionState) *agents.AgentRegistry {
	if state == nil {
		return nil
	}
	return state.Registry
}

func handleSlashLimits(cmd string, fields []string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	if cmd == "/budget" {
		return handleBudget(fields, sess, term)
	}
	sink := terminalSlashSink{t: term}
	n, hasArg, ok := ParseNonNegInt(fields)
	if hasArg {
		if !ok {
			arg := ""
			if len(fields) >= 2 {
				arg = fields[1]
			}
			sink.Info(FormatStepsInvalid(arg))
			return true, false, nil
		}
		if err := sess.SetMaxSteps(n); err != nil {
			sink.Info("invalid step limit: " + err.Error())
			return true, false, nil
		}
		sink.Info(FormatStepsSet(n))
		return true, false, nil
	}
	sink.Info(FormatStepsSummary(sess.MaxStepsValue()))
	return true, false, nil
}

func handleBudget(fields []string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	sink := terminalSlashSink{t: term}
	n, hasArg, ok := ParseNonNegInt(fields)
	if hasArg {
		if !ok {
			arg := ""
			if len(fields) >= 2 {
				arg = fields[1]
			}
			sink.Info(FormatBudgetInvalid(arg))
			return true, false, nil
		}
		if err := sess.SetPromptBudget(n); err != nil {
			sink.Info("invalid budget: " + err.Error())
			return true, false, nil
		}
		sink.Info(FormatBudgetSet(sess.PromptBudget()))
		return true, false, nil
	}
	sink.Info(FormatBudgetSummary(sess.PromptBudget()))
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
		sink.Info(SaveSessionResult(name, len(sess.Messages), sess.UserTurns()))
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
		if sess.LoadedContextSession() {
			sink.Info(LoadContextSessionResult(name, len(sess.Messages), sess.UserTurns()) + "\n")
		} else {
			sink.Info(LoadSessionResult(name, len(sess.Messages), sess.UserTurns()) + "\n")
		}
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
		sink.Info(DeleteSessionResult(name))
	case "/session":
		return showSession(sess, term)
	}
	return true, false, nil
}

func writeModelRestoreNotice(term *Terminal, sess *chat.Session) {
	if saved, current, ok := sess.ModelRestoreNotice(); ok {
		terminalSlashSink{t: term}.Info("(" + ModelRestoreNoticeText(saved, current) + ")")
	}
	for _, note := range sess.TakeAdmissionNotes() {
		terminalSlashSink{t: term}.Info("(" + note + ")")
	}
}

func listSessions(sess *chat.Session, term *Terminal) (bool, bool, error) {
	sessions, err := sess.ListSessions()
	if err != nil {
		term.WriteString(fmt.Sprintf("\nlist error: %v", err))
		return true, false, nil
	}
	sessions = CollapseConversations(sessions)
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
	usage := sess.ContextUsage()
	term.WriteString(fmt.Sprintf("\ncurrent: %d messages, %d turns, ~%d tokens (%d%% context)", len(sess.Messages), sess.UserTurns(), usage.UsedTokens, usage.Percent))
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
		c := FindCoordinator()
		if c == nil {
			term.WriteString("\nno active orchestration runs")
			return true, false, nil
		}
		runs, err := ListInterruptedRuns(context.Background(), c)
		if err != nil {
			term.WriteString(fmt.Sprintf("\nerror: %v", err))
			return true, false, nil
		}
		term.WriteString("\n" + FormatListedRuns(runs))
		return true, false, nil
	}
	// With a run ID: show confirmation and resume.
	runID := fields[1]
	c := FindCoordinator()
	if c == nil {
		term.WriteString("\nno active orchestration runs")
		return true, false, nil
	}
	d := FindDispatcher()
	_, err := ResumeRun(context.Background(), c, d, runID, nil)
	if err != nil {
		term.WriteString(fmt.Sprintf("\n%v", FormatResumeError(err, runID)))
		return true, false, nil
	}
	term.WriteString(fmt.Sprintf("\nrun %s resumed", runID))
	return true, false, nil
}
