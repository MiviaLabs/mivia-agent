package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// slashSink is the only thing classic REPL and TUI differ on for pure slash
// feedback: where text goes. Classic writes prose to the terminal; TUI appends
// styled blocks via its own methods and does not need this interface.
type slashSink interface {
	Info(s string)
	Error(s string)
}

// terminalSlashSink adapts *Terminal to slashSink with the classic REPL
// leading-newline convention (term.WriteString("\n"+s)).
type terminalSlashSink struct {
	t *Terminal
}

func (s terminalSlashSink) Info(msg string) {
	if s.t != nil {
		s.t.WriteString("\n" + msg)
	}
}

func (s terminalSlashSink) Error(msg string) {
	if s.t != nil {
		s.t.WriteString("\n" + msg)
	}
}

// ParseModelArgs extracts provider/model from a /model slash line.
// fields[0] is the command token. hasArg is false when only /model was given.
func ParseModelArgs(fields []string, currentProvider, defaultProvider string) (provider, model string, hasArg bool) {
	provider = currentProvider
	if provider == "" {
		provider = defaultProvider
	}
	if len(fields) < 2 {
		return provider, "", false
	}
	model = fields[1]
	if len(fields) >= 3 {
		provider = fields[1]
		model = strings.Join(fields[2:], " ")
	}
	return provider, model, true
}

// ParseNonNegInt parses fields[1] as a non-negative integer for /budget and /steps.
// hasArg is false when no argument was supplied; ok is false on parse failure.
func ParseNonNegInt(fields []string) (n int, hasArg bool, ok bool) {
	if len(fields) < 2 {
		return 0, false, false
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil || n < 0 {
		return 0, true, false
	}
	return n, true, true
}

// ModelSwitchChoices returns the selectable model list for providerName.
func ModelSwitchChoices(res *config.Resolved, providerName, defaultProvider string) string {
	if res == nil {
		return ""
	}
	if providerName == "" {
		providerName = defaultProvider
	}
	return res.ModelChoicesFor(providerName)
}

// ModelRestoreNoticeText is the single shared wording for a failed model
// restore after /load or auto-restore. Call sites must not re-format this.
func ModelRestoreNoticeText(saved, current string) string {
	return fmt.Sprintf("session was saved with model %q, which is not available; using %s", saved, current)
}

// FormatBudgetSummary implements format budget summary.
func FormatBudgetSummary(budget int) string {
	return fmt.Sprintf("context budget=%d tokens\nusage: /budget <tokens>\n  set to 0 for model default", budget)
}

// FormatBudgetSet implements format budget set.
func FormatBudgetSet(budget int) string {
	return fmt.Sprintf("(context budget set to %d tokens)", budget)
}

// FormatBudgetInvalid implements format budget invalid.
func FormatBudgetInvalid(arg string) string {
	return fmt.Sprintf("invalid budget %q; use a positive number", arg)
}

// FormatStepsSummary implements format steps summary.
func FormatStepsSummary(steps int) string {
	if steps <= 0 {
		return "max steps: unlimited\nusage: /steps <n> (set to 0 for unlimited)"
	}
	return fmt.Sprintf("max steps: %d\nusage: /steps <n> (set to 0 for unlimited)", steps)
}

// FormatStepsSet implements format steps set.
func FormatStepsSet(steps int) string {
	if steps <= 0 {
		return "(max steps set to unlimited)"
	}
	return fmt.Sprintf("(max steps set to %d)", steps)
}

// FormatStepsInvalid implements format steps invalid.
func FormatStepsInvalid(arg string) string {
	return fmt.Sprintf("invalid step limit %q; use a positive number (0 = unlimited)", arg)
}

// SaveSessionResult implements save session result.
func SaveSessionResult(name string, msgs, turns int) string {
	return fmt.Sprintf("(session %q saved - %d messages, %d turns)", name, msgs, turns)
}

// LoadSessionResult implements load session result.
func LoadSessionResult(name string, msgs, turns int) string {
	return fmt.Sprintf("(session %q loaded - %d messages, %d turns)", name, msgs, turns)
}

// LoadContextSessionResult implements load context session result.
func LoadContextSessionResult(name string, msgs, turns int) string {
	return fmt.Sprintf("(durable session %q adopted into current session - %d messages, %d turns; subsequent turns write to the current session)", name, msgs, turns)
}

// DeleteSessionResult implements delete session result.
func DeleteSessionResult(name string) string {
	return fmt.Sprintf("(session %q deleted)", name)
}

func formatModelCurrent(model, choices string) string {
	if choices != "" {
		return fmt.Sprintf("current model=%s\navailable: %s", model, choices)
	}
	return fmt.Sprintf("current model=%s\nusage: /model <name>", model)
}

// FormatModelSet implements format model set.
func FormatModelSet(providerName, model string, discarded reasoning.Level) string {
	return fmt.Sprintf("(model set to %s/%s%s)", providerName, model, EffortDiscardedSuffix(discarded))
}

// EffortDiscardedSuffix is the one wording for a /effort choice a model switch
// dropped. The surfaces phrase the switch itself differently - the plain REPL
// parenthesises it, the picker does not - but a user who learns to recognise
// this clause on one of them must recognise it on the others.
func EffortDiscardedSuffix(discarded reasoning.Level) string {
	if !discarded.Active() {
		return ""
	}
	return fmt.Sprintf(" · effort %s discarded", discarded)
}

// FormatModelUnavailable implements format model unavailable.
func FormatModelUnavailable(providerName, choices string) string {
	if choices != "" {
		return fmt.Sprintf("model is not available for provider %s\navailable: %s", providerName, choices)
	}
	return "model name is invalid"
}
