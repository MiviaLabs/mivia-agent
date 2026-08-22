package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// EffortBusyNotice is the single wording for "this dial cannot move yet". The
// picker footer, the typed argument and a session refusal all describe the
// same state, so they say it the same way. Relocated from
// internal/legacytui/effort_dialog.go: needed unqualified there (the TUI
// picker) and by the classic-mode /effort handler here.
const EffortBusyNotice = "finish current work first"

// EffortOrchestrationNotice replaces the shared switch guard's wording. That
// guard is written for /model and /agent, and telling someone who typed
// /effort that "model switching is unavailable" names an action they did not
// take - and overflows the 52 columns the TUI dialog footer has at 80
// columns.
const EffortOrchestrationNotice = "effort is locked while orchestration runs"

// SessionEffortBusyRefusal is chat.Session's wording for an in-flight turn.
// It lives in another package with no sentinel to match, so this surface owns
// a copy of the sentence and a test in internal/legacytui keeps the copy
// honest.
const SessionEffortBusyRefusal = "reasoning effort cannot change while work is active"

// SafeEffortError keeps the session's own wording where it already names the
// level and the offered set, and rewrites the refusals that were written for
// another command. Unlike a model switch, nothing here can carry a credential
// or a provider message.
func SafeEffortError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, cliorchestrate.ErrOrchestrationSwitchActive) {
		return EffortOrchestrationNotice
	}
	if msg := err.Error(); msg != SessionEffortBusyRefusal {
		return msg
	}
	return EffortBusyNotice
}

// EffortUnsetWord is the one spelling of the unset state: the picker row and
// the typed argument use it, so what the user reads is what the user can type.
const EffortUnsetWord = "unset"

// EffortRowName names a reasoning level row. The unset level has no wire
// spelling of its own, so it needs a word here.
func EffortRowName(level reasoning.Level) string {
	if !level.Active() {
		return EffortUnsetWord
	}
	return string(level)
}

// ParseEffortArg reads a /effort argument. It accepts the unset word on top of
// the levels, which is how the text surfaces reach the state reasoning.Level
// spells as empty - reasoning.ParseLevel cannot carry it, because there an
// empty argument is a missing key rather than a request to clear.
func ParseEffortArg(arg string) (reasoning.Level, error) {
	if arg == EffortUnsetWord {
		return "", nil
	}
	return reasoning.ParseLevel(arg)
}

// FormatEffortSet confirms what the request will now carry, which is not the
// same as what was asked for: clearing the override on a model with a
// configured default puts that default back on the wire, and reporting the
// argument there would promise silence the provider never gets.
func FormatEffortSet(model string, requested reasoning.Level, effective reasoning.Setting) string {
	switch {
	case requested.Active():
		return fmt.Sprintf("reasoning effort set to %s for %s", effective.Level, model)
	case effective.Active():
		return fmt.Sprintf("reasoning effort choice cleared for %s: %s (model default) is in force",
			model, effective.Level)
	default:
		return fmt.Sprintf("reasoning effort %s for %s: no reasoning field is sent", EffortUnsetWord, model)
	}
}

// FormatEffortSummary is the no-argument answer on both surfaces: what is
// active now, and what else this model offers.
func FormatEffortSummary(model string, choices []reasoning.Level, current, fallback reasoning.Level) string {
	if len(choices) == 0 {
		return fmt.Sprintf("no reasoning effort configured for %s", model)
	}
	line := fmt.Sprintf("reasoning effort=%s for %s (offers %s",
		EffortRowName(current), model, reasoning.FormatLevels(choices))
	// The plain surface has no picker, so this line is the only place the unset
	// word is discoverable - but only where unset is a state this model can
	// reach, which is the same question the picker asks before adding its row.
	if len(choices) > 0 && !fallback.Active() {
		line += ", or " + EffortUnsetWord
	}
	return line + ")"
}

// FormatEffortStatus is the /status reading of the dial: the level plus the
// dialect that carries it, since the same level reaches different providers as
// different JSON. A model with no reasoning surface says so rather than
// leaving the field blank.
//
// offersReasoning is a separate argument because "has this model anything to
// offer" is a question about its DECLARED SET, which no dialect value answers:
// an absent dialect resolves to the provider's default, and a declared one is a
// wire shape for levels that may not exist. Callers take it from
// Session.ReasoningChoices, the same set /effort accepts against.
func FormatEffortStatus(setting reasoning.Setting, offersReasoning bool) string {
	if !offersReasoning {
		return "none · model declares no reasoning efforts"
	}
	if !setting.Active() {
		return EffortUnsetWord + " · no reasoning field is sent"
	}
	if setting.Dialect == "" {
		return string(setting.Level)
	}
	return fmt.Sprintf("%s · %s", setting.Level, setting.Dialect)
}

// HandleSlashEffort is the plain-surface /effort. There is no picker here, so
// the no-argument form prints what the picker would have shown: the active
// level and the set this model offers, or why there is nothing to choose.
func HandleSlashEffort(fields []string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	sink := SlashSinkFor(term)
	model := sess.CurrentModel()
	if len(fields) < 2 {
		sink.Info(FormatEffortSummary(model, sess.ReasoningChoices(), sess.ReasoningEffort(), sess.ReasoningDefault()))
		return true, false, nil
	}
	level, err := ParseEffortArg(strings.TrimSpace(fields[1]))
	if err != nil {
		sink.Error(err.Error())
		return true, false, nil
	}
	if err := sess.SetReasoningEffort(level); err != nil {
		sink.Error(SafeEffortError(err))
		return true, false, nil
	}
	sink.Info(FormatEffortSet(model, level, sess.ReasoningSetting()))
	if jsink, ok := sink.(*JSONSlashSink); ok {
		jsink.EffortChanged(model, level)
	}
	return true, false, nil
}
